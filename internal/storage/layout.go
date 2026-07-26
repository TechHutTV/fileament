package storage

import (
	"os"
	"path/filepath"
)

func EnsureLayout(root string) error {
	if err := os.RemoveAll(filepath.Join(root, "tmp")); err != nil {
		return err
	}
	for _, dir := range []string{root, filepath.Join(root, "tmp"), filepath.Join(root, "models")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return nil
}
