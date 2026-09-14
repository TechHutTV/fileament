package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/TechHutTV/fileament/internal/ids"
	"github.com/TechHutTV/fileament/internal/mesh"
	"github.com/TechHutTV/fileament/internal/render"
	"github.com/go-chi/chi/v5"
)

const (
	thumbnailRenderVersionKey = "thumbnail_render_version"
	thumbnailRenderVersion    = "3"
	primaryThumbSourceName    = ".primary-thumb-source"
)

type ThumbnailEvent struct {
	ModelID   string `json:"modelId"`
	FileID    string `json:"fileId"`
	ThumbPath string `json:"thumbPath"`
}

var thumbnailSlots = make(chan struct{}, 2)

func (a *App) startWorkers() {
	if a.cfg.ThumbWorkers <= 0 {
		return
	}
	if a.stop == nil {
		a.stop = make(chan struct{})
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.workerCancel = cancel
	for i := 0; i < a.cfg.ThumbWorkers; i++ {
		a.workerWG.Add(1)
		go func() {
			defer a.workerWG.Done()
			tick := time.NewTicker(2 * time.Second)
			defer tick.Stop()
			for {
				select {
				case <-a.stop:
					return
				case <-tick.C:
					_ = a.processNextThumbnailContext(ctx)
				}
			}
		}()
	}
}

func (a *App) stopWorkers() {
	if a.stop == nil {
		return
	}
	close(a.stop)
	if a.workerCancel != nil {
		a.workerCancel()
		a.workerCancel = nil
	}
	a.workerWG.Wait()
	a.stop = nil
}

func (a *App) refreshThumbnailRenderVersion() error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current string
	err = tx.QueryRow(`SELECT value FROM settings WHERE key = ?`, thumbnailRenderVersionKey).Scan(&current)
	if err == nil && current == thumbnailRenderVersion {
		return nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	rows, err := tx.Query(`SELECT id FROM files ORDER BY id`)
	if err != nil {
		return err
	}
	var fileIDs []string
	for rows.Next() {
		var fileID string
		if err := rows.Scan(&fileID); err != nil {
			_ = rows.Close()
			return err
		}
		fileIDs = append(fileIDs, fileID)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM jobs WHERE type = 'thumbnail'`); err != nil {
		return err
	}
	now := time.Now().Unix()
	for _, fileID := range fileIDs {
		if _, err := tx.Exec(`INSERT INTO jobs(id,type,file_id,status,created_at) VALUES(?, 'thumbnail', ?, 'pending', ?)`, ids.New(), fileID, now); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO settings(key, value) VALUES(?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, thumbnailRenderVersionKey, thumbnailRenderVersion); err != nil {
		return err
	}
	return tx.Commit()
}

func (a *App) mountThumbRoutes(r chi.Router) {
	r.With(a.requireDataAuth).Get("/api/events", a.handleEvents)
	r.With(a.requireAuth).Get("/thumbs/{modelID}/{name}", a.handleThumb)
}

func (a *App) processNextThumbnail() error {
	return a.processNextThumbnailContext(context.Background())
}

func (a *App) processNextThumbnailContext(ctx context.Context) (err error) {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case thumbnailSlots <- struct{}{}:
		defer func() { <-thumbnailSlots }()
	case <-ctx.Done():
		return ctx.Err()
	}
	a.dataMu.RLock()
	locked := true
	unlock := func() {
		if locked {
			locked = false
			a.dataMu.RUnlock()
		}
	}
	relock := func() {
		if !locked {
			a.dataMu.RLock()
			locked = true
		}
	}
	defer unlock()
	jobID, fileID, err := a.claimThumbnailJob(ctx)
	if err != nil || jobID == "" {
		return err
	}
	claimed := true
	defer func() {
		if claimed && err != nil {
			relock()
			status := "failed"
			if errors.Is(err, context.Canceled) || errors.Is(err, errMutationRecoveryRequired) {
				status = "pending"
			}
			err = errors.Join(err, a.finishThumbnailJob(jobID, status, err.Error()))
		}
	}()
	var modelID, relPath string
	if err := a.db.QueryRow(`SELECT model_id, rel_path FROM files WHERE id = ?`, fileID).Scan(&modelID, &relPath); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		return err
	}
	modelRoot, err := modelRootPath(a.cfg.DataDir, modelID)
	if err != nil {
		return err
	}
	if !validStorageID(fileID) || !validAssetPath(relPath, "files") {
		return errInvalidPath
	}
	meshPath, err := containedPath(modelRoot, relPath)
	if err != nil {
		return err
	}
	// Parsing and rendering only read the mesh and write under tmp/, so release the data lock
	// while they run; otherwise a backup waiting for the write lock stalls every request behind it.
	unlock()
	_, tris, err := mesh.ParseFileContext(ctx, meshPath)
	if err != nil {
		return err
	}
	prepared, err := os.CreateTemp(filepath.Join(a.cfg.DataDir, "tmp"), "thumbnail-*.png")
	if err != nil {
		return err
	}
	defer os.Remove(prepared.Name())
	if err := prepared.Close(); err != nil {
		return err
	}
	if err := render.RenderPNGContext(ctx, tris, prepared.Name(), 512); err != nil {
		return err
	}
	relock()
	published, err := a.publishThumbnail(ctx, jobID, fileID, modelID, relPath, prepared.Name())
	if err != nil {
		return err
	}
	claimed = false
	if published {
		a.publishEvent(ThumbnailEvent{ModelID: modelID, FileID: fileID, ThumbPath: filepath.ToSlash(filepath.Join("thumbs", fileID+".png"))})
	}
	return nil
}

func (a *App) publishThumbnail(ctx context.Context, jobID, fileID, modelID, relPath, prepared string) (published bool, err error) {
	if !validStorageID(fileID) || !validAssetPath(relPath, "files") {
		return false, errInvalidPath
	}
	modelRoot, err := modelRootPath(a.cfg.DataDir, modelID)
	if err != nil {
		return false, err
	}
	thumbDir := filepath.Join(modelRoot, "thumbs")
	thumbPath, err := containedName(thumbDir, fileID+".png")
	if err != nil {
		return false, err
	}
	legacyFilePath, err := containedName(thumbDir, fileID+".jpg")
	if err != nil {
		return false, err
	}
	a.modelPersistMu.Lock()
	defer a.modelPersistMu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	var exists int
	if err := a.db.QueryRow(`SELECT COUNT(*) FROM files WHERE id = ? AND model_id = ? AND rel_path = ?`, fileID, modelID, relPath).Scan(&exists); err != nil {
		return false, err
	}
	if exists == 0 {
		_, err := a.db.Exec(`DELETE FROM jobs WHERE id = ?`, jobID)
		return false, err
	}
	mutation, err := a.beginMutation(modelID)
	if err != nil {
		return false, err
	}
	defer mutation.finishOnReturn(&err)
	thumbRel := filepath.ToSlash(filepath.Join("thumbs", fileID+".png"))
	if err := os.MkdirAll(filepath.Dir(thumbPath), 0o755); err != nil {
		return false, err
	}
	if err := os.Rename(prepared, thumbPath); err != nil {
		return false, err
	}
	updateRes, err := a.db.Exec(`UPDATE files SET thumb_path = ? WHERE id = ?`, thumbRel, fileID)
	if err != nil {
		return false, err
	}
	updated, err := updateRes.RowsAffected()
	if err != nil {
		return false, err
	}
	if updated != 1 {
		_ = os.Remove(thumbPath)
		_, _ = a.db.Exec(`DELETE FROM jobs WHERE id = ?`, jobID)
		return false, nil
	}
	a.thumbMu.Lock()
	defer a.thumbMu.Unlock()
	var primary, largestFileID string
	if err := a.db.QueryRow(`SELECT COALESCE(primary_thumb, '') FROM models WHERE id = ?`, modelID).Scan(&primary); err != nil {
		return false, err
	}
	if err := a.db.QueryRow(`SELECT id FROM files WHERE model_id = ? ORDER BY size_bytes DESC, sort_order, id LIMIT 1`, modelID).Scan(&largestFileID); err != nil {
		return false, err
	}
	legacyCardPath := filepath.Join(thumbDir, "card.jpg")
	migratesLegacyPrimary := primary == "card.jpg" && filesHaveEqualContents(legacyFilePath, legacyCardPath)
	if (fileID == largestFileID && primary == "") || migratesLegacyPrimary {
		cardPath := filepath.Join(thumbDir, "card.png")
		if err := copyFile(cardPath, thumbPath); err != nil {
			return false, err
		}
		if err := writePrimaryThumbSource(modelRoot, fileID); err != nil {
			return false, err
		}
		if _, err := a.db.Exec(`UPDATE models SET primary_thumb = 'card.png' WHERE id = ?`, modelID); err != nil {
			return false, err
		}
	}
	m, err := a.getModel(modelID)
	if err != nil {
		return false, err
	}
	if err := a.writeSidecar(m); err != nil {
		return false, err
	}
	if err := a.finishThumbnailJob(jobID, "done", ""); err != nil {
		return false, err
	}
	if err := a.removeStorageFile(legacyFilePath); err != nil {
		return false, err
	}
	if migratesLegacyPrimary || primary == "card.png" {
		if err := a.removeStorageFile(legacyCardPath); err != nil {
			return false, err
		}
	}
	return true, nil
}

func (a *App) claimThumbnailJob(ctx context.Context) (string, string, error) {
	a.modelPersistMu.Lock()
	defer a.modelPersistMu.Unlock()
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	if a.maintenance.Load() {
		return "", "", nil
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return "", "", err
	}
	defer tx.Rollback()
	var jobID, fileID string
	err = tx.QueryRow(`SELECT id, file_id FROM jobs WHERE type = 'thumbnail' AND status = 'pending' ORDER BY created_at LIMIT 1`).Scan(&jobID, &fileID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	res, err := tx.Exec(`UPDATE jobs SET status = 'running', attempts = attempts + 1 WHERE id = ? AND status = 'pending'`, jobID)
	if err != nil {
		return "", "", err
	}
	changed, err := res.RowsAffected()
	if err != nil {
		return "", "", err
	}
	if changed != 1 {
		return "", "", nil
	}
	if err := tx.Commit(); err != nil {
		return "", "", err
	}
	return jobID, fileID, nil
}

func (a *App) finishThumbnailJob(jobID, status, diagnostic string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var finished any
	if status == "done" || status == "failed" {
		finished = time.Now().Unix()
	}
	if _, err := a.db.ExecContext(ctx, `UPDATE jobs SET status = ?, error = NULLIF(?, ''), finished_at = ? WHERE id = ?`, status, diagnostic, finished, jobID); err != nil {
		return err
	}
	return a.pruneThumbnailJobs(ctx, time.Now())
}

func (a *App) pruneThumbnailJobs(ctx context.Context, now time.Time) error {
	_, err := a.db.ExecContext(ctx, `DELETE FROM jobs WHERE status IN ('done','failed') AND
 (finished_at < ? OR id IN (SELECT id FROM jobs WHERE status IN ('done','failed') ORDER BY finished_at DESC, id DESC LIMIT -1 OFFSET 1000))`, now.Add(-7*24*time.Hour).Unix())
	return err
}

func filesHaveEqualContents(left, right string) bool {
	leftData, err := os.ReadFile(left)
	if err != nil {
		return false
	}
	rightData, err := os.ReadFile(right)
	return err == nil && bytes.Equal(leftData, rightData)
}

func readPrimaryThumbSource(modelRoot string) (string, error) {
	b, err := os.ReadFile(filepath.Join(modelRoot, "thumbs", primaryThumbSourceName))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(b)), nil
}

func writePrimaryThumbSource(modelRoot, fileID string) error {
	path := filepath.Join(modelRoot, "thumbs", primaryThumbSourceName)
	return atomicWriteFile(path, []byte(fileID+"\n"), 0o644)
}

func copyFile(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.CreateTemp(filepath.Dir(dst), ".copy-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(out.Name())
	err = out.Chmod(0o644)
	if err == nil {
		_, err = io.Copy(out, in)
	}
	if err == nil {
		err = out.Sync()
	}
	if err := errors.Join(err, out.Close()); err != nil {
		return err
	}
	if err := os.Rename(out.Name(), dst); err != nil {
		return err
	}
	return syncDirectory(filepath.Dir(dst))
}

func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	ch, reset := a.subscribeEventStream()
	defer a.unsubscribeEvents(ch)
	if !a.eventSessionValid(r) {
		writeError(w, http.StatusUnauthorized, errSessionStateChanged)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "private, no-store")
	flusher, _ := w.(http.Flusher)
	revalidate := time.NewTicker(time.Minute)
	defer revalidate.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-reset:
			return
		case <-revalidate.C:
			if !a.eventSessionValid(r) {
				return
			}
		case evt := <-ch:
			if !a.eventSessionValid(r) {
				return
			}
			b, _ := json.Marshal(evt)
			_, _ = fmt.Fprintf(w, "event: thumbnail\ndata: %s\n\n", b)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
}

func (a *App) eventSessionValid(r *http.Request) bool {
	a.dataMu.RLock()
	defer a.dataMu.RUnlock()
	return !a.maintenance.Load() && a.validSession(r)
}

func (a *App) subscribeEvents() chan ThumbnailEvent {
	ch, _ := a.subscribeEventStream()
	return ch
}

func (a *App) subscribeEventStream() (chan ThumbnailEvent, <-chan struct{}) {
	ch := make(chan ThumbnailEvent, 8)
	a.eventsMu.Lock()
	a.events[ch] = struct{}{}
	reset := a.eventsReset
	a.eventsMu.Unlock()
	return ch, reset
}

func (a *App) resetEventStreams() {
	a.eventsMu.Lock()
	close(a.eventsReset)
	a.eventsReset = make(chan struct{})
	a.eventsMu.Unlock()
}

func (a *App) unsubscribeEvents(ch chan ThumbnailEvent) {
	a.eventsMu.Lock()
	delete(a.events, ch)
	close(ch)
	a.eventsMu.Unlock()
}

func (a *App) publishEvent(evt ThumbnailEvent) {
	a.eventsMu.Lock()
	defer a.eventsMu.Unlock()
	for ch := range a.events {
		select {
		case ch <- evt:
		default:
		}
	}
}

func (a *App) handleThumb(w http.ResponseWriter, r *http.Request) {
	modelID := chi.URLParam(r, "modelID")
	name := chi.URLParam(r, "name")
	if !a.thumbAllowed(modelID, name) {
		http.NotFound(w, r)
		return
	}
	path, err := containedName(filepath.Join(a.cfg.DataDir, "models", modelID, "thumbs"), name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	http.ServeFile(w, r, path)
}

func (a *App) thumbAllowed(modelID, name string) bool {
	if name == "" || name != filepath.Base(name) {
		return false
	}
	var n int
	_ = a.db.QueryRow(`SELECT COUNT(*) FROM models WHERE id = ? AND primary_thumb = ?`, modelID, name).Scan(&n)
	if n > 0 {
		return true
	}
	_ = a.db.QueryRow(`SELECT COUNT(*) FROM files WHERE model_id = ? AND thumb_path = ?`, modelID, filepath.ToSlash(filepath.Join("thumbs", name))).Scan(&n)
	return n > 0
}

var _ = sync.Mutex{}
