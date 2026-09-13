package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/TechHutTV/fileament/internal/config"
	"github.com/TechHutTV/fileament/internal/storage"
	"github.com/go-chi/chi/v5"
	_ "modernc.org/sqlite"
)

type App struct {
	cfg                 config.Config
	db                  *sql.DB
	dataRoot            *os.Root
	webFS               fs.FS
	dataMu              sync.RWMutex
	modelPersistMu      sync.Mutex
	mutationMu          sync.Mutex
	mutationFault       func(string) error
	collectionPersistMu sync.Mutex
	restoreMu           sync.Mutex
	backupMu            sync.Mutex
	backupCtx           context.Context
	backupCancel        context.CancelFunc
	backupTimer         *time.Timer
	preparedBackup      *preparedBackup
	backupFault         func(string) error
	maintenance         atomic.Bool
	mutationRecovery    atomic.Bool
	stop                chan struct{}
	thumbWake           chan struct{}
	workerCancel        context.CancelFunc
	workerWG            sync.WaitGroup
	thumbMu             sync.Mutex
	eventsMu            sync.Mutex
	events              map[chan ThumbnailEvent]struct{}
	eventsReset         chan struct{}
	authLimits          authenticationLimiter
	lastSessionCleanup  atomic.Int64
}

func New(cfg config.Config, webFS fs.FS) (*App, error) {
	cfg = cfg.NormalizeLimits()
	if webFS == nil {
		return nil, fmt.Errorf("web filesystem is required")
	}
	if _, err := fs.Stat(webFS, "index.html"); err != nil {
		return nil, fmt.Errorf("web filesystem does not contain index.html: %w", err)
	}
	if err := os.MkdirAll(cfg.DataDir, 0o755); err != nil {
		return nil, err
	}
	dataRoot, err := os.OpenRoot(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	keepRoot := false
	defer func() {
		if !keepRoot {
			_ = dataRoot.Close()
		}
	}()
	if err := storage.ValidateLayout(dataRoot); err != nil {
		return nil, err
	}
	if err := recoverInterruptedRestore(cfg.DataDir); err != nil {
		return nil, err
	}
	if err := cleanupRestoreWorkspace(cfg.DataDir); err != nil {
		return nil, err
	}
	if err := storage.EnsureLayout(dataRoot); err != nil {
		return nil, err
	}
	db, err := openDatabase(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	app := &App{cfg: cfg, db: db, dataRoot: dataRoot, webFS: webFS, stop: make(chan struct{}), thumbWake: make(chan struct{}, 1), events: map[chan ThumbnailEvent]struct{}{}, eventsReset: make(chan struct{})}
	if err := app.initializeData(); err != nil {
		_ = db.Close()
		return nil, err
	}
	app.backupCtx, app.backupCancel = context.WithCancel(context.Background())
	app.startWorkers()
	keepRoot = true
	return app, nil
}

func (a *App) Close() error {
	a.resetEventStreams()
	a.backupCancel()
	a.backupMu.Lock()
	backupErr := a.clearPreparedBackup()
	a.backupMu.Unlock()
	a.stopWorkers()
	var rootErr error
	if a.dataRoot != nil {
		rootErr = a.dataRoot.Close()
	}
	if a.db != nil {
		return errors.Join(backupErr, rootErr, a.db.Close())
	}
	return errors.Join(backupErr, rootErr)
}

func (a *App) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(browserSecurityHeaders)
	r.Use(sensitiveCachePolicy)
	r.Use(protectBrowserOrigin)
	r.Use(a.maintenanceMiddleware)
	r.Use(a.authenticationLimitsMiddleware)
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
	r.Head("/", a.serveSPA)
	r.Head("/index.html", a.serveSPA)
	r.Head("/assets/*", a.serveSPA)
	return r
}

func openSQLite(path string, query url.Values) (*sql.DB, error) {
	dbPath, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	u := url.URL{Scheme: "file", Path: filepath.ToSlash(dbPath)}
	u.RawQuery = query.Encode()
	return sql.Open("sqlite", u.String())
}

func openDatabase(dataDir string) (*sql.DB, error) {
	db, err := openSQLite(filepath.Join(dataDir, "fileament.db"), url.Values{
		"_pragma": {"foreign_keys(ON)", "busy_timeout(5000)", "synchronous(FULL)"},
		"_txlock": {"immediate"},
	})
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetMaxIdleConns(4)
	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, err
	}
	var mode string
	if err := db.QueryRow(`PRAGMA journal_mode=WAL`).Scan(&mode); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("enable SQLite WAL: %w", err)
	}
	if mode != "wal" {
		_ = db.Close()
		return nil, errors.New("SQLite WAL mode is unavailable")
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return db, nil
}

func (a *App) initializeData() error {
	if err := a.recoverMutations(); err != nil {
		return err
	}
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
	if err := a.recoverThumbnailJobs(); err != nil {
		return err
	}
	return a.pruneExpiredSessions(context.Background(), time.Now())
}

func (a *App) maintenanceMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.maintenance.Load() && r.URL.Path != "/healthz" {
			writeError(w, http.StatusServiceUnavailable, errors.New("Fileament is recovering stored data"))
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
		if a.maintenance.Load() {
			writeError(w, http.StatusServiceUnavailable, errMutationRecoveryRequired)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *App) handleStorageStats(w http.ResponseWriter, r *http.Request) {
	var usage storageUsage
	if err := a.db.QueryRowContext(r.Context(), `SELECT COALESCE(SUM(total_bytes), 0) FROM models`).Scan(&usage.TotalBytes); err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("storage usage is unavailable"))
		return
	}
	if err := usage.measure(r.Context(), a.cfg.DataDir); err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("storage usage could not be measured"))
		return
	}
	writeJSON(w, http.StatusOK, usage)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func ensureDir(path string) error {
	return os.MkdirAll(path, 0o755)
}
