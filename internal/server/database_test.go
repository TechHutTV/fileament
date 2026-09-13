package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/TechHutTV/fileament/internal/config"
)

func TestDatabaseReaderDoesNotBlockWriter(t *testing.T) {
	app := newTestApp(t)
	if _, err := app.db.Exec(`INSERT INTO settings(key,value) VALUES('concurrency','before')`); err != nil {
		t.Fatal(err)
	}
	reader, err := app.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Rollback()
	var value string
	if err := reader.QueryRow(`SELECT value FROM settings WHERE key='concurrency'`).Scan(&value); err != nil {
		t.Fatal(err)
	}
	writer, err := app.db.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.ExecContext(context.Background(), `PRAGMA busy_timeout=100`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.ExecContext(context.Background(), `UPDATE settings SET value='after' WHERE key='concurrency'`); err != nil {
		t.Fatal(err)
	}
	if err := reader.QueryRow(`SELECT value FROM settings WHERE key='concurrency'`).Scan(&value); err != nil || value != "before" {
		t.Fatalf("reader lost its snapshot: value=%q err=%v", value, err)
	}
	if err := reader.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := writer.QueryRowContext(context.Background(), `SELECT value FROM settings WHERE key='concurrency'`).Scan(&value); err != nil || value != "after" {
		t.Fatalf("write was lost: value=%q err=%v", value, err)
	}
}

func TestDatabaseConcurrentReadModifyWrites(t *testing.T) {
	app := newTestApp(t)
	if _, err := app.db.Exec(`INSERT INTO settings(key,value) VALUES('counter','0')`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	errors := make(chan error, 6)
	for range 6 {
		wg.Go(func() {
			for range 10 {
				if err := incrementDatabaseCounter(ctx, app.db); err != nil {
					errors <- err
					return
				}
			}
		})
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	var count int
	if err := app.db.QueryRow(`SELECT value FROM settings WHERE key='counter'`).Scan(&count); err != nil || count != 60 {
		t.Fatalf("count=%d err=%v", count, err)
	}
}

func incrementDatabaseCounter(ctx context.Context, db *sql.DB) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT value FROM settings WHERE key='counter'`).Scan(&count); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE settings SET value=? WHERE key='counter'`, count+1); err != nil {
		return err
	}
	return tx.Commit()
}

func TestCanceledCatalogReadReleasesPoolWait(t *testing.T) {
	app := newTestApp(t)
	app.db.SetMaxOpenConns(1)
	for name, handler := range map[string]http.HandlerFunc{
		"model":       app.handleGetModel,
		"tags":        app.handleTags,
		"shares":      app.handleListShares,
		"public":      app.handlePublic,
		"owner asset": func(w http.ResponseWriter, r *http.Request) { app.serveModelFile(w, r, true) },
	} {
		t.Run(name, func(t *testing.T) {
			conn, err := app.db.Conn(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				handler(httptest.NewRecorder(), httptest.NewRequestWithContext(ctx, http.MethodGet, "/", nil))
			}()
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				_ = conn.Close()
				<-done
				t.Fatal("canceled read waited for the database connection")
			}
		})
	}
}

func TestDatabasePathEscapesURICharacters(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "library ?mode=memory#data")
	app := newTestAppWithConfig(t, config.Config{DataDir: dir, OwnerPassword: "password-password", MaxUploadMB: 32, ThumbWorkers: 0})
	var sequence int
	var name, path string
	if err := app.db.QueryRow(`PRAGMA database_list`).Scan(&sequence, &name, &path); err != nil {
		t.Fatal(err)
	}
	expected, err := filepath.EvalSymlinks(filepath.Join(dir, "fileament.db"))
	if err != nil {
		t.Fatal(err)
	}
	if path != expected {
		t.Fatalf("database opened outside configured directory: %q", path)
	}
	cookie := loginCookie(t, app, "password-password")
	model := uploadSTLModel(t, app, cookie, "part.stl", "URI path")
	inspection := inspectBackup(t, app, cookie, downloadBackup(t, app, cookie))
	rec := serveMutationRequest(app, cookie, jsonReq(http.MethodPost, "/api/backups/restore", fmt.Sprintf(`{"restoreToken":%q,"confirmation":"RESTORE"}`, inspection.RestoreToken)))
	if rec.Code != http.StatusOK {
		t.Fatalf("restore: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := app.getModel(model.ID); err != nil {
		t.Fatal(err)
	}
}

