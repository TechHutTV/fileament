package server

import (
	"math"
	"testing"

	"github.com/TechHutTV/fileament/internal/config"
)

func TestNewNormalizesRuntimeLimits(t *testing.T) {
	app := newTestAppWithConfig(t, config.Config{DataDir: t.TempDir(), MaxUploadMB: math.MaxInt64, MaxBackupMB: -1, ThumbWorkers: 33})
	if app.cfg.MaxUploadMB != 2048 || app.cfg.MaxBackupMB != 8192 || app.cfg.ThumbWorkers != 2 {
		t.Fatalf("unchecked runtime limits: upload=%d backup=%d workers=%d", app.cfg.MaxUploadMB, app.cfg.MaxBackupMB, app.cfg.ThumbWorkers)
	}
}

func TestZeroWorkersFromEnvRemainsDisabled(t *testing.T) {
	t.Setenv("FILEAMENT_THUMB_WORKERS", "0")
	cfg := config.FromEnv()
	cfg.DataDir = t.TempDir()
	cfg.WebDir = testWebDir(t)
	cfg.OwnerPassword = ""
	app := newTestAppWithConfig(t, cfg)
	if app.cfg.ThumbWorkers != 0 || app.workerCancel != nil {
		t.Fatal("environment configuration started workers when zero was requested")
	}
}
