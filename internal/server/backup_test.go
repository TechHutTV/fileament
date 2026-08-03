package server

import (
	"archive/zip"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBackupDownloadCapturesPersistentDataAndOmitsTransientState(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	model := uploadSTLModel(t, app, cookie, "backup.stl", "Backup model")
	collectionReq := httptest.NewRequest(http.MethodPost, "/api/collections", strings.NewReader(`{"name":"Backup collection"}`))
	collectionReq.Header.Set("Content-Type", "application/json")
	collectionReq.AddCookie(cookie)
	collectionRec := httptest.NewRecorder()
	app.Router().ServeHTTP(collectionRec, collectionReq)
	if collectionRec.Code != http.StatusCreated {
		t.Fatalf("collection create status=%d body=%s", collectionRec.Code, collectionRec.Body.String())
	}
	if _, err := app.db.Exec(`INSERT INTO share_links(id,token,scope,target_id,label,created_at) VALUES('share_backup','token_backup','model',?,'Backup share',1)`, model.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(app.cfg.DataDir, "future"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app.cfg.DataDir, "future", "state.bin"), []byte("future state"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"tmp", "backups", ".restore"} {
		if err := os.MkdirAll(filepath.Join(app.cfg.DataDir, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(app.cfg.DataDir, dir, "excluded.txt"), []byte("exclude me"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	req := httptest.NewRequest(http.MethodPost, "/api/backups", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("backup status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q want no-store", got)
	}
	if got := rec.Header().Get("Content-Disposition"); !strings.Contains(got, ".fileament") {
		t.Fatalf("Content-Disposition=%q", got)
	}

	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatal(err)
	}
	entries := map[string]*zip.File{}
	for _, entry := range zr.File {
		entries[entry.Name] = entry
		if strings.HasPrefix(entry.Name, "data/tmp/") || strings.HasPrefix(entry.Name, "data/backups/") || strings.HasPrefix(entry.Name, "data/.restore/") {
			t.Fatalf("backup contains transient entry %q", entry.Name)
		}
	}
	for _, name := range []string{
		"manifest.json",
		"data/fileament.db",
		"data/collections.json",
		"data/future/state.bin",
		"data/models/" + model.ID + "/model.json",
		"data/models/" + model.ID + "/" + model.Files[0].RelPath,
	} {
		if entries[name] == nil {
			t.Errorf("backup missing %q", name)
		}
	}

	manifestBytes := readZipEntry(t, entries["manifest.json"])
	var manifest struct {
		BackupFormatVersion int `json:"backupFormatVersion"`
		DataFormatVersion   int `json:"dataFormatVersion"`
		DatabaseVersion     int `json:"databaseVersion"`
		Models              int `json:"models"`
		Files               int `json:"files"`
		Collections         int `json:"collections"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.BackupFormatVersion != 1 || manifest.DataFormatVersion != 1 || manifest.DatabaseVersion != 1 {
		t.Fatalf("unexpected manifest versions: %+v", manifest)
	}
	if manifest.Models != 1 || manifest.Files != 1 || manifest.Collections != 1 {
		t.Fatalf("unexpected manifest counts: %+v", manifest)
	}

	dbPath := filepath.Join(t.TempDir(), "fileament.db")
	if err := os.WriteFile(dbPath, readZipEntry(t, entries["data/fileament.db"]), 0o600); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(dbPath)+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var integrity string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("snapshot integrity=%q err=%v", integrity, err)
	}
	for table, want := range map[string]int{"models": 1, "share_links": 1, "sessions": 0} {
		var got int
		if err := db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("%s count=%d want=%d", table, got, want)
		}
	}
}

func TestRestoreInspectStagesValidBackupWithoutChangingLibrary(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	uploadSTLModel(t, app, cookie, "current.stl", "Current model")
	backup := downloadBackup(t, app, cookie)

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	part, err := mw.CreateFormFile("file", "library.fileament")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(backup); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/backups/inspect", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("inspect status=%d body=%s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("inspect cache control=%q", rec.Header().Get("Cache-Control"))
	}
	var result struct {
		RestoreToken string         `json:"restoreToken"`
		Manifest     backupManifest `json:"manifest"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.RestoreToken == "" || result.Manifest.Models != 1 || result.Manifest.Files != 1 {
		t.Fatalf("unexpected inspection result: %+v", result)
	}
	var models int
	if err := app.db.QueryRow(`SELECT COUNT(*) FROM models`).Scan(&models); err != nil {
		t.Fatal(err)
	}
	if models != 1 {
		t.Fatalf("preflight changed model count to %d", models)
	}
}

func TestRestoreInspectionReplacesPreviousStage(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	backup := downloadBackup(t, app, cookie)
	first := inspectBackup(t, app, cookie, backup)
	second := inspectBackup(t, app, cookie, backup)
	stagingRoot := filepath.Join(app.cfg.DataDir, ".restore", "staging")
	if _, err := os.Stat(filepath.Join(stagingRoot, first.RestoreToken)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("first stage remains after replacement: %v", err)
	}
	if _, err := os.Stat(filepath.Join(stagingRoot, second.RestoreToken)); err != nil {
		t.Fatalf("replacement stage is missing: %v", err)
	}
}

func TestRestoreReplacesInstallationCreatesSafetyBackupAndInvalidatesSessions(t *testing.T) {
	source := newTestAppWithPassword(t, "source-password")
	sourceCookie := loginCookie(t, source, "source-password")
	sourceModel := uploadSTLModel(t, source, sourceCookie, "source.stl", "Source model")
	shareReq := jsonReq(http.MethodPost, "/api/shares", `{"scope":"model","targetId":"`+sourceModel.ID+`","label":"Source share"}`)
	shareReq.AddCookie(sourceCookie)
	shareRec := httptest.NewRecorder()
	source.Router().ServeHTTP(shareRec, shareReq)
	if shareRec.Code != http.StatusCreated {
		t.Fatalf("source share status=%d body=%s", shareRec.Code, shareRec.Body.String())
	}
	backup := downloadBackup(t, source, sourceCookie)

	destination := newTestAppWithPassword(t, "destination-password")
	destinationCookie := loginCookie(t, destination, "destination-password")
	destinationModel := uploadSTLModel(t, destination, destinationCookie, "destination.stl", "Destination model")
	inspection := inspectBackup(t, destination, destinationCookie, backup)
	restoreReq := jsonReq(http.MethodPost, "/api/backups/restore", `{"restoreToken":"`+inspection.RestoreToken+`","confirmation":"RESTORE"}`)
	restoreReq.AddCookie(destinationCookie)
	restoreRec := httptest.NewRecorder()
	destination.Router().ServeHTTP(restoreRec, restoreReq)
	if restoreRec.Code != http.StatusOK {
		t.Fatalf("restore status=%d body=%s", restoreRec.Code, restoreRec.Body.String())
	}

	var modelID, title string
	if err := destination.db.QueryRow(`SELECT id, title FROM models`).Scan(&modelID, &title); err != nil {
		t.Fatal(err)
	}
	if modelID != sourceModel.ID || title != sourceModel.Title {
		t.Fatalf("restored model id=%q title=%q", modelID, title)
	}
	if _, err := os.Stat(filepath.Join(destination.cfg.DataDir, "models", destinationModel.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("destination model directory still exists: %v", err)
	}
	var shares int
	if err := destination.db.QueryRow(`SELECT COUNT(*) FROM share_links WHERE label = 'Source share'`).Scan(&shares); err != nil || shares != 1 {
		t.Fatalf("restored shares=%d err=%v", shares, err)
	}
	backups, err := filepath.Glob(filepath.Join(destination.cfg.DataDir, "backups", "pre-restore-*.fileament"))
	if err != nil || len(backups) != 1 {
		t.Fatalf("safety backups=%v err=%v", backups, err)
	}

	meReq := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	meReq.AddCookie(destinationCookie)
	meRec := httptest.NewRecorder()
	destination.Router().ServeHTTP(meRec, meReq)
	if !strings.Contains(meRec.Body.String(), `"authenticated":false`) {
		t.Fatalf("restored session remained authenticated: %s", meRec.Body.String())
	}
	restoredCookie := loginCookie(t, destination, "source-password")
	reuseReq := jsonReq(http.MethodPost, "/api/backups/restore", `{"restoreToken":"`+inspection.RestoreToken+`","confirmation":"RESTORE"}`)
	reuseReq.AddCookie(restoredCookie)
	reuseRec := httptest.NewRecorder()
	destination.Router().ServeHTTP(reuseRec, reuseReq)
	if reuseRec.Code != http.StatusBadRequest {
		t.Fatalf("reused restore status=%d body=%s", reuseRec.Code, reuseRec.Body.String())
	}
	badLogin := httptest.NewRecorder()
	destination.Router().ServeHTTP(badLogin, jsonReq(http.MethodPost, "/api/auth/login", `{"password":"destination-password"}`))
	if badLogin.Code != http.StatusUnauthorized {
		t.Fatalf("destination password status=%d body=%s", badLogin.Code, badLogin.Body.String())
	}
}

func TestRestoreInspectRejectsUnsafeAndUnsupportedArchives(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	validManifest, err := json.Marshal(backupManifest{
		BackupFormatVersion: backupFormatVersion,
		DataFormatVersion:   dataFormatVersion,
		DatabaseVersion:     schemaVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	futureManifest, err := json.Marshal(backupManifest{
		BackupFormatVersion: backupFormatVersion + 1,
		DataFormatVersion:   dataFormatVersion,
		DatabaseVersion:     schemaVersion,
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name    string
		entries []testZipEntry
	}{
		{name: "traversal", entries: []testZipEntry{{Name: "manifest.json", Data: validManifest}, {Name: "data/../../outside", Data: []byte("escape")}}},
		{name: "symlink", entries: []testZipEntry{{Name: "manifest.json", Data: validManifest}, {Name: "data/link", Data: []byte("target"), Mode: os.ModeSymlink | 0o777}}},
		{name: "reserved restore path", entries: []testZipEntry{{Name: "manifest.json", Data: validManifest}, {Name: "data/.restore/state.json", Data: []byte("unsafe")}}},
		{name: "sqlite auxiliary file", entries: []testZipEntry{{Name: "manifest.json", Data: validManifest}, {Name: "data/fileament.db-wal", Data: []byte("unsafe")}}},
		{name: "future format", entries: []testZipEntry{{Name: "manifest.json", Data: futureManifest}, {Name: "data/fileament.db", Data: []byte("not reached")}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			archive := makeTestZip(t, tt.entries)
			rec := postBackupInspection(t, app, cookie, archive)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("inspect status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
	if _, err := os.Stat(filepath.Join(app.cfg.DataDir, "outside")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unsafe archive wrote outside staging: %v", err)
	}
}

func TestRestoreRequiresExactConfirmation(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	model := uploadSTLModel(t, app, cookie, "current.stl", "Current")
	inspection := inspectBackup(t, app, cookie, downloadBackup(t, app, cookie))
	req := jsonReq(http.MethodPost, "/api/backups/restore", `{"restoreToken":"`+inspection.RestoreToken+`","confirmation":"restore"}`)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("restore status=%d body=%s", rec.Code, rec.Body.String())
	}
	if _, err := app.getModel(model.ID); err != nil {
		t.Fatalf("rejected restore changed current data: %v", err)
	}
}

func TestRestoreRevalidatesSessionInsideExclusiveGate(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	inspection := inspectBackup(t, app, cookie, downloadBackup(t, app, cookie))
	if _, err := app.db.Exec(`DELETE FROM sessions`); err != nil {
		t.Fatal(err)
	}
	req := jsonReq(http.MethodPost, "/api/backups/restore", `{"restoreToken":"`+inspection.RestoreToken+`","confirmation":"RESTORE"}`)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.handleApplyRestore(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if app.maintenance.Load() {
		t.Fatal("rejected restore left maintenance enabled")
	}
}

func TestBackupRevalidatesSessionInsideExclusiveGate(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	if _, err := app.db.Exec(`DELETE FROM sessions`); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/backups", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.handleCreateBackup(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestOpenEventStreamDoesNotBlockBackup(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	ctx, cancel := context.WithCancel(context.Background())
	eventReq := httptest.NewRequest(http.MethodGet, "/api/events", nil).WithContext(ctx)
	eventReq.AddCookie(cookie)
	eventDone := make(chan struct{})
	go func() {
		app.Router().ServeHTTP(httptest.NewRecorder(), eventReq)
		close(eventDone)
	}()
	deadline := time.Now().Add(time.Second)
	for {
		app.eventsMu.Lock()
		subscribers := len(app.events)
		app.eventsMu.Unlock()
		if subscribers == 1 {
			break
		}
		if time.Now().After(deadline) {
			cancel()
			<-eventDone
			t.Fatal("event stream did not subscribe")
		}
		time.Sleep(5 * time.Millisecond)
	}
	backupDone := make(chan int, 1)
	go func() {
		req := httptest.NewRequest(http.MethodPost, "/api/backups", nil)
		req.AddCookie(cookie)
		rec := httptest.NewRecorder()
		app.Router().ServeHTTP(rec, req)
		backupDone <- rec.Code
	}()
	select {
	case status := <-backupDone:
		if status != http.StatusOK {
			t.Fatalf("backup status=%d", status)
		}
	case <-time.After(2 * time.Second):
		cancel()
		<-eventDone
		status := <-backupDone
		t.Fatalf("open event stream blocked backup; eventual status=%d", status)
	}
	cancel()
	<-eventDone
}

func TestStartupRollsBackInterruptedRestore(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	model := uploadSTLModel(t, app, cookie, "original.stl", "Original")
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	currentEntries, err := managedTopLevelEntries(app.cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	token := "interrupted-restore"
	rollbackRoot := filepath.Join(app.cfg.DataDir, ".restore", "rollback", token)
	if err := os.MkdirAll(rollbackRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(app.cfg.DataDir, ".restore", "applying", token), 0o700); err != nil {
		t.Fatal(err)
	}
	journal := restoreJournal{
		Version:         1,
		Token:           token,
		CurrentEntries:  currentEntries,
		RestoredEntries: []string{"fileament.db", "models", "introduced"},
	}
	if err := writeRestoreJournal(app.cfg.DataDir, journal); err != nil {
		t.Fatal(err)
	}
	for _, name := range currentEntries {
		if err := os.Rename(filepath.Join(app.cfg.DataDir, name), filepath.Join(rollbackRoot, name)); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(app.cfg.DataDir, "fileament.db"), []byte("interrupted replacement"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(app.cfg.DataDir, "models"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(app.cfg.DataDir, "introduced"), []byte("new data"), 0o600); err != nil {
		t.Fatal(err)
	}
	recovered, err := New(app.cfg, app.webFS)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	if _, err := recovered.getModel(model.ID); err != nil {
		t.Fatalf("original model was not recovered: %v", err)
	}
	if _, err := os.Stat(filepath.Join(app.cfg.DataDir, "introduced")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("introduced restore data remains after recovery: %v", err)
	}
	if _, err := os.Stat(filepath.Join(app.cfg.DataDir, ".restore", "state.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("restore journal remains after recovery: %v", err)
	}
}

func TestBackupRoutesRequireAuthentication(t *testing.T) {
	app := newAuthedTestApp(t)
	for _, request := range []*http.Request{
		httptest.NewRequest(http.MethodPost, "/api/backups", nil),
		httptest.NewRequest(http.MethodPost, "/api/backups/inspect", nil),
		jsonReq(http.MethodPost, "/api/backups/restore", `{"restoreToken":"missing","confirmation":"RESTORE"}`),
	} {
		rec := httptest.NewRecorder()
		app.Router().ServeHTTP(rec, request)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s status=%d body=%s", request.URL.Path, rec.Code, rec.Body.String())
		}
	}
}

func TestCopyBackupEntryEnforcesActualExpandedLimit(t *testing.T) {
	var destination bytes.Buffer
	copied, err := copyBackupEntry(&destination, strings.NewReader(strings.Repeat("x", 128)), 32)
	if err == nil || !strings.Contains(err.Error(), "expanded size limit") {
		t.Fatalf("copied=%d err=%v", copied, err)
	}
	if copied != 33 || destination.Len() != 33 {
		t.Fatalf("copy should stop one byte beyond the limit: copied=%d len=%d", copied, destination.Len())
	}
}

func TestReadFileLimitedRejectsOversizedMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.json")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 33), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readFileLimited(path, 32); err == nil || !strings.Contains(err.Error(), "metadata size limit") {
		t.Fatalf("oversized metadata error=%v", err)
	}
}

func TestConsumeRestoreStageIsExpiringAndOneTime(t *testing.T) {
	dataRoot := t.TempDir()
	now := time.Now()
	expired := filepath.Join(dataRoot, ".restore", "staging", "expired")
	if err := os.MkdirAll(expired, 0o700); err != nil {
		t.Fatal(err)
	}
	old := now.Add(-restoreStageTTL - time.Minute)
	if err := os.Chtimes(expired, old, old); err != nil {
		t.Fatal(err)
	}
	if _, err := consumeRestoreStage(dataRoot, "expired", now); !errors.Is(err, errRestoreStageUnavailable) {
		t.Fatalf("expired stage error=%v", err)
	}
	if _, err := os.Stat(expired); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired stage remains: %v", err)
	}

	fresh := filepath.Join(dataRoot, ".restore", "staging", "fresh")
	if err := os.MkdirAll(fresh, 0o700); err != nil {
		t.Fatal(err)
	}
	consumed, err := consumeRestoreStage(dataRoot, "fresh", now)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(filepath.Dir(consumed)) != "applying" {
		t.Fatalf("stage consumed into %q", consumed)
	}
	if _, err := consumeRestoreStage(dataRoot, "fresh", now); err == nil {
		t.Fatal("consumed stage token was reusable")
	}
}

func TestStartupCleansAbandonedRestoreWorkspace(t *testing.T) {
	app := newAuthedTestApp(t)
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"staging", "applying", "rollback"} {
		path := filepath.Join(app.cfg.DataDir, ".restore", name, "abandoned")
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	reopened, err := New(app.cfg, app.webFS)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for _, name := range []string{"staging", "applying", "rollback"} {
		path := filepath.Join(app.cfg.DataDir, ".restore", name)
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("abandoned %s workspace remains: %v", name, err)
		}
	}
}

func TestRestoreValidationRejectsDatabaseSidecarMismatch(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	model := uploadSTLModel(t, app, cookie, "mismatch.stl", "Matching model")
	archivePath := filepath.Join(t.TempDir(), "backup.fileament")
	if err := os.WriteFile(archivePath, downloadBackup(t, app, cookie), 0o600); err != nil {
		t.Fatal(err)
	}
	dataRoot := filepath.Join(t.TempDir(), "data")
	manifest, err := inspectAndExtractBackup(archivePath, dataRoot, 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	sidecarPath := filepath.Join(dataRoot, "models", model.ID, "model.json")
	contents, err := os.ReadFile(sidecarPath)
	if err != nil {
		t.Fatal(err)
	}
	var sidecar Model
	if err := json.Unmarshal(contents, &sidecar); err != nil {
		t.Fatal(err)
	}
	sidecar.Title = "Different model"
	contents, err = json.Marshal(sidecar)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sidecarPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateStagedData(dataRoot, manifest); err == nil || !strings.Contains(err.Error(), "does not match its database") {
		t.Fatalf("mismatched sidecar validation error=%v", err)
	}
}

func TestHealthReportsRestoreMaintenance(t *testing.T) {
	app := newAuthedTestApp(t)
	app.maintenance.Store(true)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("health status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func downloadBackup(t *testing.T, app *App, cookie *http.Cookie) []byte {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/backups", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("backup status=%d body=%s", rec.Code, rec.Body.String())
	}
	return bytes.Clone(rec.Body.Bytes())
}

func inspectBackup(t *testing.T, app *App, cookie *http.Cookie, backup []byte) backupInspection {
	t.Helper()
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	part, err := mw.CreateFormFile("file", "library.fileament")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(backup); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/backups/inspect", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("inspect status=%d body=%s", rec.Code, rec.Body.String())
	}
	var result backupInspection
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func postBackupInspection(t *testing.T, app *App, cookie *http.Cookie, backup []byte) *httptest.ResponseRecorder {
	t.Helper()
	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	part, err := mw.CreateFormFile("file", "library.fileament")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(backup); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/backups/inspect", body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	return rec
}

type testZipEntry struct {
	Name string
	Data []byte
	Mode os.FileMode
}

func makeTestZip(t *testing.T, entries []testZipEntry) []byte {
	t.Helper()
	var body bytes.Buffer
	zw := zip.NewWriter(&body)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.Name, Method: zip.Store}
		if entry.Mode != 0 {
			header.SetMode(entry.Mode)
		}
		writer, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(entry.Data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return body.Bytes()
}

func readZipEntry(t *testing.T, entry *zip.File) []byte {
	t.Helper()
	if entry == nil {
		t.Fatal("zip entry is missing")
	}
	r, err := entry.Open()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
