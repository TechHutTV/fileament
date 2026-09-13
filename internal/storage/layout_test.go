package storage

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateLayoutRejectsNestedSymlinks(t *testing.T) {
	for _, name := range []string{"models/model/files/payload.stl", "models/model/thumbs", "collections.json", "fileament.db-wal", ".restore/staging", ".restore/rollback/token/model", ".mutations/mutation/state.json", "backups/archive.fileament"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("missing", path); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if err := ValidateLayout(root); err == nil {
				t.Fatal("accepted a nested storage symlink")
			}
			if _, err := os.Lstat(path); err != nil {
				t.Fatalf("validation changed storage: %v", err)
			}
		})
	}
}

func TestEnsureLayoutClearsTemporaryLinksWithoutFollowingThem(t *testing.T) {
	for _, nested := range []bool{false, true} {
		t.Run(fmt.Sprint(nested), func(t *testing.T) {
			dir, outside := t.TempDir(), t.TempDir()
			marker := filepath.Join(outside, "sentinel")
			contents := []byte("preserved")
			if err := os.WriteFile(marker, contents, 0o600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "tmp")
			if nested {
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
				path = filepath.Join(path, "link")
			}
			if err := os.Symlink(outside, path); err != nil {
				t.Fatal(err)
			}
			root, err := os.OpenRoot(dir)
			if err != nil {
				t.Fatal(err)
			}
			defer root.Close()
			if err := ValidateLayout(root); err != nil {
				t.Fatal(err)
			}
			if err := EnsureLayout(root); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(marker)
			if err != nil || !bytes.Equal(got, contents) {
				t.Fatalf("temporary cleanup changed outside data: %v", err)
			}
			entries, err := os.ReadDir(filepath.Join(dir, "tmp"))
			if err != nil || len(entries) != 0 {
				t.Fatalf("temporary storage is not empty: %v", err)
			}
		})
	}
}

func BenchmarkValidateLayout(b *testing.B) {
	dir := b.TempDir()
	for i := range 1000 {
		model := filepath.Join(dir, "models", fmt.Sprint(i))
		for _, name := range []string{"model.json", "files/part.stl", "thumbs/card.png"} {
			path := filepath.Join(model, name)
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				b.Fatal(err)
			}
			if err := os.WriteFile(path, nil, 0o600); err != nil {
				b.Fatal(err)
			}
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		b.Fatal(err)
	}
	defer root.Close()
	b.ReportAllocs()
	for b.Loop() {
		if err := ValidateLayout(root); err != nil {
			b.Fatal(err)
		}
	}
}
