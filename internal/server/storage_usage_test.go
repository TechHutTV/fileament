package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestStorageUsageIncludesWorkspaceAndDoesNotFollowSymlinks(t *testing.T) {
	app := newAuthedTestApp(t)
	cookie := loginCookie(t, app, "password-password")
	fixtures := map[string]int{
		"models/example/files/part.stl":  32,
		"models/example/thumbs/file.png": 48,
		"backups/safety.fileament":       64,
		"tmp/backups/download.fileament": 80,
		".restore/staging/upload":        96,
		"future/state":                   112,
	}
	for name, size := range fixtures {
		path := filepath.Join(app.cfg.DataDir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "private"), make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(app.cfg.DataDir, "outside")); err != nil {
		t.Fatal(err)
	}
	rec := serveMutationRequest(app, cookie, httptest.NewRequest(http.MethodGet, "/api/storage", nil))
	var usage storageUsage
	if err := json.Unmarshal(rec.Body.Bytes(), &usage); err != nil || rec.Code != http.StatusOK {
		t.Fatal(rec.Code, err)
	}
	if usage.TotalBytes != 0 || usage.LibraryBytes < 32 || usage.ThumbnailBytes != 48 || usage.BackupBytes != 64 || usage.WorkspaceBytes != 176 || usage.OtherBytes != 112 || usage.DatabaseBytes <= 0 {
		t.Fatalf("incorrect storage categories: %+v", usage)
	}
	before := usage.DiskBytes
	if err := os.Link(filepath.Join(app.cfg.DataDir, "backups", "safety.fileament"), filepath.Join(app.cfg.DataDir, "tmp", "snapshot-link")); err != nil {
		t.Fatal(err)
	}
	var after storageUsage
	if err := after.measure(context.Background(), app.cfg.DataDir); err != nil {
		t.Fatal(err)
	}
	if before != nil && after.DiskBytes != nil && *after.DiskBytes-*before >= 4096 {
		t.Fatalf("hardlinked bytes were counted twice: before=%d after=%d", *before, *after.DiskBytes)
	}
	if after.WorkspaceBytes != 240 {
		t.Fatalf("workspace logical size=%d", after.WorkspaceBytes)
	}
}
