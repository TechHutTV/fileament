package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/brandon/fileament/internal/mesh"
	"github.com/brandon/fileament/internal/render"
	"github.com/go-chi/chi/v5"
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

func (a *App) mountThumbRoutes(r chi.Router) {
	r.With(a.requireAuth).Get("/api/events", a.handleEvents)
	r.Get("/thumbs/{modelID}/{name}", a.handleThumb)
}

func (a *App) processNextThumbnail() error {
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
	if _, err := tx.Exec(`UPDATE jobs SET status = 'running', attempts = attempts + 1 WHERE id = ?`, jobID); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	var modelID, relPath string
	if err := a.db.QueryRow(`SELECT model_id, rel_path FROM files WHERE id = ?`, fileID).Scan(&modelID, &relPath); err != nil {
		return err
	}
	meshPath := filepath.Join(a.cfg.DataDir, "models", modelID, relPath)
	_, tris, err := mesh.ParseFile(meshPath)
	if err != nil {
		_, _ = a.db.Exec(`UPDATE jobs SET status = 'failed', error = ? WHERE id = ?`, err.Error(), jobID)
		return err
	}
	thumbRel := filepath.ToSlash(filepath.Join("thumbs", fileID+".jpg"))
	thumbPath := filepath.Join(a.cfg.DataDir, "models", modelID, thumbRel)
	if err := os.MkdirAll(filepath.Dir(thumbPath), 0o755); err != nil {
		return err
	}
	if err := render.RenderJPEG(tris, thumbPath, 512); err != nil {
		_, _ = a.db.Exec(`UPDATE jobs SET status = 'failed', error = ? WHERE id = ?`, err.Error(), jobID)
		return err
	}
	cardPath := filepath.Join(a.cfg.DataDir, "models", modelID, "thumbs", "card.jpg")
	primary := "card.jpg"
	var existing sql.NullString
	_ = a.db.QueryRow(`SELECT primary_thumb FROM models WHERE id = ?`, modelID).Scan(&existing)
	if existing.Valid && existing.String != "" {
		primary = existing.String
	} else if err := copyFile(cardPath, thumbPath); err != nil {
		return err
	}
	if _, err := a.db.Exec(`UPDATE files SET thumb_path = ? WHERE id = ?`, thumbRel, fileID); err != nil {
		return err
	}
	if _, err := a.db.Exec(`UPDATE models SET primary_thumb = ? WHERE id = ? AND (primary_thumb IS NULL OR primary_thumb = '')`, primary, modelID); err != nil {
		return err
	}
	if _, err := a.db.Exec(`UPDATE jobs SET status = 'done', error = NULL WHERE id = ?`, jobID); err != nil {
		return err
	}
	a.publishEvent(ThumbnailEvent{ModelID: modelID, FileID: fileID, ThumbPath: thumbRel})
	return nil
}

func copyFile(dst, src string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	ch := a.subscribeEvents()
	defer a.unsubscribeEvents(ch)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	for {
		select {
		case <-r.Context().Done():
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
	ch := make(chan ThumbnailEvent, 8)
	a.eventsMu.Lock()
	a.events[ch] = struct{}{}
	a.eventsMu.Unlock()
	return ch
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
	name := filepath.Base(chi.URLParam(r, "name"))
	path := filepath.Join(a.cfg.DataDir, "models", modelID, "thumbs", name)
	http.ServeFile(w, r, path)
}

var _ = sync.Mutex{}
