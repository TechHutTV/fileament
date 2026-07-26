package server

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestModelZipUploadPersistsFilesMetadataAndSidecar(t *testing.T) {
	app := newAuthedTestApp(t)
	body, contentType := multipartZip(t, map[string]string{
		"models/cube.stl": `solid p
facet normal 0 0 1
outer loop
vertex 0 0 0
vertex 1 0 0
vertex 0 1 0
endloop
endfacet
endsolid p`,
		"README.txt": "A useful model",
	})
	req := httptest.NewRequest(http.MethodPost, "/api/models", body)
	req.Header.Set("Content-Type", contentType)
	req.AddCookie(loginCookie(t, app, "password-password"))
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("upload status = %d body=%s", rec.Code, rec.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	id := created["id"].(string)
	sidecar := filepath.Join(app.cfg.DataDir, "models", id, "model.json")
	if _, err := os.Stat(sidecar); err != nil {
		t.Fatalf("sidecar missing: %v", err)
	}
	var files int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM files WHERE model_id = ? AND format = 'stl' AND triangle_count = 1`, id).Scan(&files); err != nil {
		t.Fatal(err)
	}
	if files != 1 {
		t.Fatalf("parsed files = %d", files)
	}
	if _, err := os.Stat(filepath.Join(app.cfg.DataDir, "models", id, "bundle.zip")); !os.IsNotExist(err) {
		t.Fatalf("source archive should not be retained: %v", err)
	}
}

func TestMultipartRequestCapIncludesHeadersAndOverhead(t *testing.T) {
	app := newAuthedTestApp(t)
	app.cfg.MaxUploadMB = 1
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="file"; filename="part.stl"`)
	header.Set("Content-Type", "application/octet-stream")
	header.Set("X-Fill", strings.Repeat("a", 3<<20))
	part, err := mw.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte(validSTL()))
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/models", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	_, cleanup, err := app.streamSingleUpload(httptest.NewRecorder(), req)
	if cleanup != nil {
		defer cleanup()
	}
	if err == nil || !strings.Contains(err.Error(), "request body too large") {
		t.Fatalf("expected whole-request size rejection, got %v", err)
	}
}

func TestZipSlipUploadIsRejected(t *testing.T) {
	app := newAuthedTestApp(t)
	body, contentType := multipartZip(t, map[string]string{"../escape.stl": "solid nope\nendsolid nope\n"})
	req := httptest.NewRequest(http.MethodPost, "/api/models", body)
	req.Header.Set("Content-Type", contentType)
	req.AddCookie(loginCookie(t, app, "password-password"))
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("zip slip status = %d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(app.cfg.DataDir, "escape.stl")); !os.IsNotExist(err) {
		t.Fatalf("zip slip wrote outside model dir: %v", err)
	}
}

func multipartZip(t *testing.T, files map[string]string) (io.Reader, string) {
	t.Helper()
	var zipBuf bytes.Buffer
	zw := zip.NewWriter(&zipBuf)
	for name, contents := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(contents))
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", "bundle.zip")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(zipBuf.Bytes())
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &body, mw.FormDataContentType()
}

func newAuthedTestApp(t *testing.T) *App {
	t.Helper()
	return newTestAppWithPassword(t, "password-password")
}

func loginCookie(t *testing.T, app *App, password string) *http.Cookie {
	t.Helper()
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, jsonReq(http.MethodPost, "/api/auth/login", `{"password":"`+password+`"}`))
	if rec.Code != http.StatusOK || len(rec.Result().Cookies()) == 0 {
		t.Fatalf("login failed: %d %s", rec.Code, rec.Body.String())
	}
	return rec.Result().Cookies()[0]
}
