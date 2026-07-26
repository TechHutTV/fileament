package server

import (
	"database/sql"
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/brandon/fileament/internal/config"
	"github.com/brandon/fileament/internal/storage"
	"github.com/go-chi/chi/v5"
	_ "modernc.org/sqlite"
)

//go:embed dist
var webFS embed.FS

type App struct {
	cfg config.Config
	db  *sql.DB
}

func New(cfg config.Config) (*App, error) {
	if err := storage.EnsureLayout(cfg.DataDir); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", filepath.Join(cfg.DataDir, "fileament.db"))
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`PRAGMA foreign_keys = ON;`); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	app := &App{cfg: cfg, db: db}
	if err := app.seedOwnerPassword(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return app, nil
}

func (a *App) Close() error {
	if a.db != nil {
		return a.db.Close()
	}
	return nil
}

func (a *App) Router() http.Handler {
	r := chi.NewRouter()
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	r.Get("/api/me", a.handleMe)
	r.Post("/api/auth/setup", a.handleSetup)
	r.Post("/api/auth/login", a.handleLogin)
	r.Post("/api/auth/logout", a.handleLogout)
	r.Get("/*", a.serveSPA)
	return r
}

func (a *App) serveSPA(w http.ResponseWriter, r *http.Request) {
	sub, _ := fs.Sub(webFS, "dist")
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	if _, err := fs.Stat(sub, path); err != nil {
		data, readErr := fs.ReadFile(sub, "index.html")
		if readErr != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
		return
	}
	http.FileServer(http.FS(sub)).ServeHTTP(w, r)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func ensureDir(path string) error {
	return os.MkdirAll(path, 0o755)
}
