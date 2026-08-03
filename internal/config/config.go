package config

import (
	"os"
	"strconv"
)

type Config struct {
	DataDir       string
	WebDir        string
	Port          string
	OwnerPassword string
	MaxUploadMB   int64
	MaxBackupMB   int64
	ThumbWorkers  int
	BaseURL       string
}

func FromEnv() Config {
	return Config{
		DataDir:       env("FILEAMENT_DATA_DIR", "/data"),
		WebDir:        env("FILEAMENT_WEB_DIR", "web/dist"),
		Port:          env("FILEAMENT_PORT", "8080"),
		OwnerPassword: os.Getenv("FILEAMENT_OWNER_PASSWORD"),
		MaxUploadMB:   envInt64("FILEAMENT_MAX_UPLOAD_MB", 2048),
		MaxBackupMB:   envInt64("FILEAMENT_MAX_BACKUP_MB", 8192),
		ThumbWorkers:  int(envInt64("FILEAMENT_THUMB_WORKERS", 2)),
		BaseURL:       os.Getenv("FILEAMENT_BASE_URL"),
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt64(key string, fallback int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}
