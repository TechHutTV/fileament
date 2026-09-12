package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNewOwnerPasswordsUseOnePolicy(t *testing.T) {
	for _, password := range []string{"x", "12345678901", strings.Repeat("界", 11), strings.Repeat("x", 1025), "123456789012", strings.Repeat("界", 12), strings.Repeat("x", 1024)} {
		valid := len([]rune(password)) >= 12 && len(password) <= 1024
		for _, entry := range []string{"seed", "setup", "change"} {
			t.Run(entry+"/bytes="+strconvInt(int64(len(password))), func(t *testing.T) {
				app := newTestApp(t)
				if entry == "seed" {
					app.cfg.OwnerPassword = password
					err := app.seedOwnerPassword()
					if (err == nil) != valid {
						t.Fatalf("seed accepted=%t want=%t", err == nil, valid)
					}
					if err != nil && strings.Contains(err.Error(), password) {
						t.Fatal("seed error contains the password")
					}
					return
				}
				payload := map[string]string{"password": password}
				path, status := "/api/auth/setup", http.StatusCreated
				var cookie *http.Cookie
				if entry == "change" {
					app = newAuthedTestApp(t)
					cookie = loginCookie(t, app, "password-password")
					payload = map[string]string{"currentPassword": "password-password", "newPassword": password}
					path, status = "/api/auth/password", http.StatusNoContent
				}
				if !valid {
					status = http.StatusBadRequest
				}
				body, _ := json.Marshal(payload)
				req := jsonReq(http.MethodPost, path, string(body))
				if cookie != nil {
					req.AddCookie(cookie)
				}
				rec := httptest.NewRecorder()
				app.Router().ServeHTTP(rec, req)
				if rec.Code != status {
					t.Fatalf("status=%d want=%d", rec.Code, status)
				}
			})
		}
	}
}

func TestExistingOwnerIgnoresInvalidSeed(t *testing.T) {
	app := newAuthedTestApp(t)
	app.cfg.OwnerPassword = "x"
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := New(app.cfg, app.webFS)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loginCookie(t, reopened, "password-password")
}

func TestStartupRejectsInvalidOwnerSeed(t *testing.T) {
	app := newTestApp(t)
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	app.cfg.OwnerPassword = "x"
	reopened, err := New(app.cfg, app.webFS)
	if err == nil {
		_ = reopened.Close()
		t.Fatal("startup accepted a one-character owner password")
	}
}
