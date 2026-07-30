//go:build embedded_ui

package main

import (
	"embed"
	"io/fs"

	"github.com/TechHutTV/fileament/internal/config"
)

//go:embed dist
var embeddedWeb embed.FS

func webFilesystem(config.Config) (fs.FS, error) {
	return fs.Sub(embeddedWeb, "dist")
}
