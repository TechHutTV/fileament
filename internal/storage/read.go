package storage

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// OpenRegular opens a slash-separated path without accepting symlink aliases.
// The caller retains ownership of root and must close the returned file.
func OpenRegular(root *os.Root, name string) (*os.File, error) {
	if name == "." || strings.ContainsAny(name, "\\\x00") {
		return nil, fs.ErrInvalid
	}
	local, err := filepath.Localize(name)
	if err != nil {
		return nil, err
	}
	parent := root
	defer func() {
		if parent != root {
			_ = parent.Close()
		}
	}()
	parts := strings.Split(local, string(filepath.Separator))
	for _, part := range parts[:len(parts)-1] {
		before, err := parent.Lstat(part)
		if err != nil {
			return nil, err
		}
		if !before.IsDir() {
			return nil, fs.ErrInvalid
		}
		next, err := parent.OpenRoot(part)
		if err != nil {
			return nil, err
		}
		opened, err := next.Stat(".")
		if err != nil || !os.SameFile(before, opened) {
			_ = next.Close()
			return nil, fs.ErrInvalid
		}
		if parent != root {
			_ = parent.Close()
		}
		parent = next
	}
	base := parts[len(parts)-1]
	before, err := parent.Lstat(base)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fs.ErrInvalid
	}
	// Nonblocking open also handles a regular file replaced by a FIFO.
	file, err := parent.OpenFile(base, os.O_RDONLY|nonblockReadFlag, 0)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		_ = file.Close()
		return nil, fs.ErrInvalid
	}
	return file, nil
}
