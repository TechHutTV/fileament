package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestOwnerSetupLoginAndLogout(t *testing.T) {
	app := newTestApp(t)
	router := app.Router()

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/me", nil))
	var me map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	if me["setupRequired"] != true {
		t.Fatalf("expected setupRequired before owner exists, got %#v", me)
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, jsonReq(http.MethodPost, "/api/auth/setup", `{"password":"correct horse battery staple"}`))
	if rec.Code != http.StatusCreated {
		t.Fatalf("setup status = %d body=%s", rec.Code, rec.Body.String())
	}
	if countSetting(t, app, "owner_password_hash") != 1 {
		t.Fatal("owner password hash was not stored")
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, jsonReq(http.MethodPost, "/api/auth/login", `{"password":"wrong"}`))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong login status = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, jsonReq(http.MethodPost, "/api/auth/login", `{"password":"correct horse battery staple"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("login status = %d body=%s", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookieName || !cookies[0].HttpOnly {
		t.Fatalf("expected secure session cookie, got %#v", cookies)
	}
	if countSessions(t, app) != 1 {
		t.Fatal("session was not stored in sqlite")
	}

	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	req.AddCookie(cookies[0])
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	me = map[string]any{}
	if err := json.Unmarshal(rec.Body.Bytes(), &me); err != nil {
		t.Fatal(err)
	}
	if me["authenticated"] != true {
		t.Fatalf("expected authenticated me, got %#v", me)
	}

	req = httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.AddCookie(cookies[0])
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout status = %d", rec.Code)
	}
	if countSessions(t, app) != 0 {
		t.Fatal("session was not removed on logout")
	}
}

func TestOwnerPasswordCanSeedFromEnvironment(t *testing.T) {
	app := newTestAppWithPassword(t, "seeded-password")
	if countSetting(t, app, "owner_password_hash") != 1 {
		t.Fatal("seeded password hash was not stored")
	}
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, jsonReq(http.MethodPost, "/api/auth/login", `{"password":"seeded-password"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("seeded login status = %d body=%s", rec.Code, rec.Body.String())
	}
}

func jsonReq(method, path, body string) *http.Request {
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	return req
}

func countSetting(t *testing.T, app *App, key string) int {
	t.Helper()
	var n int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key = ?`, key).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func countSessions(t *testing.T, app *App) int {
	t.Helper()
	var n int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
