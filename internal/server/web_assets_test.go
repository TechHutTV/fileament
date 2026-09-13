package server

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHashedWebAssetTransfer(t *testing.T) {
	app := newTestApp(t)
	data := []byte(strings.Repeat("export const model = 'Fileament';\n", 100))
	if err := os.MkdirAll(filepath.Join(app.cfg.WebDir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	name := "assets/index-1234abcd.js"
	if err := os.WriteFile(filepath.Join(app.cfg.WebDir, name), data, 0o644); err != nil {
		t.Fatal(err)
	}
	var compressed bytes.Buffer
	z := gzip.NewWriter(&compressed)
	if _, err := z.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app.cfg.WebDir, name+".gz"), compressed.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	request := func(method, encoding, tag string, extra ...map[string]string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "/"+name, nil)
		r.Header.Set("Accept-Encoding", encoding)
		if tag != "" {
			r.Header.Set("If-None-Match", tag)
		}
		for _, headers := range extra {
			for key, value := range headers {
				r.Header.Set(key, value)
			}
		}
		w := httptest.NewRecorder()
		app.Router().ServeHTTP(w, r)
		return w
	}
	w := request(http.MethodGet, "gzip", "")
	if w.Code != http.StatusOK || w.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("status=%d encoding=%q", w.Code, w.Header().Get("Content-Encoding"))
	}
	if w.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" || w.Header().Get("Vary") != "Accept-Encoding" {
		t.Fatalf("headers=%v", w.Header())
	}
	reader, err := gzip.NewReader(w.Body)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("decoded asset changed")
	}
	tag := w.Header().Get("ETag")
	if tag == "" {
		t.Fatal("missing asset validator")
	}
	if cached := request(http.MethodGet, "gzip", tag); cached.Code != http.StatusNotModified || cached.Body.Len() != 0 {
		t.Fatalf("conditional response=%d", cached.Code)
	}
	if identity := request(http.MethodGet, "identity", tag); identity.Code != http.StatusOK || !bytes.Equal(identity.Body.Bytes(), data) || identity.Header().Get("Content-Encoding") != "" {
		t.Fatalf("identity response=%d", identity.Code)
	}
	if head := request(http.MethodHead, "gzip", ""); head.Code != http.StatusOK || head.Body.Len() != 0 || head.Header().Get("ETag") != tag {
		t.Fatalf("HEAD response=%d body=%d", head.Code, head.Body.Len())
	}
	for _, tc := range []struct {
		header, encoding string
		status           int
	}{
		{"", "", 200}, {"br", "", 200}, {"gzip;q=0", "", 200},
		{"GZIP; q=1.0", "gzip", 200}, {"x-gzip", "gzip", 200}, {"*", "gzip", 200},
		{"gzip;q=0, *;q=1", "", 200}, {"gzip;q=0.5, identity;q=1", "", 200},
		{"gzip;q=0.5, identity;q=0.1", "gzip", 200}, {"gzip, identity;q=0", "gzip", 200},
		{"gzip;q=0.5", "gzip", 200}, {"*;q=0, identity;q=1", "", 200},
		{"*;q=0", "", 406}, {"br, identity;q=0", "", 406},
		{"gzip;q=NaN", "", 200}, {"gzip;q=1.1", "", 200}, {"gzip;q=-1", "", 200},
		{"gzip;q=1;q=0", "", 200}, {"gzip;q=1, gzip;q=0", "", 200},
	} {
		t.Run(tc.header, func(t *testing.T) {
			response := request(http.MethodGet, tc.header, "")
			if response.Code != tc.status || response.Header().Get("Content-Encoding") != tc.encoding {
				t.Fatalf("status=%d encoding=%q", response.Code, response.Header().Get("Content-Encoding"))
			}
			if response.Code == 406 && strings.Contains(response.Header().Get("Cache-Control"), "immutable") {
				t.Fatal("cached negotiation error")
			}
		})
	}
	partial := request(http.MethodGet, "identity", "", map[string]string{"Range": "bytes=0-9"})
	if partial.Code != http.StatusPartialContent || !bytes.Equal(partial.Body.Bytes(), data[:10]) {
		t.Fatalf("identity range=%d body=%q", partial.Code, partial.Body.String())
	}
	multi := request(http.MethodGet, "identity", "", map[string]string{"Range": "bytes=0-9,20-29"})
	if multi.Code != http.StatusPartialContent || !strings.HasPrefix(multi.Header().Get("Content-Type"), "multipart/byteranges;") {
		t.Fatalf("multiple identity ranges=%d type=%q", multi.Code, multi.Header().Get("Content-Type"))
	}
	for _, ranges := range []string{"bytes=0-9", "bytes=0-9,20-29"} {
		whole := request(http.MethodGet, "gzip", "", map[string]string{"Range": ranges})
		if whole.Code != http.StatusOK || !bytes.Equal(whole.Body.Bytes(), compressed.Bytes()) {
			t.Fatalf("gzip range=%d bytes=%d", whole.Code, whole.Body.Len())
		}
	}
	if err := os.Remove(filepath.Join(app.cfg.WebDir, name+".gz")); err != nil {
		t.Fatal(err)
	}
	if fallback := request(http.MethodGet, "gzip", ""); fallback.Code != http.StatusOK || !bytes.Equal(fallback.Body.Bytes(), data) {
		t.Fatalf("missing gzip fallback=%d", fallback.Code)
	}
	if rejected := request(http.MethodGet, "gzip, identity;q=0", ""); rejected.Code != http.StatusNotAcceptable {
		t.Fatalf("unavailable encoding=%d", rejected.Code)
	}
}

func TestWebHTMLAndMissingAssetsRemainRefreshable(t *testing.T) {
	app := newTestApp(t)
	for _, path := range []string{"/", "/models/example", "/s/example", "/assets/missing-1234abcd.js", "/assets/"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		app.Router().ServeHTTP(w, r)
		if strings.HasPrefix(path, "/assets/") {
			if w.Code != http.StatusNotFound || strings.Contains(w.Body.String(), "Fileament test UI") {
				t.Fatalf("missing asset=%d %s", w.Code, w.Body.String())
			}
		} else if w.Code != http.StatusOK {
			t.Fatalf("HTML status=%d", w.Code)
		}
		wantCache := "no-cache"
		if strings.HasPrefix(path, "/s/") {
			wantCache = "private, no-store"
			if w.Header().Get("X-Robots-Tag") != "noindex" {
				t.Fatal("share HTML lost noindex")
			}
		}
		if w.Header().Get("Cache-Control") != wantCache || w.Header().Get("Content-Encoding") != "" {
			t.Fatalf("%s headers=%v", path, w.Header())
		}
	}
	if err := os.WriteFile(filepath.Join(app.cfg.WebDir, "index.html"), []byte("<!doctype html><title>New UI</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	app.Router().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if !strings.Contains(w.Body.String(), "New UI") {
		t.Fatal("served stale HTML")
	}
}
