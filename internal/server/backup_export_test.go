package server

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/TechHutTV/fileament/internal/ids"
	"io"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestBackupExportEnforcesConfiguredSizeLimit(t *testing.T) {
	app := newAuthedTestApp(t)
	app.cfg.MaxBackupMB = 1
	cookie := loginCookie(t, app, "password-password")
	if err := os.WriteFile(filepath.Join(app.cfg.DataDir, "large.bin"), make([]byte, 2<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/backups", nil)
	req.AddCookie(cookie)
	rec := httptest.NewRecorder()
	app.Router().ServeHTTP(rec, req)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("export status=%d, want 413", rec.Code)
	}
}

func TestPrepareBackupRequiresAuthentication(t *testing.T) {
	app := newAuthedTestApp(t)
	for _, method := range []string{http.MethodPost, http.MethodGet} {
		path := "/api/backups/prepare"
		if method == http.MethodGet {
			path = "/api/backups/download/missing"
		}
		rec := httptest.NewRecorder()
		app.Router().ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s status=%d, want 401", path, rec.Code)
		}
	}
}

func TestPreparedBackupIsConsistentDuringConcurrentMutation(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	model := uploadSTLModel(t, app, cookie, "before.stl", "Before snapshot")
	captured, resume := make(chan struct{}), make(chan struct{})
	app.backupFault = func(stage string) error {
		close(captured)
		<-resume
		return nil
	}
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		done <- serveMutationRequest(app, cookie, httptest.NewRequest(http.MethodPost, "/api/backups/prepare", nil))
	}()
	<-captured
	mutated := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		mutated <- serveMutationRequest(app, cookie, jsonReq(http.MethodDelete, "/api/models/"+model.ID, ""))
	}()
	select {
	case rec := <-mutated:
		if rec.Code != http.StatusNoContent {
			t.Errorf("delete during compression: %d %s", rec.Code, rec.Body.String())
		}
	case <-time.After(2 * time.Second):
		close(resume)
		<-done
		<-mutated
		t.Fatal("backup compression blocked catalog mutation")
	}
	busy := serveMutationRequest(app, cookie, httptest.NewRequest(http.MethodPost, "/api/backups/prepare", nil))
	if busy.Code != http.StatusConflict {
		t.Errorf("parallel export status=%d", busy.Code)
	}
	close(resume)
	result := <-done
	if result.Code != http.StatusCreated {
		t.Fatal(result.Code, result.Body.String())
	}
	var ready preparedBackup
	if err := json.Unmarshal(result.Body.Bytes(), &ready); err != nil {
		t.Fatal(err)
	}
	rec := serveMutationRequest(app, cookie, httptest.NewRequest(http.MethodGet, ready.DownloadURL, nil))
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "private, no-store" {
		t.Fatalf("download status=%d cache=%q", rec.Code, rec.Header().Get("Cache-Control"))
	}
	inspection := inspectBackup(t, app, cookie, rec.Body.Bytes())
	if inspection.Manifest.Models != 1 || inspection.Manifest.Files != 1 {
		t.Fatalf("snapshot lost deleted model: %+v", inspection.Manifest)
	}
	stage := filepath.Join(app.cfg.DataDir, ".restore", "staging", inspection.RestoreToken, "data")
	contents, err := os.ReadFile(filepath.Join(stage, "models", model.ID, "model.json"))
	if err != nil || !strings.Contains(string(contents), "Before snapshot") {
		t.Fatalf("pre-mutation sidecar missing: %v", err)
	}
}

