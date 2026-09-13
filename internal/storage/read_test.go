package storage

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenRegularRejectsInvalidPathsAndNonFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "nested", "file"), []byte("safe"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	for _, name := range []string{"", ".", "..", "../file", "/file", "nested/../../file", "nested/../nested/file", "nested//file", "nested/file/", "nested\\file", "nested/file\x00", "nested", "missing"} {
		t.Run(name, func(t *testing.T) {
			file, err := OpenRegular(root, name)
			if err == nil {
				_ = file.Close()
				t.Fatal("unsafe path opened")
			}
		})
	}
	file, err := OpenRegular(root, "nested/file")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	body, err := io.ReadAll(file)
	if err != nil || string(body) != "safe" {
		t.Fatalf("ordinary read: %q, %v", body, err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if file, err := OpenRegular(root, "nested/file"); err == nil {
		_ = file.Close()
		t.Fatal("closed storage root accepted a new read")
	}
}
