//go:build embedded_ui

package main

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/TechHutTV/fileament/internal/config"
	"github.com/TechHutTV/fileament/internal/server"
)

func TestEmbeddedUIServesWithoutExternalDirectory(t *testing.T) {
	cfg := config.Config{DataDir: t.TempDir(), WebDir: t.TempDir() + "/missing", MaxUploadMB: 32, ThumbWorkers: 0}
	web, err := webFilesystem(cfg)
	if err != nil {
		t.Fatal(err)
	}
	app, err := server.New(cfg, web)
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	httpServer := httptest.NewServer(app.Router())
	defer httpServer.Close()
	assets, err := fs.Glob(web, "assets/index-*.js")
	if err != nil || len(assets) == 0 {
		t.Fatalf("hashed JavaScript is missing: %v", err)
	}
	for _, path := range []string{"/healthz", "/", "/" + assets[0]} {
		response, err := httpServer.Client().Get(httpServer.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(response.Body)
		closeErr := response.Body.Close()
		if readErr != nil || closeErr != nil {
			t.Fatalf("read=%v close=%v", readErr, closeErr)
		}
		if response.StatusCode != http.StatusOK || len(body) == 0 {
			t.Fatalf("%s: status=%d bytes=%d", path, response.StatusCode, len(body))
		}
		if path == "/" && !strings.Contains(string(body), "/assets/") {
			t.Fatal("index does not reference built assets")
		}
		if path == "/healthz" && !strings.Contains(string(body), `"status":"ok"`) {
			t.Fatal("health check failed")
		}
	}
}
