package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/TechHutTV/fileament/internal/config"
	"github.com/TechHutTV/fileament/internal/server"
)

func main() {
	cfg := config.FromEnv()
	app, err := newApp(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer app.Close()

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           app.Router(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Printf("fileament listening on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal(err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
}

func newApp(cfg config.Config) (*server.App, error) {
	tempDir, err := filepath.Abs(filepath.Join(cfg.DataDir, "tmp"))
	if err != nil {
		return nil, err
	}
	// SQLite snapshots the environment on first use; configure it before opening any database.
	if err := os.Setenv("SQLITE_TMPDIR", tempDir); err != nil {
		return nil, err
	}
	webFS, err := webFilesystem(cfg)
	if err != nil {
		return nil, err
	}
	return server.New(cfg, webFS)
}