func TestDatabaseConnectionDurabilityAndCancellation(t *testing.T) {
	app := newTestApp(t)
	connections := make([]*sql.Conn, 0, 4)
	defer func() {
		for _, conn := range connections {
			_ = conn.Close()
		}
	}()
	for range 4 {
		conn, err := app.db.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		connections = append(connections, conn)
		for pragma, expected := range map[string]int{"foreign_keys": 1, "synchronous": 2, "busy_timeout": 5000, "wal_autocheckpoint": 1000} {
			var got int
			if err := conn.QueryRowContext(context.Background(), "PRAGMA "+pragma).Scan(&got); err != nil || got != expected {
				t.Fatalf("%s=%d err=%v", pragma, got, err)
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if conn, err := app.db.Conn(ctx); err == nil {
		_ = conn.Close()
		t.Fatal("connection pool exceeded its limit")
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	for _, conn := range connections {
		_ = conn.Close()
	}
	app.db.SetMaxOpenConns(1)
	for range 4 {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		var n int
		err := app.db.QueryRowContext(ctx, `WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<1000000000) SELECT SUM(x) FROM n`).Scan(&n)
		cancel()
		if err == nil {
			t.Fatal("expensive query ignored cancellation")
		}
		checkCtx, stop := context.WithTimeout(context.Background(), time.Second)
		err = app.db.QueryRowContext(checkCtx, `SELECT 1`).Scan(&n)
		stop()
		if err != nil || n != 1 {
			t.Fatalf("connection unusable after cancellation: %v", err)
		}
	}
}

func TestWALRestoreRecoveryDiscardsReplacementJournal(t *testing.T) {
	app := newAuthedTestApp(t)
	model := uploadSTLModel(t, app, loginCookie(t, app, "password-password"), "original.stl", "Original")
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	current, err := managedTopLevelEntries(app.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	const token = "wal-restore"
	rollback := filepath.Join(app.cfg.DataDir, ".restore", "rollback", token)
	if err := os.MkdirAll(rollback, 0o700); err != nil {
		t.Fatal(err)
	}
	journal := restoreJournal{Version: 1, Token: token, CurrentEntries: current, RestoredEntries: []string{"fileament.db", "models", "fileament.db-wal", "fileament.db-shm", "fileament.db-journal"}}
	if err := writeRestoreJournal(app.cfg.DataDir, journal); err != nil {
		t.Fatal(err)
	}
	for _, name := range current {
		if err := os.Rename(filepath.Join(app.cfg.DataDir, name), filepath.Join(rollback, name)); err != nil {
			t.Fatal(err)
		}
	}
	replacement, err := openDatabase(app.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer replacement.Close()
	if _, err := replacement.Exec(`PRAGMA wal_autocheckpoint=0`); err != nil {
		t.Fatal(err)
	}
	if _, err := replacement.Exec(`INSERT INTO settings(key,value) VALUES('replacement','uncommitted restore')`); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Join(app.cfg.DataDir, "fileament.db-wal")); err != nil || info.Size() == 0 {
		t.Fatalf("missing live WAL: %v", err)
	}
	crashed := t.TempDir()
	// Capture a quiet database with its committed WAL, without closing it.
	if err := os.CopyFS(crashed, os.DirFS(app.cfg.DataDir)); err != nil {
		t.Fatal(err)
	}
	if err := replacement.Close(); err != nil {
		t.Fatal(err)
	}
	cfg := app.cfg
	cfg.DataDir = crashed
	recovered, err := New(cfg, app.webFS)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	if _, err := recovered.getModel(model.ID); err != nil {
		t.Fatalf("original model lost: %v", err)
	}
	var count int
	if err := recovered.db.QueryRow(`SELECT COUNT(*) FROM settings WHERE key='replacement'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("replacement WAL contaminated original database: count=%d err=%v", count, err)
	}
	var integrity string
	if err := recovered.db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity=%q err=%v", integrity, err)
	}
}
