package storage

import (
	"errors"
	"io/fs"
	"os"
)

// ValidateLayout runs before recovery can change existing storage.
func ValidateLayout(root *os.Root) error {
	return fs.WalkDir(root.FS(), ".", func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == "tmp" {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !entry.IsDir() && !entry.Type().IsRegular() {
			return errors.New("storage contains a symbolic link or special file; restore regular files and directories before restarting")
		}
		return nil
	})
}

func EnsureLayout(root *os.Root) error {
	if err := root.RemoveAll("tmp"); err != nil {
		return err
	}
	for _, dir := range []string{"tmp", "models"} {
		if err := root.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}
