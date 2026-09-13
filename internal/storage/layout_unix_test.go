//go:build unix

package storage

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestValidateLayoutRejectsSpecialFiles(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "fileament.db"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err := ValidateLayout(root); err == nil {
		t.Fatal("accepted a FIFO as database storage")
	}
}
