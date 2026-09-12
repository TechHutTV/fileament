package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestWorkersDrainQueuedThumbnailsPromptly(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	for range 3 {
		uploadSTLModel(t, app, cookie, "part.stl", "Queued model")
	}
	events := app.subscribeEvents()
	defer app.unsubscribeEvents(events)
	app.cfg.ThumbWorkers = 1
	app.startWorkers()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	for range 3 {
		select {
		case <-events:
		case <-deadline.C:
			t.Fatal("worker left queued thumbnails waiting for periodic ticks")
		}
	}
	app.stopWorkers()
	var done int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE status='done'`).Scan(&done); err != nil || done != 3 {
		t.Fatalf("done=%d err=%v", done, err)
	}
}

func TestTransientThumbnailRetriesAreBoundedAndSurviveRestart(t *testing.T) {
	app := newAuthedTestApp(t)
	model := uploadSTLModel(t, app, loginCookie(t, app, "password-password"), "part.stl", "Retry")
	for attempt := 1; attempt <= 3; attempt++ {
		app.mutationFault = func(step string) error {
			if step == "model-sidecar" {
				return &os.PathError{Op: "write", Path: "private-storage-path", Err: syscall.EBUSY}
			}
			return nil
		}
		if err := app.processNextThumbnail(); err == nil {
			t.Fatal("injected failure was lost")
		}
		var status string
		var attempts int
		var available int64
		if err := app.db.QueryRow(`SELECT status,attempts,available_at FROM jobs WHERE file_id=?`, model.Files[0].ID).Scan(&status, &attempts, &available); err != nil {
			t.Fatal(err)
		}
		if attempts != attempt {
			t.Fatalf("attempts=%d want=%d", attempts, attempt)
		}
		if attempt == 3 {
			if status != "failed" || available != 0 {
				t.Fatalf("retry limit ignored: status=%s available=%d", status, available)
			}
			break
		}
		if status != "pending" || available <= time.Now().Unix() {
			t.Fatalf("retry not delayed: status=%s available=%d", status, available)
		}
		if worked, err := app.processThumbnailJob(context.Background()); worked || err != nil {
			t.Fatalf("future retry claimed: worked=%v err=%v", worked, err)
		}
		if attempt == 1 {
			if err := app.Close(); err != nil {
				t.Fatal(err)
			}
			var err error
			app, err = New(app.cfg, app.webFS)
			if err != nil {
				t.Fatal(err)
			}
			defer app.Close()
			var restored int64
			if err := app.db.QueryRow(`SELECT available_at FROM jobs WHERE file_id=?`, model.Files[0].ID).Scan(&restored); err != nil || restored != available {
				t.Fatalf("retry deadline lost on restart: %d %v", restored, err)
			}
		}
		if _, err := app.db.Exec(`UPDATE jobs SET available_at=0`); err != nil {
			t.Fatal(err)
		}
	}
	app.mutationFault = nil
	if worked, err := app.processThumbnailJob(context.Background()); worked || err != nil {
		t.Fatalf("terminal failure restarted: worked=%v err=%v", worked, err)
	}
}

func TestThumbnailRetryMigrationPreservesExistingJobs(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "old.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(schema + jobLifecycleMigration + queryIndexesMigration + `
INSERT INTO models(id,title,created_at,updated_at) VALUES('m','Kept',1,1);
INSERT INTO files(id,model_id,filename,rel_path,format,size_bytes) VALUES('f','m','part.stl','files/part.stl','stl',1);
INSERT INTO jobs(id,type,file_id,status,attempts,error,created_at) VALUES('j','thumbnail','f','failed',2,'diagnostic',1);`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	var status, diagnostic string
	var attempts, available int
	if err := db.QueryRow(`SELECT status,attempts,error,available_at FROM jobs WHERE id='j'`).Scan(&status, &attempts, &diagnostic, &available); err != nil || status != "failed" || attempts != 2 || diagnostic != "diagnostic" || available != 0 {
		t.Fatalf("migration lost job: %s %d %s %d %v", status, attempts, diagnostic, available, err)
	}
	plan := queryPlan(t, db, `SELECT id,file_id FROM jobs WHERE type='thumbnail' AND status='pending' AND available_at<=100 ORDER BY available_at,created_at,id LIMIT 1`)
	if !strings.Contains(plan, "jobs_pending_ready") || strings.Contains(plan, "TEMP B-TREE") {
		t.Fatal(plan)
	}
}

func TestThumbnailRetryRequiresOwnerAndSafeOrigin(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	model := uploadSTLModel(t, app, cookie, "part.stl", "Private")
	path := "/api/models/" + model.ID + "/thumbnails/retry"
	for _, authenticated := range []bool{false, true} {
		req := jsonReq(http.MethodPost, path, "{}")
		want := http.StatusUnauthorized
		if authenticated {
			req.AddCookie(cookie)
			req.Header.Set("Origin", "https://hostile.example")
			want = http.StatusForbidden
		}
		rec := httptest.NewRecorder()
		app.Router().ServeHTTP(rec, req)
		if rec.Code != want {
			t.Fatalf("authenticated=%v status=%d", authenticated, rec.Code)
		}
	}
	states, err := app.modelThumbnailStates(context.Background(), model.ID)
	if err != nil || len(states) != 1 || states[0].Status != "pending" {
		t.Fatalf("invalid state after denied requests: %+v %v", states, err)
	}
	if _, err := app.db.Exec(`UPDATE jobs SET status='failed',error=?`, fmt.Sprintf("private diagnostic at %s", app.cfg.DataDir)); err != nil {
		t.Fatal(err)
	}
	rec := serveMutationRequest(app, cookie, jsonReq(http.MethodGet, "/api/models/"+model.ID, ""))
	if strings.Contains(rec.Body.String(), app.cfg.DataDir) || strings.Contains(rec.Body.String(), "private diagnostic") {
		t.Fatal("owner model response exposed raw job diagnostic")
	}
	rec = serveMutationRequest(app, cookie, jsonReq(http.MethodPost, "/api/models/missing/thumbnails/retry", "{}"))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing model retry status=%d", rec.Code)
	}
}

func TestThumbnailStatesRebuildWithoutPersistingJobDiagnostics(t *testing.T) {
	app := newAuthedTestApp(t)
	model := uploadSTLModel(t, app, loginCookie(t, app, "password-password"), "part.stl", "Rebuild")
	if _, err := app.db.Exec(`UPDATE jobs SET status='failed',error='private diagnostic',attempts=3`); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(app.cfg.DataDir, "models", model.ID, "model.json"))
	if err != nil || strings.Contains(string(contents), "thumbnailJobs") || strings.Contains(string(contents), "private diagnostic") {
		t.Fatalf("operational state entered sidecar: %v", err)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(app.cfg.DataDir, "fileament.db")); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := New(app.cfg, app.webFS)
	if err != nil {
		t.Fatal(err)
	}
	defer rebuilt.Close()
	states, err := rebuilt.modelThumbnailStates(context.Background(), model.ID)
	if err != nil || len(states) != 1 || states[0].Status != "pending" || states[0].Attempts != 0 {
		t.Fatalf("missing preview not queued by rebuild: %+v %v", states, err)
	}
	if err := rebuilt.processNextThumbnail(); err != nil {
		t.Fatal(err)
	}
	cookie := loginCookie(t, rebuilt, "password-password")
	rec := serveMutationRequest(rebuilt, cookie, jsonReq(http.MethodPost, "/api/models/"+model.ID+"/thumbnails/retry", "{}"))
	var response struct{ Queued int }
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || rec.Code != http.StatusOK || response.Queued != 0 {
		t.Fatalf("successful preview was retried: %d %s %v", rec.Code, rec.Body.String(), err)
	}
	states, err = rebuilt.modelThumbnailStates(context.Background(), model.ID)
	if err != nil || len(states) != 1 || states[0].Status != "done" {
		t.Fatalf("successful preview state: %+v %v", states, err)
	}
}

func TestThumbnailFailurePublishesStateAndCanBeRetried(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	model := uploadSTLModel(t, app, cookie, "part.stl", "Saved model")
	path := filepath.Join(app.cfg.DataDir, "models", model.ID, model.Files[0].RelPath)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("broken mesh"), 0o600); err != nil {
		t.Fatal(err)
	}
	events := app.subscribeEvents()
	defer app.unsubscribeEvents(events)
	if err := app.processNextThumbnail(); err == nil {
		t.Fatal("invalid mesh rendered")
	}
	select {
	case event := <-events:
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		var state map[string]any
		if err := json.Unmarshal(data, &state); err != nil {
			t.Fatal(err)
		}
		if state["status"] != "failed" || state["modelId"] != model.ID || state["fileId"] != model.Files[0].ID {
			t.Fatalf("failure event=%s", data)
		}
	default:
		t.Fatal("thumbnail failure was not published")
	}
	rec := serveMutationRequest(app, cookie, jsonReq(http.MethodGet, "/api/models/"+model.ID, ""))
	var detail struct {
		ThumbnailJobs []struct {
			FileID string `json:"fileId"`
			Status string `json:"status"`
		} `json:"thumbnailJobs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &detail); err != nil || len(detail.ThumbnailJobs) != 1 || detail.ThumbnailJobs[0].Status != "failed" {
		t.Fatalf("failure status missing after event: %s (%v)", rec.Body.String(), err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		rec := serveMutationRequest(app, cookie, jsonReq(http.MethodPost, "/api/models/"+model.ID+"/thumbnails/retry", "{}"))
		if rec.Code != http.StatusOK {
			t.Fatalf("retry=%d %s", rec.Code, rec.Body.String())
		}
	}
	var pending int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM jobs WHERE status='pending'`).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("retry duplicated work: pending=%d err=%v", pending, err)
	}
	if err := app.processNextThumbnail(); err != nil {
		t.Fatal(err)
	}
	updated, err := app.getModel(model.ID)
	if err != nil || updated.PrimaryThumb == "" {
		t.Fatalf("retry did not produce preview: %+v %v", updated, err)
	}
}
