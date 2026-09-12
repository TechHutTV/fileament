package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrowserOriginPolicyProtectsMutations(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	for i, test := range []struct {
		name, site, origin string
		want               int
	}{
		{"same-origin browser", "same-origin", "https://models.example.com", http.StatusCreated},
		{"sibling origin", "same-site", "https://untrusted.example.com", http.StatusForbidden},
		{"cross-site form", "cross-site", "https://attacker.example", http.StatusForbidden},
		{"legacy sibling", "", "https://untrusted.example.com", http.StatusForbidden},
		{"legacy same-host", "", "https://models.example.com", http.StatusCreated},
		{"opaque origin", "", "null", http.StatusForbidden},
		{"malformed origin", "", "://bad", http.StatusForbidden},
		{"command line client", "", "", http.StatusCreated},
	} {
		t.Run(test.name, func(t *testing.T) {
			req := jsonReq(http.MethodPost, "https://models.example.com/api/collections", fmt.Sprintf(`{"name":"Collection %d"}`, i))
			req.Header.Set("Origin", test.origin)
			req.Header.Set("Sec-Fetch-Site", test.site)
			req.AddCookie(cookie)
			rec := httptest.NewRecorder()
			app.Router().ServeHTTP(rec, req)
			if rec.Code != test.want {
				t.Fatalf("status=%d want=%d body=%s", rec.Code, test.want, rec.Body.String())
			}
			if test.want == http.StatusForbidden {
				var body map[string]string
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["error"] == "" {
					t.Fatalf("missing JSON error: %s", rec.Body.String())
				}
			}
		})
	}
	var count int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM collections`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("rejected browser requests changed state: count=%d err=%v", count, err)
	}
}

func TestCrossOriginPolicyCoversAuthUploadsAndBodylessActions(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	for _, test := range []struct{ method, path string }{
		{http.MethodPost, "/api/auth/setup"}, {http.MethodPost, "/api/auth/login"},
		{http.MethodPost, "/api/auth/logout"}, {http.MethodPost, "/api/auth/password"},
		{http.MethodPost, "/api/models"}, {http.MethodPost, "/api/models/grouped"},
		{http.MethodPost, "/api/models/m/files"}, {http.MethodPost, "/api/models/m/images"},
		{http.MethodDelete, "/api/models/m"}, {http.MethodPut, "/api/collections/c/models/m"},
		{http.MethodPost, "/api/backups"}, {http.MethodPost, "/api/backups/inspect"},
		{http.MethodPost, "/api/backups/restore"}, {http.MethodDelete, "/api/shares/s"},
	} {
		req := httptest.NewRequest(test.method, test.path, strings.NewReader("unread body"))
		req.Header.Set("Sec-Fetch-Site", "same-site")
		req.Header.Set("Content-Type", "multipart/form-data; boundary=unused")
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		app.Router().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: status=%d body=%s", test.method, test.path, rec.Code, rec.Body.String())
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("safe request rejected: %d", rec.Code)
	}
}

func TestJSONMutationRoutesRequireJSONMediaType(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	for _, test := range []struct{ method, path string }{
		{http.MethodPost, "/api/collections"}, {http.MethodPatch, "/api/collections/c"},
		{http.MethodPut, "/api/collections/c/order"}, {http.MethodPost, "/api/shares"},
		{http.MethodPatch, "/api/models/m"}, {http.MethodPatch, "/api/models/m/files/f"},
		{http.MethodPut, "/api/models/m/thumb"}, {http.MethodPost, "/api/backups/restore"},
	} {
		for _, contentType := range []string{"text/plain", "application/x-www-form-urlencoded", "multipart/form-data; boundary=x", ""} {
			req := jsonReq(test.method, test.path, `{"name":"Unwanted collection"}`)
			req.Header.Set("Content-Type", contentType)
			req.AddCookie(cookie)
			rec := httptest.NewRecorder()
			app.Router().ServeHTTP(rec, req)
			if rec.Code != http.StatusUnsupportedMediaType {
				t.Errorf("%s %s %q: status=%d body=%s", test.method, test.path, contentType, rec.Code, rec.Body.String())
			}
		}
	}
	req := jsonReq(http.MethodPost, "/api/collections", `{"name":"JSON with charset"}`)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("valid JSON media type rejected: %d %s", rec.Code, rec.Body.String())
	}
}
