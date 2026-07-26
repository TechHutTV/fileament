package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/TechHutTV/fileament/internal/config"
	"github.com/TechHutTV/fileament/internal/server"
)

func main() {
	cfg := config.FromEnv()
	app, err := server.New(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer app.Close()

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           app.Router(),
		ReadHeaderTimeout: 10 * time.Second,
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
