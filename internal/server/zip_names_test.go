package server

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestZIPNameCollisionsPreserveDownloadsDeletionAndBackup(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	var archive bytes.Buffer
	writer := zip.NewWriter(&archive)
	wantMeshes, wantImages := map[string]bool{}, map[string]bool{}
	for i, name := range []string{"part-1", "a/part", "b/PART", "c/part"} {
		body := []byte(strings.Replace(validSTL(), "vertex 1 0 0", fmt.Sprintf("vertex %d 0 0", i+2), 1))
		sum := sha256.Sum256(body)
		wantMeshes[hex.EncodeToString(sum[:])] = true
		entry, err := writer.Create(name + ".stl")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(body); err != nil {
			t.Fatal(err)
		}
		var pngBody bytes.Buffer
		img := image.NewRGBA(image.Rect(0, 0, 1, 1))
		img.Set(0, 0, color.RGBA{R: uint8(i * 50), A: 255})
		if err := png.Encode(&pngBody, img); err != nil {
			t.Fatal(err)
		}
		wantImages[pngBody.String()] = true
		entry, err = writer.Create(name + ".png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write(pngBody.Bytes()); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "bundle.zip")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(archive.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/models", &body)
	req.Header.Set("Content-Type", form.FormDataContentType())
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var model Model
	if err := json.Unmarshal(rec.Body.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	if len(model.Files) != 4 || len(model.Images) != 4 {
		t.Fatalf("files=%d images=%d", len(model.Files), len(model.Images))
	}
	seen := map[string]bool{}
	for _, file := range model.Files {
		if seen[strings.ToLower(file.RelPath)] {
			t.Fatalf("duplicate path: %s", file.RelPath)
		}
		seen[strings.ToLower(file.RelPath)] = true
		response := getWithCookie(t, app, cookie, "/files/"+model.ID+"/"+file.ID)
		sum := sha256.Sum256(response.Body.Bytes())
		got := hex.EncodeToString(sum[:])
		if response.Code != http.StatusOK || got != file.SHA256 || !wantMeshes[got] {
			t.Fatalf("corrupted download: %s", file.Filename)
		}
		delete(wantMeshes, got)
	}
	imageBodies := map[string][]byte{}
	for _, img := range model.Images {
		if seen[strings.ToLower(img.RelPath)] {
			t.Fatalf("duplicate path: %s", img.RelPath)
		}
		seen[strings.ToLower(img.RelPath)] = true
		response := getWithCookie(t, app, cookie, "/images/"+model.ID+"/"+img.ID)
		if response.Code != http.StatusOK || !wantImages[response.Body.String()] {
			t.Fatalf("corrupted image: %s", img.RelPath)
		}
		delete(wantImages, response.Body.String())
		imageBodies[img.ID] = response.Body.Bytes()
	}
	backup := downloadBackup(t, app, cookie)
	inspection := inspectBackup(t, app, cookie, backup)
	if inspection.Manifest.Files != 4 {
		t.Fatalf("backup file count=%d", inspection.Manifest.Files)
	}
	for index, route := range []string{"/api/models/" + model.ID + "/files/" + model.Files[0].ID, "/api/models/" + model.ID + "/images/" + model.Images[0].ID} {
		req := httptest.NewRequest(http.MethodDelete, route, nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		app.Router().ServeHTTP(rec, req)
		wantStatus := http.StatusOK
		if index == 1 {
			wantStatus = http.StatusNoContent
		}
		if rec.Code != wantStatus {
			t.Fatalf("delete status=%d body=%s", rec.Code, rec.Body.String())
		}
	}
	for _, file := range model.Files[1:] {
		if got := getWithCookie(t, app, cookie, "/files/"+model.ID+"/"+file.ID); got.Code != http.StatusOK {
			t.Fatalf("deleting another file removed %s", file.Filename)
		}
	}
	for _, img := range model.Images[1:] {
		if got := getWithCookie(t, app, cookie, "/images/"+model.ID+"/"+img.ID); got.Code != http.StatusOK {
			t.Fatalf("deleting another image removed %s", img.RelPath)
		}
	}
	inspectBackup(t, app, cookie, downloadBackup(t, app, cookie))
	inspection = inspectBackup(t, app, cookie, backup)
	restoreReq := jsonReq(http.MethodPost, "/api/backups/restore", `{"restoreToken":"`+inspection.RestoreToken+`","confirmation":"RESTORE"}`)
	restoreReq.AddCookie(cookie)
	restoreRec := httptest.NewRecorder()
	app.Router().ServeHTTP(restoreRec, restoreReq)
	if restoreRec.Code != http.StatusOK {
		t.Fatalf("restore status=%d body=%s", restoreRec.Code, restoreRec.Body.String())
	}
	cookie = loginCookie(t, app, "password-password")
	for _, file := range model.Files {
		response := getWithCookie(t, app, cookie, "/files/"+model.ID+"/"+file.ID)
		sum := sha256.Sum256(response.Body.Bytes())
		if response.Code != http.StatusOK || hex.EncodeToString(sum[:]) != file.SHA256 {
			t.Fatalf("restored file changed: %s", file.Filename)
		}
	}
	for _, img := range model.Images {
		response := getWithCookie(t, app, cookie, "/images/"+model.ID+"/"+img.ID)
		if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), imageBodies[img.ID]) {
			t.Fatalf("restored image changed: %s", img.RelPath)
		}
	}
}

func TestCappedCopyDoesNotOverwriteExistingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "part.stl")
	if err := os.WriteFile(path, []byte("original"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := copyCapped(path, strings.NewReader("replacement"), 32); err == nil {
		t.Fatal("copy reused an existing destination")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "original" {
		t.Fatalf("contents=%q err=%v", data, err)
	}
}

func getWithCookie(t *testing.T, app *App, cookie *http.Cookie, path string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	return rec
}
