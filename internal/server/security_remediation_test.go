package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TechHutTV/fileament/internal/config"
)

func TestOwnerAssetsRequireAuthenticationAndImagesAreScoped(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	model := uploadSTLModel(t, app, cookie, "private.stl", "Private")
	if err := app.processNextThumbnail(); err != nil {
		t.Fatal(err)
	}
	model, err := app.getModel(model.ID)
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/thumbs/"+model.ID+"/card.jpg", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth thumb status=%d", rec.Code)
	}

	body, ctype := multipartFile(t, "photo.png", pngBytes(t))
	req := httptest.NewRequest(http.MethodPost, "/api/models/"+model.ID+"/images", body)
	req.Header.Set("Content-Type", ctype)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("image upload status=%d body=%s", rec.Code, rec.Body.String())
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &model)
	if len(model.Images) != 1 {
		t.Fatalf("expected uploaded image in model: %#v", model.Images)
	}
	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/images/"+model.ID+"/"+model.Images[0].ID, nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth image status=%d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/images/"+model.ID+"/"+model.Images[0].ID, nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("auth image status=%d", rec.Code)
	}
}

func TestAdditionalFilesAndDeletesUpdateSidecarAndJobs(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	model := uploadSTLModel(t, app, cookie, "base.stl", "Base")
	body, ctype := multipartFile(t, "extra.stl", []byte(validSTL()))
	req := httptest.NewRequest(http.MethodPost, "/api/models/"+model.ID+"/files", body)
	req.Header.Set("Content-Type", ctype)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("add file status=%d body=%s", rec.Code, rec.Body.String())
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &model)
	if len(model.Files) != 2 {
		t.Fatalf("files after append=%d", len(model.Files))
	}
	var pending int
	_ = app.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE status = 'pending'`).Scan(&pending)
	if pending != 2 {
		t.Fatalf("pending jobs=%d", pending)
	}
	req = httptest.NewRequest(http.MethodDelete, "/api/models/"+model.ID+"/files/"+model.Files[1].ID, nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete file status=%d body=%s", rec.Code, rec.Body.String())
	}
	sidecar, err := os.ReadFile(filepath.Join(app.cfg.DataDir, "models", model.ID, "model.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sidecar), model.Files[1].ID) {
		t.Fatal("deleted file remains in sidecar")
	}
}

func TestPublicImagesAndThumbsAreTokenScoped(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	modelA := uploadSTLModel(t, app, cookie, "a.stl", "A")
	modelB := uploadSTLModel(t, app, cookie, "b.stl", "B")
	body, ctype := multipartFile(t, "photo.png", pngBytes(t))
	req := httptest.NewRequest(http.MethodPost, "/api/models/"+modelB.ID+"/images", body)
	req.Header.Set("Content-Type", ctype)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("image upload status=%d", rec.Code)
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &modelB)
	req = httptest.NewRequest(http.MethodPost, "/api/shares", strings.NewReader(`{"scope":"model","targetId":"`+modelA.ID+`","label":"a"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	var share ShareLink
	_ = json.Unmarshal(rec.Body.Bytes(), &share)
	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/public/"+share.Token+"/images/"+modelB.Images[0].ID, nil))
	if rec.Code != http.StatusNotFound || rec.Header().Get("X-Robots-Tag") == "" {
		t.Fatalf("cross-scope image status=%d robots=%q", rec.Code, rec.Header().Get("X-Robots-Tag"))
	}
}

func TestSecureCookieFromHTTPSBaseURL(t *testing.T) {
	app := newTestAppWithConfig(t, config.Config{DataDir: t.TempDir(), OwnerPassword: "password-password", ThumbWorkers: 0, MaxUploadMB: 32, BaseURL: "https://fileament.example"})
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, jsonReq(http.MethodPost, "/api/auth/login", `{"password":"password-password"}`))
	if rec.Code != http.StatusOK || len(rec.Result().Cookies()) == 0 || !rec.Result().Cookies()[0].Secure {
		t.Fatalf("expected secure cookie, status=%d cookies=%#v", rec.Code, rec.Result().Cookies())
	}
}

func TestForeignKeysAreEnabledOnEveryPooledConnection(t *testing.T) {
	app := newAuthedTestApp(t)
	ctx := context.Background()
	first, err := app.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := app.db.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	for i, conn := range []*sql.Conn{first, second} {
		var enabled int
		if err := conn.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&enabled); err != nil {
			t.Fatal(err)
		}
		if enabled != 1 {
			t.Fatalf("connection %d foreign_keys=%d", i+1, enabled)
		}
	}
}