func TestPreparedBackupSessionExpiryReplacementAndShutdown(t *testing.T) {
	app := newAuthedTestApp(t)
	owner := loginCookie(t, app, "password-password")
	otherSession := loginCookie(t, app, "password-password")
	prepare := func() preparedBackup {
		t.Helper()
		rec := serveMutationRequest(app, owner, httptest.NewRequest(http.MethodPost, "/api/backups/prepare", nil))
		if rec.Code != http.StatusCreated {
			t.Fatal(rec.Code, rec.Body.String())
		}
		var ready preparedBackup
		if err := json.Unmarshal(rec.Body.Bytes(), &ready); err != nil {
			t.Fatal(err)
		}
		return ready
	}
	ready := prepare()
	firstPath := app.preparedBackup.path
	for _, cookie := range []*http.Cookie{nil, otherSession} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, ready.DownloadURL, nil)
		if cookie != nil {
			req.AddCookie(cookie)
		}
		app.Router().ServeHTTP(rec, req)
		want := http.StatusUnauthorized
		if cookie != nil {
			want = http.StatusNotFound
		}
		if rec.Code != want {
			t.Fatalf("foreign session status=%d want=%d", rec.Code, want)
		}
	}
	newReady := prepare()
	if _, err := os.Stat(firstPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replaced archive remains: %v", err)
	}
	if rec := serveMutationRequest(app, owner, httptest.NewRequest(http.MethodGet, ready.DownloadURL, nil)); rec.Code != http.StatusNotFound {
		t.Fatalf("old link status=%d", rec.Code)
	}
	req := httptest.NewRequest(http.MethodGet, newReady.DownloadURL, nil)
	req.Header.Set("Range", "bytes=0-3")
	if rec := serveMutationRequest(app, owner, req); rec.Code != http.StatusPartialContent || rec.Body.Len() != 4 {
		t.Fatalf("range download: %d %d", rec.Code, rec.Body.Len())
	}
	app.backupMu.Lock()
	app.preparedBackup.ExpiresAt = time.Now().Add(-time.Second).Unix()
	app.backupMu.Unlock()
	if rec := serveMutationRequest(app, owner, httptest.NewRequest(http.MethodGet, newReady.DownloadURL, nil)); rec.Code != http.StatusNotFound {
		t.Fatalf("expired link status=%d", rec.Code)
	}
	prepare()
	lastPath := app.preparedBackup.path
	if _, err := app.db.Exec("DELETE FROM sessions"); err != nil {
		t.Fatal(err)
	}
	if rec := serveMutationRequest(app, owner, httptest.NewRequest(http.MethodGet, app.preparedBackup.DownloadURL, nil)); rec.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session status=%d", rec.Code)
	}
	if err := app.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(lastPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("shutdown archive remains: %v", err)
	}
}

func TestBackupCancellationAndFailureRemoveWorkspace(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			app := newAuthedTestApp(t)
			cookie := loginCookie(t, app, "password-password")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			app.backupFault = func(string) error {
				if fail {
					return errors.New("injected disk failure")
				}
				cancel()
				return nil
			}
			req := httptest.NewRequest(http.MethodPost, "/api/backups/prepare", nil).WithContext(ctx)
			rec := serveMutationRequest(app, cookie, req)
			if rec.Code != http.StatusInternalServerError {
				t.Fatalf("failed export status=%d", rec.Code)
			}
			entries, err := os.ReadDir(filepath.Join(app.cfg.DataDir, "tmp", "backups"))
			if err != nil || len(entries) != 0 {
				t.Fatalf("failed export left workspace: %v %v", entries, err)
			}
		})
	}
}

func TestBackupSnapshotCopiesUnknownFilesAndRejectsLinks(t *testing.T) {
	app := newAuthedTestApp(t)
	path := filepath.Join(app.cfg.DataDir, "future.bin")
	if err := os.WriteFile(path, []byte("before"), 0o600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := app.captureBackupSnapshot(context.Background())
	if snapshot != nil {
		defer os.RemoveAll(snapshot.root)
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("after!"), 0o600); err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(snapshot.root, "future.bin"))
	if err != nil || string(contents) != "before" {
		t.Fatalf("unknown mutable file was not copied: %q %v", contents, err)
	}
	if err := os.Symlink(path, filepath.Join(app.cfg.DataDir, "link")); err != nil {
		t.Fatal(err)
	}
	linked, err := app.captureBackupSnapshot(context.Background())
	if linked != nil {
		defer os.RemoveAll(linked.root)
	}
	if err == nil {
		t.Fatal("snapshot accepted a symlink")
	}
}

