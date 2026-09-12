package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBrowserHeadersCoverOwnerAndSharePages(t *testing.T) {
	app := newTestApp(t)
	for _, path := range []string{"/", "/login", "/models/example", "/s/example", "/api/me", "/api/public/missing"} {
		rec := httptest.NewRecorder()
		app.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		for name, want := range map[string]string{
			"X-Frame-Options":        "DENY",
			"Referrer-Policy":        "no-referrer",
			"X-Content-Type-Options": "nosniff",
		} {
			if got := rec.Header().Get(name); got != want {
				t.Errorf("%s %s=%q want=%q", path, name, got, want)
			}
		}
		for _, directive := range []string{"frame-ancestors 'none'", "object-src 'none'", "base-uri 'self'", "form-action 'self'"} {
			if !strings.Contains(rec.Header().Get("Content-Security-Policy"), directive) {
				t.Errorf("%s missing CSP directive %s", path, directive)
			}
		}
		if strings.HasPrefix(path, "/s/") && rec.Header().Get("X-Robots-Tag") != "noindex" {
			t.Error("share page lost noindex")
		}
	}
}