func TestLargestMeshBecomesDefaultCardRegardlessOfJobOrder(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	model := uploadSTLModel(t, app, cookie, "small.stl", "Meshes")
	largeSTL := strings.ReplaceAll(validSTL(), "vertex 1 0 0", "vertex 10 0 0") + strings.Repeat("\n# padding", 100)
	body, ctype := multipartFile(t, "large.stl", []byte(largeSTL))
	req := httptest.NewRequest(http.MethodPost, "/api/models/"+model.ID+"/files", body)
	req.Header.Set("Content-Type", ctype)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("append status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	if _, err := app.db.Exec(`UPDATE jobs SET created_at = CASE file_id WHEN ? THEN 1 ELSE 2 END WHERE file_id IN (?, ?)`, model.Files[0].ID, model.Files[0].ID, model.Files[1].ID); err != nil {
		t.Fatal(err)
	}
	if err := app.processNextThumbnail(); err != nil {
		t.Fatal(err)
	}
	if err := app.processNextThumbnail(); err != nil {
		t.Fatal(err)
	}
	card, err := os.ReadFile(filepath.Join(app.cfg.DataDir, "models", model.ID, "thumbs", "card.png"))
	if err != nil {
		t.Fatal(err)
	}
	large, err := os.ReadFile(filepath.Join(app.cfg.DataDir, "models", model.ID, "thumbs", model.Files[1].ID+".png"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(card, large) {
		t.Fatal("card thumbnail was not generated from the largest mesh")
	}
}

func TestStartupRecoversSidecarsAndRunningJobs(t *testing.T) {
	dir := t.TempDir()
	webDir := testWebDir(t)
	app := newTestAppWithConfig(t, config.Config{DataDir: dir, WebDir: webDir, OwnerPassword: "password-password", ThumbWorkers: 0, MaxUploadMB: 32})
	cookie := loginCookie(t, app, "password-password")
	model := uploadSTLModel(t, app, cookie, "recover.stl", "Recover")
	if _, err := app.db.Exec(`UPDATE jobs SET status = 'running'`); err != nil {
		t.Fatal(err)
	}
	if _, err := app.db.Exec(`DELETE FROM files`); err != nil {
		t.Fatal(err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app2, err := New(config.Config{DataDir: dir, WebDir: webDir, OwnerPassword: "password-password", ThumbWorkers: 0, MaxUploadMB: 32}, os.DirFS(webDir))
	if err != nil {
		t.Fatal(err)
	}
	defer app2.Close()
	var files int
	_ = app2.db.QueryRow(`SELECT COUNT(*) FROM files WHERE model_id = ?`, model.ID).Scan(&files)
	var status string
	_ = app2.db.QueryRow(`SELECT status FROM jobs LIMIT 1`).Scan(&status)
	if files != 1 || status != "pending" {
		t.Fatalf("recovery files=%d status=%q", files, status)
	}
}

func TestStartupRebuildsCollectionsFromDurableSidecar(t *testing.T) {
	dir := t.TempDir()
	cfg := config.Config{DataDir: dir, WebDir: testWebDir(t), OwnerPassword: "password-password", ThumbWorkers: 0, MaxUploadMB: 32}
	app := newTestAppWithConfig(t, cfg)
	cookie := loginCookie(t, app, "password-password")
	model := uploadSTLModel(t, app, cookie, "member.stl", "Member")

	req := jsonReq(http.MethodPost, "/api/collections", `{"name":"Durable"}`)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create collection status=%d body=%s", rec.Code, rec.Body.String())
	}
	var collection Collection
	if err := json.Unmarshal(rec.Body.Bytes(), &collection); err != nil {
		t.Fatal(err)
	}
	req = httptest.NewRequest(http.MethodPut, "/api/collections/"+collection.ID+"/models/"+model.ID, nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("add member status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = jsonReq(http.MethodPatch, "/api/collections/"+collection.ID, `{"name":"Durable","coverModelId":"`+model.ID+`"}`)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("set cover status=%d body=%s", rec.Code, rec.Body.String())
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(filepath.Join(dir, "fileament.db") + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}

	app2, err := New(cfg, os.DirFS(cfg.WebDir))
	if err != nil {
		t.Fatal(err)
	}
	defer app2.Close()
	rebuilt, err := app2.getCollection(collection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.CoverModelID != model.ID || len(rebuilt.ModelIDs) != 1 || rebuilt.ModelIDs[0] != model.ID {
		t.Fatalf("collection was not rebuilt from sidecar: %#v", rebuilt)
	}
}

func multipartFile(t *testing.T, name string, data []byte) (*bytes.Buffer, string) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, err := mw.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(data)
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	return &body, mw.FormDataContentType()
}

func pngBytes(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 1, 1))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func validSTL() string {
	return `solid p
facet normal 0 0 1
outer loop
vertex 0 0 0
vertex 1 0 0
vertex 0 1 0
endloop
endfacet
endsolid p`
}
