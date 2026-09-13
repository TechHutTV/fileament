//go:build unix

package storage

import (
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestOpenRegularRetainsOpenedDirectoryAndFile(t *testing.T) {
	parent, outside := t.TempDir(), t.TempDir()
	dir := filepath.Join(parent, "data")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{filepath.Join(dir, "file"): "safe", filepath.Join(outside, "file"): "outside"} {
		if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	saved := filepath.Join(parent, "saved")
	if err := os.Rename(dir, saved); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, dir); err != nil {
		t.Fatal(err)
	}
	file, err := OpenRegular(root, "file")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := os.Rename(filepath.Join(saved, "file"), filepath.Join(saved, "original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "file"), filepath.Join(saved, "file")); err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(file)
	if err != nil || string(body) != "safe" {
		t.Fatalf("replacement changed the opened read: %q, %v", body, err)
	}
	if file, err := OpenRegular(root, "file"); err == nil {
		_ = file.Close()
		t.Fatal("subsequent read accepted a substituted symlink")
	}
}

func TestOpenRegularRejectsFIFO(t *testing.T) {
	dir := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(dir, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if file, err := OpenRegular(root, "fifo"); err == nil {
		_ = file.Close()
		t.Fatal("FIFO opened as a regular file")
	}
}

func TestOpenRegularDuringDirectoryReplacement(t *testing.T) {
	dir, outside := t.TempDir(), t.TempDir()
	child := filepath.Join(dir, "child")
	saved := filepath.Join(dir, "saved")
	if err := os.Mkdir(child, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{filepath.Join(child, "file"): "safe", filepath.Join(outside, "file"): "outside"} {
		if err := os.WriteFile(name, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	stop, done := make(chan struct{}), make(chan error, 1)
	go func() {
		for {
			select {
			case <-stop:
				done <- nil
				return
			default:
			}
			if err := os.Rename(child, saved); err != nil {
				done <- err
				return
			}
			if err := os.Symlink(outside, child); err != nil {
				done <- err
				return
			}
			if err := os.Remove(child); err != nil {
				done <- err
				return
			}
			if err := os.Rename(saved, child); err != nil {
				done <- err
				return
			}
		}
	}()
	defer func() {
		close(stop)
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	for range 1000 {
		file, err := OpenRegular(root, "child/file")
		if err != nil {
			continue
		}
		body, readErr := io.ReadAll(file)
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || string(body) != "safe" {
			t.Fatalf("replacement escaped storage: %q, read=%v close=%v", body, readErr, closeErr)
		}
	}
}
