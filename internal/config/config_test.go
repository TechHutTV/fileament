package config

import (
	"math"
	"strconv"
	"testing"
)

func TestBackupSizeConfiguration(t *testing.T) {
	t.Setenv("FILEAMENT_MAX_BACKUP_MB", "")
	if got := FromEnv().MaxBackupMB; got != 8192 {
		t.Fatalf("default MaxBackupMB=%d want=8192", got)
	}
	t.Setenv("FILEAMENT_MAX_BACKUP_MB", "16384")
	if got := FromEnv().MaxBackupMB; got != 16384 {
		t.Fatalf("configured MaxBackupMB=%d want=16384", got)
	}
}

func TestRuntimeLimitsFromEnv(t *testing.T) {
	for _, tc := range []struct {
		value          string
		workers        int
		upload, backup int64
	}{
		{"", 2, 2048, 8192},
		{"0", 0, 2048, 8192},
		{"1", 1, 1, 1},
		{"32", 32, 32, 32},
		{"33", 2, 33, 33},
		{"-1", 2, 2048, 8192},
		{"invalid", 2, 2048, 8192},
		{"1.5", 2, 2048, 8192},
		{"8796093022206", 2, 8796093022206, 8796093022206},
		{"8796093022207", 2, 2048, 8192},
		{strconv.FormatInt(math.MaxInt64, 10), 2, 2048, 8192},
		{"18446744073709551615", 2, 2048, 8192},
	} {
		t.Run(tc.value, func(t *testing.T) {
			for _, key := range []string{"FILEAMENT_THUMB_WORKERS", "FILEAMENT_MAX_UPLOAD_MB", "FILEAMENT_MAX_BACKUP_MB"} {
				t.Setenv(key, tc.value)
			}
			cfg := FromEnv()
			if cfg.ThumbWorkers != tc.workers || cfg.MaxUploadMB != tc.upload || cfg.MaxBackupMB != tc.backup {
				t.Fatalf("limits: workers=%d upload=%d backup=%d; want %d %d %d", cfg.ThumbWorkers, cfg.MaxUploadMB, cfg.MaxBackupMB, tc.workers, tc.upload, tc.backup)
			}
			for _, mb := range []int64{cfg.MaxUploadMB, cfg.MaxBackupMB} {
				if bodyLimit := (mb << 20) + (1 << 20); bodyLimit <= 0 || bodyLimit < mb<<20 {
					t.Fatal("configured body limit overflows with multipart overhead")
				}
			}
		})
	}
}
