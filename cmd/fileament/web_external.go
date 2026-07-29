//go:build !embedded_ui

package main

import (
	"fmt"
	"io/fs"
	"os"

	"github.com/TechHutTV/fileament/internal/config"
)

func webFilesystem(cfg config.Config) (fs.FS, error) {
	if cfg.WebDir == "" {
		return nil, fmt.Errorf("web directory is required")
	}
	return os.DirFS(cfg.WebDir), nil
}
