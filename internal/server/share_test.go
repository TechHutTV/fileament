package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
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

	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/public/"+share.Token+"/files/"+modelB.Files[0].ID, nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("cross-model public file status=%d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodDelete, "/api/shares/"+share.ID, nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("revoke status=%d", rec.Code)
	}
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
	if rec.Code != http.StatusCreated {
		t.Fatalf("share create status=%d", rec.Code)
	}
	var share ShareLink
	_ = json.Unmarshal(rec.Body.Bytes(), &share)
	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/public/"+share.Token, nil))
	if rec.Code != http.StatusGone {
		t.Fatalf("expired public status=%d", rec.Code)
	}
}
