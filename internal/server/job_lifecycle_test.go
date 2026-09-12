package server

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestModelDeletionRemovesEveryJobState(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	model := uploadSTLModel(t, app, cookie, "part.stl", "Part")
	for _, status := range []string{"running", "done", "failed"} {
		if _, err := app.db.Exec(`INSERT INTO jobs(id,type,file_id,status,created_at) VALUES(?, 'thumbnail', ?, ?, 1)`, status, model.Files[0].ID, status); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(http.MethodDelete, "/api/models/"+model.ID, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete=%d %s", rec.Code, rec.Body.String())
	}
	var jobs int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM jobs`).Scan(&jobs); err != nil || jobs != 0 {
		t.Fatalf("orphan jobs=%d err=%v", jobs, err)
	}
	if err := app.processNextThumbnail(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(app.cfg.DataDir, "models", model.ID)); !os.IsNotExist(err) {
		t.Fatalf("deleted model directory exists: %v", err)
	}
	inspectBackup(t, app, cookie, downloadBackup(t, app, cookie))
	cfg, web := app.cfg, app.webFS
	for _, fresh := range []bool{false, true} {
		if err := app.Close(); err != nil {
			t.Fatal(err)
		}
		if fresh {
			if err := os.Remove(filepath.Join(cfg.DataDir, "fileament.db")); err != nil {
				t.Fatal(err)
			}
		}
		var err error
		app, err = New(cfg, web)
		if err != nil {
			t.Fatal(err)
		}
		if err := app.db.QueryRow(`SELECT COUNT(*) FROM jobs`).Scan(&jobs); err != nil || jobs != 0 {
			t.Fatalf("restart fresh=%v jobs=%d err=%v", fresh, jobs, err)
		}
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestJobMigrationCleansOrphansAndPreservesValidWork(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "old.db"))+"?_pragma=foreign_keys(ON)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(schema + `
PRAGMA user_version = 1;
INSERT INTO models(id,title,created_at,updated_at) VALUES('model','Model',1,1);
INSERT INTO files(id,model_id,filename,rel_path,format,size_bytes) VALUES('file','model','part.stl','files/part.stl','stl',1);
INSERT INTO jobs(id,type,file_id,status,attempts,error,created_at) VALUES
 ('pending','thumbnail','file','pending',0,NULL,1),
 ('failed','thumbnail','file','failed',2,'retained diagnostic',2),
 ('orphan','thumbnail','missing','pending',0,NULL,3),
 ('missing','thumbnail',NULL,'pending',0,NULL,4);
`); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err != nil {
		t.Fatalf("repeat migration: %v", err)
	}
	var count, version, attempts int
	if err := db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != schemaVersion {
		t.Fatalf("version=%d err=%v", version, err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM jobs`).Scan(&count); err != nil || count != 2 {
		t.Fatalf("jobs=%d err=%v", count, err)
	}
	var diagnostic string
	var finished sql.NullInt64
	if err := db.QueryRow(`SELECT attempts, error, finished_at FROM jobs WHERE id='failed'`).Scan(&attempts, &diagnostic, &finished); err != nil || attempts != 2 || diagnostic != "retained diagnostic" || !finished.Valid {
		t.Fatalf("migrated failed job: attempts=%d diagnostic=%q finished=%v err=%v", attempts, diagnostic, finished, err)
	}
	if _, err := db.Exec(`INSERT INTO jobs(id,type,file_id,status,created_at) VALUES('bad','thumbnail','missing','pending',1)`); err == nil {
		t.Fatal("new orphan accepted")
	}
	if _, err := db.Exec(`DELETE FROM models WHERE id='model'`); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM jobs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("cascade jobs=%d err=%v", count, err)
	}
	if _, err := db.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, schemaVersion+1)); err != nil {
		t.Fatal(err)
	}
	if err := migrate(db); err == nil {
		t.Fatal("newer schema accepted")
	}
}

func TestDeletedModelCannotPublishAnActiveThumbnail(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	model := uploadSTLModel(t, app, cookie, "part.stl", "Part")
	jobID, fileID, err := app.claimThumbnailJob(context.Background())
	if err != nil || jobID == "" {
		t.Fatalf("claim=%s err=%v", jobID, err)
	}
	prepared := filepath.Join(app.cfg.DataDir, "tmp", "prepared.png")
	if err := os.WriteFile(prepared, []byte("completed render"), 0o600); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodDelete, "/api/models/"+model.ID, nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete=%d %s", rec.Code, rec.Body.String())
	}
	if err := app.publishThumbnail(context.Background(), jobID, fileID, model.ID, model.Files[0].RelPath, prepared); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(app.cfg.DataDir, "models", model.ID)); !os.IsNotExist(err) {
		t.Fatalf("late worker recreated deleted model: %v", err)
	}
	var count int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM jobs`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("late worker recreated job: count=%d err=%v", count, err)
	}
}

func TestTerminalJobRetentionPreservesActiveWork(t *testing.T) {
	app := newAuthedTestApp(t)
	model := uploadSTLModel(t, app, loginCookie(t, app, "password-password"), "part.stl", "Part")
	now := time.Now()
	tx, err := app.db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for i := 0; i < 1010; i++ {
		if _, err := tx.Exec(`INSERT INTO jobs(id,type,file_id,status,created_at,finished_at) VALUES(?, 'thumbnail', ?, 'done', ?, ?)`, fmt.Sprint(i), model.Files[0].ID, now.Unix(), now.Unix()-int64(i)); err != nil {
			t.Fatal(err)
		}
	}
	for _, status := range []string{"pending", "running", "done", "failed"} {
		if _, err := tx.Exec(`INSERT INTO jobs(id,type,file_id,status,created_at,finished_at) VALUES(?, 'thumbnail', ?, ?, 1, 1)`, "old-"+status, model.Files[0].ID, status); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := app.pruneThumbnailJobs(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	for query, want := range map[string]int{
		`SELECT COUNT(*) FROM jobs WHERE status IN ('done','failed')`:            1000,
		`SELECT COUNT(*) FROM jobs WHERE status IN ('pending','running')`:        3,
		`SELECT COUNT(*) FROM jobs WHERE id IN ('old-done','old-failed','1009')`: 0,
	} {
		var count int
		if err := app.db.QueryRow(query).Scan(&count); err != nil || count != want {
			t.Fatalf("%s: count=%d want=%d err=%v", query, count, want, err)
		}
	}
}