func TestSafetyBackupRetentionPreservesNewestAndUnrelatedFiles(t *testing.T) {
	dir := t.TempDir()
	var names []string
	for i := 0; i < 5; i++ {
		name := fmt.Sprintf("pre-restore-2026010%dT000000Z-%s.fileament", i+1, ids.New())
		names = append(names, name)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("backup"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"personal.fileament", "pre-restore-not-generated.fileament"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneSafetyBackups(dir, names[4]); err != nil {
		t.Fatal(err)
	}
	for i, name := range names {
		_, err := os.Stat(filepath.Join(dir, name))
		if (i < 2 && !errors.Is(err, os.ErrNotExist)) || (i >= 2 && err != nil) {
			t.Fatalf("retention for %s: %v", name, err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 5 {
		t.Fatalf("retention removed unrelated files: %v %v", entries, err)
	}
}

func TestPreparedBackupTimerRemovesArchive(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	rec := serveMutationRequest(app, cookie, httptest.NewRequest(http.MethodPost, "/api/backups/prepare", nil))
	if rec.Code != http.StatusCreated {
		t.Fatal(rec.Code, rec.Body.String())
	}
	app.backupMu.Lock()
	path := app.preparedBackup.path
	app.backupTimer.Reset(0)
	app.backupMu.Unlock()
	deadline := time.Now().Add(2 * time.Second)
	for {
		_, err := os.Stat(path)
		if errors.Is(err, os.ErrNotExist) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("expired backup was not removed")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestBackupStoresCompressedAssetsWithoutRecompression(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	for _, name := range []string{"asset.3mf", "image.PNG", "image.jpeg", "plain.obj"} {
		if err := os.WriteFile(filepath.Join(app.cfg.DataDir, name), []byte("payload"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	payload := downloadBackup(t, app, cookie)
	zr, err := zip.NewReader(bytes.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range zr.File {
		if strings.HasPrefix(entry.Name, "data/asset.") || strings.HasPrefix(entry.Name, "data/image.") {
			if entry.Method != zip.Store {
				t.Fatalf("compressed asset %s was recompressed", entry.Name)
			}
		}
		if entry.Name == "data/plain.obj" && entry.Method != zip.Deflate {
			t.Fatal("plain mesh was not compressed")
		}
	}
}

func TestBackupArchiveOutputLimitAndCanceledSnapshot(t *testing.T) {
	app := newAuthedTestApp(t)
	app.cfg.MaxBackupMB = 1
	root := t.TempDir()
	payload := make([]byte, 1<<20)
	_, _ = rand.New(rand.NewSource(42)).Read(payload)
	if err := os.WriteFile(filepath.Join(root, "compressed.3mf"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	_, err := app.archiveBackupSnapshot(context.Background(), &backupSnapshot{root: root}, dir)
	if !errors.Is(err, errBackupTooLarge) {
		t.Fatalf("archive size limit error=%v", err)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("oversized archive remains: %v %v", entries, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	snapshot, err := app.captureBackupSnapshot(ctx)
	if snapshot != nil {
		defer os.RemoveAll(snapshot.root)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled snapshot error=%v", err)
	}
}

type stalledBackupWriter struct {
	header      http.Header
	started     chan struct{}
	interrupted chan struct{}
	once        sync.Once
}

func (w *stalledBackupWriter) Header() http.Header { return w.header }
func (w *stalledBackupWriter) WriteHeader(int)     {}
func (w *stalledBackupWriter) Write([]byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	<-w.interrupted
	return 0, io.ErrClosedPipe
}
func (w *stalledBackupWriter) SetWriteDeadline(deadline time.Time) error {
	if !deadline.IsZero() && !deadline.After(time.Now()) {
		close(w.interrupted)
	}
	return nil
}

func TestShutdownInterruptsStalledBackupDownload(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	rec := serveMutationRequest(app, cookie, httptest.NewRequest(http.MethodPost, "/api/backups/prepare", nil))
	if rec.Code != http.StatusCreated {
		t.Fatal(rec.Code, rec.Body.String())
	}
	writer := &stalledBackupWriter{header: make(http.Header), started: make(chan struct{}), interrupted: make(chan struct{})}
	req := httptest.NewRequest(http.MethodGet, app.preparedBackup.DownloadURL, nil)
	req.AddCookie(cookie)
	downloaded := make(chan struct{})
	go func() {
		app.Router().ServeHTTP(writer, req)
		close(downloaded)
	}()
	<-writer.started
	closed := make(chan error, 1)
	go func() { closed <- app.Close() }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		close(writer.interrupted)
		<-closed
		t.Fatal("shutdown waited for a stalled backup download")
	}
	<-downloaded
}

func TestBackupCompressedLimitIncludesArchiveOverhead(t *testing.T) {
	app := newAuthedTestApp(t)
	app.cfg.MaxBackupMB = 1
	root := t.TempDir()
	for i := 0; i < 7000; i++ {
		path := filepath.Join(root, fmt.Sprintf("entry-%024d", i))
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	destination := t.TempDir()
	_, err := app.archiveBackupSnapshot(context.Background(), &backupSnapshot{root: root}, destination)
	if !errors.Is(err, errBackupTooLarge) {
		t.Fatalf("ZIP headers bypassed the archive limit: %v", err)
	}
	if entries, err := os.ReadDir(destination); err != nil || len(entries) != 0 {
		t.Fatalf("over-limit archive was retained: %v %v", entries, err)
	}
}
