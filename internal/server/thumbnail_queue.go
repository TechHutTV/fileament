package server

import (
	"context"
	"database/sql"
	"errors"
	"net/http"
	"syscall"
	"time"

	"github.com/TechHutTV/fileament/internal/ids"
	"github.com/go-chi/chi/v5"
)

func (a *App) startWorkers() {
	if a.cfg.ThumbWorkers <= 0 || a.workerCancel != nil {
		return
	}
	if a.stop == nil {
		a.stop = make(chan struct{})
	}
	if a.thumbWake == nil {
		a.thumbWake = make(chan struct{}, 1)
	}
	ctx, cancel := context.WithCancel(context.Background())
	a.workerCancel = cancel
	for range a.cfg.ThumbWorkers {
		a.workerWG.Go(func() {
			for ctx.Err() == nil {
				worked, err := a.processThumbnailJob(ctx)
				if ctx.Err() != nil {
					return
				}
				if worked {
					continue
				}
				delay := a.thumbnailWorkDelay(ctx)
				if err != nil {
					delay = time.Second
				}
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
				case <-a.thumbWake:
				case <-timer.C:
				}
				timer.Stop()
			}
		})
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

func (a *App) wakeThumbnailWorkers() {
	select {
	case a.thumbWake <- struct{}{}:
	default:
	}
}

func (a *App) thumbnailWorkDelay(ctx context.Context) time.Duration {
	const fallback = 30 * time.Second
	a.dataMu.RLock()
	defer a.dataMu.RUnlock()
	if a.maintenance.Load() || a.db == nil {
		return fallback
	}
	var available sql.NullInt64
	if err := a.db.QueryRowContext(ctx, `SELECT MIN(available_at) FROM jobs WHERE type='thumbnail' AND status='pending'`).Scan(&available); err != nil || !available.Valid {
		return fallback
	}
	return max(100*time.Millisecond, min(fallback, time.Until(time.Unix(available.Int64, 0))))
}

func (a *App) failThumbnailJob(jobID string, cause error) (string, error) {
	if errors.Is(cause, context.Canceled) || errors.Is(cause, errMutationRecoveryRequired) {
		return "pending", a.finishThumbnailJob(jobID, "pending", cause.Error())
	}
	if transientThumbnailError(cause) {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		result, err := a.db.ExecContext(ctx, `UPDATE jobs SET status='pending', error=?, finished_at=NULL,
available_at=? + CASE WHEN attempts < 2 THEN 2 ELSE 4 END WHERE id=? AND attempts < 3`, cause.Error(), time.Now().Unix(), jobID)
		if err != nil {
			return "", err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return "", err
		}
		if n > 0 {
			return "pending", nil
		}
	}
	return "failed", a.finishThumbnailJob(jobID, "failed", cause.Error())
}

func transientThumbnailError(err error) bool {
	var sqliteError interface{ Code() int }
	if errors.As(err, &sqliteError) && (sqliteError.Code()&255 == 5 || sqliteError.Code()&255 == 6) {
		return true
	}
	return errors.Is(err, syscall.EINTR) || errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EBUSY) || errors.Is(err, syscall.EMFILE) || errors.Is(err, syscall.ENFILE)
}

type thumbnailState struct {
	FileID   string `json:"fileId"`
	Status   string `json:"status"`
	Attempts int    `json:"attempts"`
	RetryAt  int64  `json:"retryAt,omitempty"`
}

func (a *App) modelThumbnailStates(ctx context.Context, modelID string) ([]thumbnailState, error) {
	rows, err := a.db.QueryContext(ctx, `SELECT f.id,
CASE WHEN j.status IN ('pending','running','failed') THEN j.status
WHEN COALESCE(f.thumb_path,'') <> '' THEN 'done' ELSE 'unavailable' END,
COALESCE(j.attempts,0),COALESCE(j.available_at,0)
FROM files f LEFT JOIN jobs j ON j.id=(SELECT id FROM jobs WHERE type='thumbnail' AND file_id=f.id ORDER BY created_at DESC,id DESC LIMIT 1)
WHERE f.model_id=? ORDER BY f.sort_order,f.filename,f.id`, modelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	states := []thumbnailState{}
	for rows.Next() {
		var state thumbnailState
		if err := rows.Scan(&state.FileID, &state.Status, &state.Attempts, &state.RetryAt); err != nil {
			return nil, err
		}
		states = append(states, state)
	}
	return states, errors.Join(rows.Err(), rows.Close())
}

func (a *App) handleRetryThumbnails(w http.ResponseWriter, r *http.Request) {
	a.modelPersistMu.Lock()
	defer a.modelPersistMu.Unlock()
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	if a.maintenance.Load() {
		writeError(w, http.StatusServiceUnavailable, errMutationRecoveryRequired)
		return
	}
	modelID := chi.URLParam(r, "id")
	var exists int
	if err := a.db.QueryRowContext(r.Context(), `SELECT 1 FROM models WHERE id=?`, modelID).Scan(&exists); err != nil {
		writeError(w, collectionReadStatus(err), err)
		return
	}
	states, err := a.modelThumbnailStates(r.Context(), modelID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	tx, err := a.db.BeginTx(r.Context(), nil)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	defer tx.Rollback()
	queued := 0
	for _, state := range states {
		if state.Status != "failed" && state.Status != "unavailable" {
			continue
		}
		result, err := tx.ExecContext(r.Context(), `INSERT INTO jobs(id,type,file_id,status,created_at)
SELECT ?,'thumbnail',?,'pending',? WHERE NOT EXISTS
(SELECT 1 FROM jobs WHERE type='thumbnail' AND file_id=? AND status IN ('pending','running'))`, ids.New(), state.FileID, time.Now().Unix(), state.FileID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		n, err := result.RowsAffected()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		queued += int(n)
	}
	if err := tx.Commit(); err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	a.wakeThumbnailWorkers()
	writeJSON(w, http.StatusOK, map[string]int{"queued": queued})
}
