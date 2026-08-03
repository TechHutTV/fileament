package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/TechHutTV/fileament/internal/config"
	"github.com/TechHutTV/fileament/internal/storage"
	"github.com/go-chi/chi/v5"
	_ "modernc.org/sqlite"
)

type App struct {
	cfg         config.Config
	db          *sql.DB
	webFS       fs.FS
	dataMu      sync.RWMutex
	maintenance atomic.Bool
	stop        chan struct{}
	workerWG    sync.WaitGroup
	thumbMu     sync.Mutex
	eventsMu    sync.Mutex
	events      map[chan ThumbnailEvent]struct{}
	eventsReset chan struct{}
}

func New(cfg config.Config, webFS fs.FS) (*App, error) {
	if webFS == nil {
		return nil, fmt.Errorf("web filesystem is required")
	}
	if _, err := fs.Stat(webFS, "index.html"); err != nil {
		return nil, fmt.Errorf("web filesystem does not contain index.html: %w", err)
	}
	if err := recoverInterruptedRestore(cfg.DataDir); err != nil {
		return nil, err
	}
	if err := storage.EnsureLayout(cfg.DataDir); err != nil {
		return nil, err
	}
	db, err := openDatabase(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	app := &App{cfg: cfg, db: db, webFS: webFS, stop: make(chan struct{}), events: map[chan ThumbnailEvent]struct{}{}, eventsReset: make(chan struct{})}
	if err := app.initializeData(); err != nil {
		_ = db.Close()
		return nil, err
	}
	app.startWorkers()
	return app, nil
}

func (a *App) Close() error {
	a.stopWorkers()
	if a.db != nil {
		return a.db.Close()
	}
	return nil
}

func (a *App) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(a.maintenanceMiddleware)
	r.Use(a.dataAccessMiddleware)
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if a.maintenance.Load() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "maintenance"})
			return
		}
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
	a.mountBackupRoutes(r)
	r.Get("/*", a.serveSPA)
	return r
}

func openDatabase(dataDir string) (*sql.DB, error) {
	dbPath := filepath.ToSlash(filepath.Join(dataDir, "fileament.db"))
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
	return db, nil
}

func (a *App) initializeData() error {
	if err := a.seedOwnerPassword(); err != nil {
		return err
	}
	if err := a.rebuildFromSidecars(); err != nil {
		return err
	}
	if err := a.rebuildCollectionsFromSidecar(); err != nil {
		return err
	}
	if err := a.refreshThumbnailRenderVersion(); err != nil {
		return err
	}
	return a.recoverThumbnailJobs()
}

func (a *App) maintenanceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.maintenance.Load() && r.URL.Path != "/healthz" {
			writeError(w, http.StatusServiceUnavailable, errors.New("Fileament is applying a restore"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) dataAccessMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" || r.URL.Path == "/api/events" || strings.HasPrefix(r.URL.Path, "/api/backups") {
			next.ServeHTTP(w, r)
			return
		}
		a.dataMu.RLock()
		defer a.dataMu.RUnlock()
		next.ServeHTTP(w, r)
	})
}

func (a *App) handleStorageStats(w http.ResponseWriter, r *http.Request) {
	var total int64
	_ = a.db.QueryRow(`SELECT COALESCE(SUM(total_bytes), 0) FROM models`).Scan(&total)
	writeJSON(w, http.StatusOK, map[string]int64{"totalBytes": total})
}

func (a *App) serveSPA(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	if path == "" {
		path = "index.html"
	}
	if _, err := fs.Stat(a.webFS, path); err != nil {
		data, readErr := fs.ReadFile(a.webFS, "index.html")
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
	http.FileServer(http.FS(a.webFS)).ServeHTTP(w, r)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func ensureDir(path string) error {
	return os.MkdirAll(path, 0o755)
}
