package server

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/TechHutTV/fileament/internal/ids"
)

func TestStartupRejectsSymlinkCatalogWithoutPruningIndex(t *testing.T) {
	for _, component := range []string{"models", "model", "sidecar", "database"} {
		t.Run(component, func(t *testing.T) {
			app := newAuthedTestApp(t)
			cookie := loginCookie(t, app, "password-password")
			model := uploadSTLModel(t, app, cookie, "part.stl", "Preserved model")
			if err := app.Close(); err != nil {
				t.Fatal(err)
			}
			rel := map[string]string{"models": "models", "model": "models/" + model.ID, "sidecar": "models/" + model.ID + "/model.json", "database": "fileament.db"}[component]
			path := filepath.Join(app.cfg.DataDir, rel)
			saved := filepath.Join(t.TempDir(), "original")
			if err := os.Rename(path, saved); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(saved, path); err != nil {
				t.Fatal(err)
			}
			restarted, err := New(app.cfg, app.webFS)
			if err == nil {
				_ = restarted.Close()
				t.Error("startup accepted symlinked catalog storage")
			}
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(saved, path); err != nil {
				t.Fatal(err)
			}
			db, err := openDatabase(app.cfg.DataDir)
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			var count int
			if err := db.QueryRow(`SELECT COUNT(*) FROM models WHERE id = ?`, model.ID).Scan(&count); err != nil || count != 1 {
				t.Fatalf("unsafe startup pruned the model index: count=%d, err=%v", count, err)
			}
		})
	}
}

func TestStartupRejectsSymlinkRecoveryWorkspaceBeforeCleanup(t *testing.T) {
	for _, component := range []string{".restore", ".mutations"} {
		t.Run(component, func(t *testing.T) {
			app := newAuthedTestApp(t)
			if err := app.Close(); err != nil {
				t.Fatal(err)
			}
			outside := t.TempDir()
			nested := "staging"
			if component == ".mutations" {
				nested = "preparing-" + ids.New()
			}
			marker := filepath.Join(outside, nested, "sentinel")
			if err := os.MkdirAll(filepath.Dir(marker), 0o700); err != nil {
				t.Fatal(err)
			}
			contents := []byte("outside data must remain unchanged")
			if err := os.WriteFile(marker, contents, 0o600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(app.cfg.DataDir, component)
			if err := os.RemoveAll(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, path); err != nil {
				t.Fatal(err)
			}
			restarted, err := New(app.cfg, app.webFS)
			if err == nil {
				_ = restarted.Close()
				t.Error("startup accepted a symlinked recovery workspace")
			}
			got, err := os.ReadFile(marker)
			if err != nil || !bytes.Equal(got, contents) {
				t.Fatalf("startup cleanup changed outside data: %v", err)
			}
		})
	}
}
