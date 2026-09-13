package config

import (
	"math"
	"os"
	"strconv"
)

const (
	maxSizeMB       = (math.MaxInt64 - (1 << 20)) >> 20
	maxThumbWorkers = 32
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
		MaxUploadMB:   envInt64("FILEAMENT_MAX_UPLOAD_MB", 2048, 1, maxSizeMB),
		MaxBackupMB:   envInt64("FILEAMENT_MAX_BACKUP_MB", 8192, 1, maxSizeMB),
		ThumbWorkers:  int(envInt64("FILEAMENT_THUMB_WORKERS", 2, 0, maxThumbWorkers)),
		BaseURL:       os.Getenv("FILEAMENT_BASE_URL"),
	}
}

// NormalizeLimits also protects callers that construct Config without FromEnv.
func (c Config) NormalizeLimits() Config {
	c.MaxUploadMB = boundedInt64(c.MaxUploadMB, 2048, 1, maxSizeMB)
	c.MaxBackupMB = boundedInt64(c.MaxBackupMB, 8192, 1, maxSizeMB)
	c.ThumbWorkers = int(boundedInt64(int64(c.ThumbWorkers), 2, 0, maxThumbWorkers))
	return c
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt64(key string, fallback, minimum, maximum int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return fallback
	}
	return boundedInt64(n, fallback, minimum, maximum)
}

func boundedInt64(n, fallback, minimum, maximum int64) int64 {
	if n < minimum || n > maximum {
		return fallback
	}
	return n
}
