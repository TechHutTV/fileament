package server

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

const (
	backupFormatVersion = 1
	dataFormatVersion   = 1
	backupDownloadTTL   = 15 * time.Minute
	backupExportTimeout = 30 * time.Minute
)

type backupManifest struct {
	BackupFormatVersion int    `json:"backupFormatVersion"`
	DataFormatVersion   int    `json:"dataFormatVersion"`
	DatabaseVersion     int    `json:"databaseVersion"`
	CreatedAt           string `json:"createdAt"`
	Models              int    `json:"models"`
	Files               int    `json:"files"`
	Collections         int    `json:"collections"`
}

type preparedBackup struct {
	DownloadURL string `json:"downloadUrl"`
	ExpiresAt   int64  `json:"expiresAt"`
	Filename    string `json:"filename"`
	SizeBytes   int64  `json:"sizeBytes"`
	path        string
	token       string
	session     [32]byte
}

func (a *App) mountBackupRoutes(r chi.Router) {
	r.With(a.requireDataAuth).Post("/api/backups", a.handleCreateBackup)
	r.With(a.requireDataAuth).Post("/api/backups/prepare", a.handlePrepareBackup)
	r.With(a.requireDataAuth).Get("/api/backups/download/{token}", a.handleDownloadBackup)
	r.With(a.requireDataAuth).Post("/api/backups/inspect", a.handleInspectBackup)
	r.With(a.requireDataAuth, requireJSON).Post("/api/backups/restore", a.handleApplyRestore)
}

func (a *App) handleCreateBackup(w http.ResponseWriter, r *http.Request) {
	a.exportBackup(w, r, false)
}

func (a *App) handlePrepareBackup(w http.ResponseWriter, r *http.Request) {
	a.exportBackup(w, r, true)
}

func (a *App) exportBackup(w http.ResponseWriter, r *http.Request, prepare bool) {
	if !a.backupMu.TryLock() {
		writeError(w, http.StatusConflict, errors.New("a backup export or download is already active"))
		return
	}
	defer a.backupMu.Unlock()
	ctx, cancel := context.WithTimeout(r.Context(), backupExportTimeout)
	defer cancel()
	stop := context.AfterFunc(a.backupCtx, cancel)
	defer stop()
	if err := a.clearPreparedBackup(); err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("previous backup could not be removed"))
		return
	}
	a.dataMu.Lock()
	if a.maintenance.Load() || a.backupCtx.Err() != nil {
		a.dataMu.Unlock()
		writeError(w, http.StatusServiceUnavailable, errMutationRecoveryRequired)
		return
	}
	if !a.validSession(r) {
		a.dataMu.Unlock()
		writeError(w, http.StatusUnauthorized, errors.New("authentication required"))
		return
	}
	snapshot, err := a.captureBackupSnapshot(ctx)
	a.dataMu.Unlock()
	if snapshot != nil {
		defer os.RemoveAll(snapshot.root)
	}
	if err == nil && a.backupFault != nil {
		err = a.backupFault("snapshot")
	}
	var path string
	if err == nil {
		path, err = a.archiveBackupSnapshot(ctx, snapshot, filepath.Join(a.cfg.DataDir, "tmp", "backups"))
	}
	if err != nil {
		status := http.StatusInternalServerError
		message := errors.New("backup could not be created; check available storage and retry")
		if errors.Is(err, errBackupTooLarge) {
			status, message = http.StatusRequestEntityTooLarge, errBackupTooLarge
		}
		writeError(w, status, message)
		return
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.Remove(path)
		}
	}()
	if err := os.RemoveAll(snapshot.root); err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("backup workspace could not be removed"))
		return
	}
	filename := "fileament-backup-" + strings.NewReplacer(":", "", "-", "").Replace(snapshot.manifest.CreatedAt) + ".fileament"
	if !prepare {
		a.serveBackupFile(w, r, path, filename)
		return
	}
	token, err := randomToken(24)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("backup download could not be prepared"))
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("backup download is unavailable"))
		return
	}
	cookie, _ := r.Cookie(sessionCookieName)
	ready := &preparedBackup{DownloadURL: "/api/backups/download/" + token, ExpiresAt: time.Now().Add(backupDownloadTTL).Unix(), Filename: filename, SizeBytes: info.Size(), path: path, token: token, session: sha256.Sum256([]byte(cookie.Value))}
	a.preparedBackup = ready
	a.backupTimer = time.AfterFunc(backupDownloadTTL, func() {
		a.backupMu.Lock()
		defer a.backupMu.Unlock()
		if a.preparedBackup == ready {
			_ = a.clearPreparedBackup()
		}
	})
	keep = true
	writeJSON(w, http.StatusCreated, ready)
}

func (a *App) handleDownloadBackup(w http.ResponseWriter, r *http.Request) {
	if !a.backupMu.TryLock() {
		writeError(w, http.StatusConflict, errors.New("a backup export or download is already active"))
		return
	}
	defer a.backupMu.Unlock()
	ready := a.preparedBackup
	cookie, _ := r.Cookie(sessionCookieName)
	if ready == nil || time.Now().Unix() >= ready.ExpiresAt || ready.token != chi.URLParam(r, "token") || ready.session != sha256.Sum256([]byte(cookie.Value)) {
		writeError(w, http.StatusNotFound, errors.New("backup download is missing or expired; create a new backup"))
		return
	}
	a.serveBackupFile(w, r, ready.path, ready.Filename)
}

// backupMu protects prepared archives through the entire download.
func (a *App) clearPreparedBackup() error {
	if a.backupTimer != nil {
		a.backupTimer.Stop()
	}
	if a.preparedBackup == nil {
		return nil
	}
	if err := os.Remove(a.preparedBackup.path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	a.preparedBackup = nil
	return nil
}

func (a *App) serveBackupFile(w http.ResponseWriter, r *http.Request, path, filename string) {
	file, err := os.Open(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("backup download is unavailable"))
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		writeError(w, http.StatusInternalServerError, errors.New("backup download is unavailable"))
		return
	}
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Now().Add(backupExportTimeout))
	interrupted := make(chan struct{})
	stop := context.AfterFunc(a.backupCtx, func() {
		_ = controller.SetWriteDeadline(time.Now())
		close(interrupted)
	})
	defer func() {
		if !stop() {
			<-interrupted
		}
		_ = controller.SetWriteDeadline(time.Time{})
	}()
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, filename))
	http.ServeContent(w, r, filename, info.ModTime(), file)
}
