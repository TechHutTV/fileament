package server

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRejectedMeshUploadLeavesNoPartialModel(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(map[bool]string{false: "non-finite variant", true: "canceled upload"}[canceled], func(t *testing.T) {
			app := newAuthedTestApp(t)
			files := map[string]string{"valid.stl": validSTL()}
			if !canceled {
				files["invalid.obj"] = "v NaN 0 0\nv 1 0 0\nv 0 1 0\nf 1 2 3\n"
			}
			body, contentType := multipartGroupedModel(t, "Group", files)
			req := httptest.NewRequest(http.MethodPost, "/api/models/grouped", body)
			req.Header.Set("Content-Type", contentType)
			req.AddCookie(loginCookie(t, app, "password-password"))
			if canceled {
				ctx, cancel := context.WithCancel(req.Context())
				defer cancel()
				req = req.WithContext(ctx)
				req.Body = cancelUploadBody{ReadCloser: req.Body, cancel: cancel}
			}
			rec := httptest.NewRecorder()
			app.Router().ServeHTTP(rec, req)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			for _, table := range []string{"models", "files", "jobs"} {
				var count int
				if err := app.db.QueryRow("SELECT COUNT(*) FROM " + table).Scan(&count); err != nil || count != 0 {
					t.Fatalf("%s count=%d err=%v", table, count, err)
				}
			}
			for _, directory := range []string{"models", "tmp"} {
				entries, err := os.ReadDir(filepath.Join(app.cfg.DataDir, directory))
				if err != nil || len(entries) != 0 {
					t.Fatalf("%s: entries=%v err=%v", directory, entries, err)
				}
			}
		})
	}
}

type cancelUploadBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b cancelUploadBody) Read(p []byte) (int, error) {
	b.cancel()
	return b.ReadCloser.Read(p)
}

func TestThumbnailAdmissionCancellationDoesNotClaimJob(t *testing.T) {
	app := newAuthedTestApp(t)
	uploadSTLModel(t, app, loginCookie(t, app, "password-password"), "part.stl", "Part")
	for i := 0; i < cap(thumbnailSlots); i++ {
		thumbnailSlots <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(thumbnailSlots); i++ {
			<-thumbnailSlots
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := app.processNextThumbnailContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("admission = %v", err)
	}
	var status string
	var attempts int
	if err := app.db.QueryRow(`SELECT status, attempts FROM jobs`).Scan(&status, &attempts); err != nil || status != "pending" || attempts != 0 {
		t.Fatalf("status=%s attempts=%d err=%v", status, attempts, err)
	}
}

func TestStopWorkersCancelsActiveGeometryAndRequeuesJob(t *testing.T) {
	app := newAuthedTestApp(t)
	model := uploadSTLModel(t, app, loginCookie(t, app, "password-password"), "part.stl", "Part")
	data := make([]byte, 84+50*100_000)
	binary.LittleEndian.PutUint32(data[80:], 100_000)
	for offset := 84; offset < len(data); offset += 50 {
		binary.LittleEndian.PutUint32(data[offset+24:], math.Float32bits(1))
		binary.LittleEndian.PutUint32(data[offset+40:], math.Float32bits(1))
	}
	if err := os.WriteFile(filepath.Join(app.cfg.DataDir, "models", model.ID, model.Files[0].RelPath), data, 0o600); err != nil {
		t.Fatal(err)
	}
	app.cfg.ThumbWorkers = 1
	app.startWorkers()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var status string
		if err := app.db.QueryRow(`SELECT status FROM jobs`).Scan(&status); err != nil {
			t.Fatal(err)
		}
		if status == "running" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not claim job")
		}
		time.Sleep(time.Millisecond)
	}
	start := time.Now()
	app.stopWorkers()
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("shutdown took %s", elapsed)
	}
	var status string
	if err := app.db.QueryRow(`SELECT status FROM jobs`).Scan(&status); err != nil || status != "pending" {
		t.Fatalf("status=%s err=%v", status, err)
	}
	if _, err := os.Stat(filepath.Join(app.cfg.DataDir, "models", model.ID, "thumbs", model.Files[0].ID+".png")); !os.IsNotExist(err) {
		t.Fatalf("partial thumbnail exists: %v", err)
	}
}
