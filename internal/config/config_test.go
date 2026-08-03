package config

import "testing"

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
