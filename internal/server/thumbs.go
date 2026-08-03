package server

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
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
)

type ThumbnailEvent struct {
	ModelID   string `json:"modelId"`
	FileID    string `json:"fileId"`
	ThumbPath string `json:"thumbPath"`
}

func (a *App) startWorkers() {
	if a.cfg.ThumbWorkers <= 0 {
		return
	}
	if a.stop == nil {
		a.stop = make(chan struct{})
	}
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
					_ = a.processNextThumbnail()
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

func (a *App) processNextThumbnail() (err error) {
	a.dataMu.RLock()
	defer a.dataMu.RUnlock()
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	var jobID, fileID string
	err = tx.QueryRow(`SELECT id, file_id FROM jobs WHERE type = 'thumbnail' AND status = 'pending' ORDER BY created_at LIMIT 1`).Scan(&jobID, &fileID)
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return nil
	}
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	res, err := tx.Exec(`UPDATE jobs SET status = 'running', attempts = attempts + 1 WHERE id = ? AND status = 'pending'`, jobID)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	changed, _ := res.RowsAffected()
	if changed != 1 {
		_ = tx.Rollback()
		return nil
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	claimed := true
	defer func() {
		if claimed && err != nil {
			_, _ = a.db.Exec(`UPDATE jobs SET status = 'failed', error = ? WHERE id = ?`, err.Error(), jobID)
		}
	}()
	var modelID, relPath string
	if err := a.db.QueryRow(`SELECT model_id, rel_path FROM files WHERE id = ?`, fileID).Scan(&modelID, &relPath); err != nil {
		return err
	}
	meshPath := filepath.Join(a.cfg.DataDir, "models", modelID, relPath)
	if safe, pathErr := containedPath(filepath.Join(a.cfg.DataDir, "models", modelID), relPath); pathErr != nil {
		err = pathErr
		return err
	} else {
		meshPath = safe
	}
	_, tris, err := mesh.ParseFile(meshPath)
	if err != nil {
		return err
	}
	thumbRel := filepath.ToSlash(filepath.Join("thumbs", fileID+".png"))
	thumbPath := filepath.Join(a.cfg.DataDir, "models", modelID, thumbRel)
	if err := os.MkdirAll(filepath.Dir(thumbPath), 0o755); err != nil {
		return err
	}
	if err := render.RenderPNG(tris, thumbPath, 512); err != nil {
		return err
	}
	if _, err := a.db.Exec(`UPDATE files SET thumb_path = ? WHERE id = ?`, thumbRel, fileID); err != nil {
		return err
	}
	a.thumbMu.Lock()
	defer a.thumbMu.Unlock()
	var primary, largestFileID string
	if err := a.db.QueryRow(`SELECT COALESCE(primary_thumb, '') FROM models WHERE id = ?`, modelID).Scan(&primary); err != nil {
		return err
	}
	if err := a.db.QueryRow(`SELECT id FROM files WHERE model_id = ? ORDER BY size_bytes DESC, sort_order, id LIMIT 1`, modelID).Scan(&largestFileID); err != nil {
		return err
	}
	legacyFilePath := filepath.Join(a.cfg.DataDir, "models", modelID, "thumbs", fileID+".jpg")
	legacyCardPath := filepath.Join(a.cfg.DataDir, "models", modelID, "thumbs", "card.jpg")
	migratesLegacyPrimary := primary == "card.jpg" && filesHaveEqualContents(legacyFilePath, legacyCardPath)
	if (fileID == largestFileID && primary == "") || migratesLegacyPrimary {
		cardPath := filepath.Join(a.cfg.DataDir, "models", modelID, "thumbs", "card.png")
		if err := copyFile(cardPath, thumbPath); err != nil {
			return err
		}
		if _, err := a.db.Exec(`UPDATE models SET primary_thumb = 'card.png' WHERE id = ?`, modelID); err != nil {
			return err
		}
	}
	m, err := a.getModel(modelID)
	if err != nil {
		return err
	}
	if err := a.writeSidecar(m); err != nil {
		return err
	}
	if _, err := a.db.Exec(`UPDATE jobs SET status = 'done', error = NULL WHERE id = ?`, jobID); err != nil {
		return err
	}
	_ = os.Remove(legacyFilePath)
	if migratesLegacyPrimary || primary == "card.png" {
		_ = os.Remove(legacyCardPath)
	}
	claimed = false
	a.publishEvent(ThumbnailEvent{ModelID: modelID, FileID: fileID, ThumbPath: thumbRel})
	return nil
}

func filesHaveEqualContents(left, right string) bool {
	leftData, err := os.ReadFile(left)
	if err != nil {
		return false
	}
	rightData, err := os.ReadFile(right)
	return err == nil && bytes.Equal(leftData, rightData)
}

func copyFile(dst, src string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	ch, reset := a.subscribeEventStream()
	defer a.unsubscribeEvents(ch)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	for {
		select {
		case <-r.Context().Done():
			return
		case <-reset:
			return
		case evt := <-ch:
			b, _ := json.Marshal(evt)
			_, _ = fmt.Fprintf(w, "event: thumbnail\ndata: %s\n\n", b)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}
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
