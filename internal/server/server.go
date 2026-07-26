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
	"sync"

	"github.com/TechHutTV/fileament/internal/config"
	"github.com/TechHutTV/fileament/internal/storage"
	"github.com/go-chi/chi/v5"
	_ "modernc.org/sqlite"
)

//go:embed dist
var webFS embed.FS

type App struct {
	cfg      config.Config
	db       *sql.DB
	stop     chan struct{}
	workerWG sync.WaitGroup
	eventsMu sync.Mutex
	events   map[chan ThumbnailEvent]struct{}
}

func New(cfg config.Config) (*App, error) {
	if err := storage.EnsureLayout(cfg.DataDir); err != nil {
		return nil, err
	}
	dbPath := filepath.ToSlash(filepath.Join(cfg.DataDir, "fileament.db"))
	db, err := sql.Open("sqlite", "file:"+dbPath+"?_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	app := &App{cfg: cfg, db: db, stop: make(chan struct{}), events: map[chan ThumbnailEvent]struct{}{}}
	if err := app.seedOwnerPassword(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := app.rebuildFromSidecars(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := app.rebuildCollectionsFromSidecar(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := app.recoverThumbnailJobs(); err != nil {
		_ = db.Close()
		return nil, err
	}
	app.startWorkers()
	return app, nil
}

func (a *App) Close() error {
	if a.stop != nil {
		close(a.stop)
		a.workerWG.Wait()
		a.stop = nil
	}
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
	r.With(a.requireAuth).Post("/api/auth/password", a.handleChangePassword)
	r.With(a.requireAuth).Get("/api/storage", a.handleStorageStats)
	a.mountModelRoutes(r)
	a.mountCollectionRoutes(r)
	a.mountThumbRoutes(r)
	r.Get("/*", a.serveSPA)
	return r
}

func (a *App) handleStorageStats(w http.ResponseWriter, r *http.Request) {
	var total int64
	_ = a.db.QueryRow(`SELECT COALESCE(SUM(total_bytes), 0) FROM models`).Scan(&total)
	writeJSON(w, http.StatusOK, map[string]int64{"totalBytes": total})
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
		if strings.HasPrefix(r.URL.Path, "/s/") {
			w.Header().Set("X-Robots-Tag", "noindex")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/s/") {
		w.Header().Set("X-Robots-Tag", "noindex")
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
