package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOwnerContextTracksValidSessionsWithoutAuthorizingRequests(t *testing.T) {
	app := newAuthedTestApp(t)
	read := func(cookie *http.Cookie) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		rec := httptest.NewRecorder()
		app.Router().ServeHTTP(rec, req)
		var me struct {
			Authenticated bool   `json:"authenticated"`
			Context       string `json:"context"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &me) != nil {
			t.Fatal("could not read owner context")
		}
		if me.Authenticated != (me.Context != "") {
			t.Fatal("owner context must be present only for an authenticated session")
		}
		if rec.Header().Get("Cache-Control") != "private, no-store" {
			t.Fatal("owner context response must not be cached")
		}
		return me.Context
	}
	if read(nil) != "" {
		t.Fatal("anonymous request received an owner context")
	}
	first := loginCookie(t, app, "password-password")
	firstContext := read(first)
	if len(firstContext) != 64 || firstContext == first.Value || read(first) != firstContext {
		t.Fatal("context must be a stable identifier distinct from the session credential")
	}
	if sessionTestStatus(app, &http.Cookie{Name: sessionCookieName, Value: firstContext}) != http.StatusUnauthorized {
		t.Fatal("context must not authorize a request")
	}
	second := loginCookie(t, app, "password-password")
	if read(second) == firstContext {
		t.Fatal("different sessions shared an owner context")
	}
	stale := jsonReq(http.MethodPost, "/api/collections", `{"name":"Stale collection"}`)
	stale.AddCookie(second)
	stale.Header.Set("X-Fileament-Context", firstContext)
	rejected := httptest.NewRecorder()
	app.Router().ServeHTTP(rejected, stale)
	var collections int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM collections`).Scan(&collections); err != nil {
		t.Fatal(err)
	}
	if rejected.Code != http.StatusUnauthorized || collections != 0 {
		t.Fatal("a stale page mutated the replacement session's library")
	}
	for _, context := range []string{"", read(second)} {
		req := httptest.NewRequest(http.MethodGet, "/api/collections", nil)
		req.AddCookie(second)
		req.Header.Set("X-Fileament-Context", context)
		rec := httptest.NewRecorder()
		app.Router().ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatal("matching context or ordinary cookie authentication was rejected")
		}
	}
	changed := changeTestPassword(app, first)
	if changed.Code != http.StatusNoContent || len(changed.Result().Cookies()) != 1 {
		t.Fatal("password change did not replace the session")
	}
	if read(first) != "" || read(second) != "" || read(changed.Result().Cookies()[0]) == firstContext {
		t.Fatal("rotation retained a previous context")
	}
	if _, err := app.db.Exec(`UPDATE sessions SET expires_at = ?`, time.Now().Add(-time.Minute).Unix()); err != nil {
		t.Fatal(err)
	}
	if read(changed.Result().Cookies()[0]) != "" {
		t.Fatal("expired session retained its context")
	}
}
