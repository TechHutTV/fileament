//go:build embedded_ui

package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TechHutTV/fileament/internal/config"
	"github.com/TechHutTV/fileament/internal/server"
)

func TestEmbeddedUIServesWithoutExternalDirectory(t *testing.T) {
	cfg := config.Config{DataDir: t.TempDir(), WebDir: t.TempDir() + "/missing", MaxUploadMB: 32, ThumbWorkers: 0}
	web, err := webFilesystem(cfg)
	if err != nil {
		t.Fatal(err)
	}
	app, err := server.New(cfg, web)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	httpServer := httptest.NewServer(app.Router())
	defer httpServer.Close()
	assets, err := fs.Glob(web, "assets/index-*.js")
	if err != nil || len(assets) == 0 {
		t.Fatalf("hashed JavaScript is missing: %v", err)
	}
	for _, path := range []string{"/healthz", "/", "/" + assets[0]} {
		response, err := httpServer.Client().Get(httpServer.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("read=%v close=%v", readErr, closeErr)
		}
		if response.StatusCode != http.StatusOK || len(body) == 0 {
			t.Fatalf("%s: status=%d bytes=%d", path, response.StatusCode, len(body))
		}
		if path == "/" && !strings.Contains(string(body), "/assets/") {
			t.Fatal("index does not reference built assets")
		}
		if path == "/healthz" && !strings.Contains(string(body), `"status":"ok"`) {
			t.Fatal("health check failed")
		}
	}
	for _, pattern := range []string{"assets/*.js", "assets/*.css"} {
		names, err := fs.Glob(web, pattern)
		if err != nil || len(names) == 0 {
			t.Fatalf("missing %s assets: %v", pattern, err)
		}
		for _, name := range names {
			original, err := fs.ReadFile(web, name)
			if err != nil {
				t.Fatal(err)
			}
			req, err := http.NewRequest(http.MethodGet, httpServer.URL+"/"+name, nil)
			if err != nil {
				t.Fatal(err)
			}
			req.Header.Set("Accept-Encoding", "gzip")
			response, err := httpServer.Client().Do(req)
			if err != nil {
				t.Fatal(err)
			}
			encoded, readErr := io.ReadAll(response.Body)
			closeErr := response.Body.Close()
			if readErr != nil || closeErr != nil {
				t.Fatalf("read=%v close=%v", readErr, closeErr)
			}
			if response.StatusCode != http.StatusOK || response.Header.Get("Content-Encoding") != "gzip" || response.Header.Get("Vary") != "Accept-Encoding" || response.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
				t.Fatalf("%s: status=%d headers=%v", name, response.StatusCode, response.Header)
			}
			if response.ContentLength != int64(len(encoded)) || len(encoded) >= len(original) {
				t.Fatalf("%s: raw=%d wire=%d length=%d", name, len(original), len(encoded), response.ContentLength)
			}
			reader, err := gzip.NewReader(bytes.NewReader(encoded))
			if err != nil {
				t.Fatal(err)
			}
			decoded, readErr := io.ReadAll(reader)
			closeErr = reader.Close()
			if readErr != nil || closeErr != nil || !bytes.Equal(decoded, original) {
				t.Fatalf("%s decoded bytes differ: read=%v close=%v", name, readErr, closeErr)
			}
			t.Logf("%s: identity=%d bytes gzip=%d bytes", name, len(original), len(encoded))
		}
	}
}
