package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/TechHutTV/fileament/internal/config"
)

func TestHealthzAndMigrations(t *testing.T) {
	app := newTestApp(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz status = %d", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(app.cfg.DataDir, "fileament.db")); err != nil {
		t.Fatalf("db was not created: %v", err)
	}
	for _, table := range []string{"models", "files", "collections", "share_links", "sessions", "jobs", "settings"} {
		var name string
		err := app.db.QueryRow(`SELECT name FROM sqlite_master WHERE type IN ('table','virtual table') AND name = ?`, table).Scan(&name)
		if err != nil {
			t.Fatalf("missing table %s: %v", table, err)
		}
	}
}

func TestNewRejectsMissingWebFilesystem(t *testing.T) {
	app, err := New(config.Config{DataDir: t.TempDir(), Port: "0", MaxUploadMB: 32, ThumbWorkers: 0}, nil)
	if err == nil {
		_ = app.Close()
		t.Fatal("expected missing web filesystem to be rejected")
	}
}

func TestSPAFallback(t *testing.T) {
	app := newTestApp(t)
	req := httptest.NewRequest(http.MethodGet, "/models/abc", nil)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("fallback status = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got == "" {
		t.Fatal("expected content type")
	}
	if !strings.Contains(rec.Body.String(), "Fileament test UI") {
		t.Fatalf("fallback body = %q", rec.Body.String())
	}
}

func newTestApp(t *testing.T) *App {
	t.Helper()
	return newTestAppWithPassword(t, "")
}

func newTestAppWithPassword(t *testing.T, password string) *App {
	t.Helper()
	return newTestAppWithConfig(t, config.Config{DataDir: t.TempDir(), Port: "0", OwnerPassword: password, MaxUploadMB: 32, ThumbWorkers: 0})
}

func newTestAppWithConfig(t *testing.T, cfg config.Config) *App {
	t.Helper()
	if cfg.Port == "" {
		cfg.Port = "0"
	}
	if cfg.WebDir == "" {
		cfg.WebDir = testWebDir(t)
	}
	app, err := New(cfg, os.DirFS(cfg.WebDir))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = app.Close() })
	return app
}

func testWebDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><title>Fileament test UI</title>"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}
