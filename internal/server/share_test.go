package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/TechHutTV/fileament/internal/config"
)

func TestCollectionsAndPublicSharesAreScoped(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	modelA := uploadSTLModel(t, app, cookie, "alpha.stl", "Alpha")
	modelB := uploadSTLModel(t, app, cookie, "beta.stl", "Beta")

	req := httptest.NewRequest(http.MethodPost, "/api/collections", strings.NewReader(`{"name":"Print Farm","description":"Useful parts"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("collection create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var col Collection
	_ = json.Unmarshal(rec.Body.Bytes(), &col)

	req = httptest.NewRequest(http.MethodPut, "/api/collections/"+col.ID+"/models/"+modelA.ID, nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("collection add status=%d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/shares", strings.NewReader(`{"scope":"model","targetId":"`+modelA.ID+`","label":"alpha"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("share create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var share ShareLink
	_ = json.Unmarshal(rec.Body.Bytes(), &share)

	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/public/"+share.Token, nil))
	if rec.Code != http.StatusOK || rec.Header().Get("X-Robots-Tag") != "noindex" {
		t.Fatalf("public status=%d robots=%q", rec.Code, rec.Header().Get("X-Robots-Tag"))
	}
	assertShareHits(t, app, share.ID, 1)
	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/public/"+share.Token+"/status", nil))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("public status check=%d", rec.Code)
	}
	assertShareHits(t, app, share.ID, 1)

	assetPaths := []string{
		"/api/public/" + share.Token + "/files/" + modelA.Files[0].ID,
		"/api/public/" + share.Token + "/mesh/" + modelA.Files[0].ID,
		"/api/public/" + share.Token + "/thumbs/missing.png",
		"/api/public/" + share.Token + "/images/missing",
	}
	for _, path := range assetPaths {
		rec = httptest.NewRecorder()
		app.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	}
	assertShareHits(t, app, share.ID, 1)

	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/public/"+share.Token+"/files/"+modelB.Files[0].ID, nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-model public file status=%d", rec.Code)
	}
	assertShareHits(t, app, share.ID, 1)

	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/public/"+share.Token, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("second public status=%d", rec.Code)
	}
	assertShareHits(t, app, share.ID, 2)

	req = httptest.NewRequest(http.MethodGet, "/api/shares", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	var listed []struct {
		TargetName string `json:"targetName"`
		URL        string `json:"url"`
		HitCount   int64  `json:"hitCount"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &listed) != nil || len(listed) != 1 {
		t.Fatalf("share list status=%d body=%s", rec.Code, rec.Body.String())
	}
	if listed[0].TargetName != modelA.Title || listed[0].URL != "http://example.com/s/"+share.Token || listed[0].HitCount != 2 {
		t.Fatalf("share list metadata=%+v", listed[0])
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/shares/"+share.ID, nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke status=%d", rec.Code)
	}
	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/public/"+share.Token+"/status", nil))
	if rec.Code != http.StatusGone {
		t.Fatalf("revoked public status check=%d", rec.Code)
	}
	assertShareHits(t, app, share.ID, 2)
	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/public/"+share.Token, nil))
	if rec.Code != http.StatusGone {
		t.Fatalf("revoked public status=%d", rec.Code)
	}
}

func TestExpiredShareIsGone(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	model := uploadSTLModel(t, app, cookie, "old.stl", "Old")
	expires := time.Now().Add(-time.Hour).Unix()
	req := httptest.NewRequest(http.MethodPost, "/api/shares", strings.NewReader(`{"scope":"model","targetId":"`+model.ID+`","expiresAt":`+strconv.FormatInt(expires, 10)+`}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("share create status=%d", rec.Code)
	}
}

func TestShareResponsesUseConfiguredBaseURL(t *testing.T) {
	app := newTestAppWithConfig(t, config.Config{DataDir: t.TempDir(), OwnerPassword: "password-password", ThumbWorkers: 0, MaxUploadMB: 32, BaseURL: "https://models.example/library/"})
	cookie := loginCookie(t, app, "password-password")
	model := uploadSTLModel(t, app, cookie, "fixture.stl", "Fixture")

	req := httptest.NewRequest(http.MethodPost, "/api/shares", strings.NewReader(`{"scope":"model","targetId":"`+model.ID+`"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	var created struct {
		URL        string `json:"url"`
		TargetName string `json:"targetName"`
	}
	if rec.Code != http.StatusCreated || json.Unmarshal(rec.Body.Bytes(), &created) != nil {
		t.Fatalf("share create status=%d body=%s", rec.Code, rec.Body.String())
	}
	if created.URL == "" || !strings.HasPrefix(created.URL, "https://models.example/s/") || created.TargetName != model.Title {
		t.Fatalf("share create metadata=%+v", created)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/shares", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	var listed []struct {
		URL        string `json:"url"`
		TargetName string `json:"targetName"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &listed) != nil || len(listed) != 1 {
		t.Fatalf("share list status=%d body=%s", rec.Code, rec.Body.String())
	}
	if listed[0].URL != created.URL || listed[0].TargetName != model.Title {
		t.Fatalf("share list metadata=%+v created=%+v", listed[0], created)
	}
}

func assertShareHits(t *testing.T, app *App, shareID string, want int64) {
	t.Helper()
	var got int64
	if err := app.db.QueryRow(`SELECT hit_count FROM share_links WHERE id = ?`, shareID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("share hits=%d want=%d", got, want)
	}
}
