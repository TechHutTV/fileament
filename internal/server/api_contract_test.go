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

func TestListModelsSupportsPunctuationCollectionAndSort(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	model := uploadSTLModel(t, app, cookie, "gear.stl", "Gear (M3/M4) bracket")
	req := httptest.NewRequest(http.MethodPatch, "/api/models/"+model.ID, strings.NewReader(`{"title":"Gear (M3/M4)","tags":["Print Tools"]}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status=%d body=%s", rec.Code, rec.Body.String())
	}
	req = httptest.NewRequest(http.MethodPost, "/api/collections", strings.NewReader(`{"name":"Fixtures"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	var col Collection
	_ = json.Unmarshal(rec.Body.Bytes(), &col)
	req = httptest.NewRequest(http.MethodPut, "/api/collections/"+col.ID+"/models/"+model.ID, nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("add collection status=%d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodGet, "/api/models?q=(M3/M4)&collection="+col.Slug+"&sort=title", nil)
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("filtered list status=%d body=%s", rec.Code, rec.Body.String())
	}
	var page struct {
		Items []Model `json:"items"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &page)
	if len(page.Items) != 1 || page.Items[0].ID != model.ID {
		t.Fatalf("unexpected filtered page %#v", page)
	}
}

func TestShareValidationAndPasswordChange(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	req := httptest.NewRequest(http.MethodPost, "/api/shares", strings.NewReader(`{"scope":"model","targetId":"missing"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing share target status=%d", rec.Code)
	}
	req = httptest.NewRequest(http.MethodPost, "/api/auth/password", strings.NewReader(`{"currentPassword":"password-password","newPassword":"new-password-ok"}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("password change status=%d body=%s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	app.Router().ServeHTTP(rec, jsonReq(http.MethodPost, "/api/auth/login", `{"password":"new-password-ok"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("new password login status=%d", rec.Code)
	}
}

func TestExpiredShareCreateIsRejected(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	model := uploadSTLModel(t, app, cookie, "old.stl", "Old")
	req := httptest.NewRequest(http.MethodPost, "/api/shares", strings.NewReader(`{"scope":"model","targetId":"`+model.ID+`","expiresAt":`+strconvInt(time.Now().Add(-time.Hour).Unix())+`}`))
	req.Header.Set("Content-Type", "application/json")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expired share create status=%d", rec.Code)
	}
}

func strconvInt(n int64) string {
	return strconv.FormatInt(n, 10)
}
