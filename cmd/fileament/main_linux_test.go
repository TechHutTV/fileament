package main

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/TechHutTV/fileament/internal/config"
)

func TestSQLiteTemporaryFilesStayInDataDirectory(t *testing.T) {
	if dataDir := os.Getenv("FILEAMENT_TEMP_TEST_DATA"); dataDir != "" {
		app, err := newApp(config.Config{DataDir: dataDir, WebDir: os.Getenv("FILEAMENT_TEMP_TEST_WEB"), ThumbWorkers: 0})
		if err != nil {
			t.Fatal(err)
		}
		defer app.Close()
		db, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		db.SetMaxOpenConns(1)
		if _, err := db.Exec(`PRAGMA temp_store=FILE; PRAGMA temp.cache_size=1;
			CREATE TEMP TABLE spill(payload BLOB);
			WITH RECURSIVE n(x) AS (VALUES(1) UNION ALL SELECT x+1 FROM n WHERE x<1024)
			INSERT INTO spill SELECT zeroblob(4096) FROM n`); err != nil {
			t.Fatal(err)
		}
		tempDir, err := filepath.Abs(filepath.Join(dataDir, "tmp"))
		if err != nil {
			t.Fatal(err)
		}
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, entry := range entries {
			target, err := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
			if err != nil || !strings.HasPrefix(filepath.Base(target), "etilqs_") {
				continue
			}
			found = true
			if filepath.Dir(target) != tempDir {
				t.Errorf("SQLite temporary file is outside data/tmp: %q", target)
			}
		}
		if !found {
			t.Fatal("SQLite did not spill the temporary table to disk")
		}
		return
	}

	for _, relative := range []bool{false, true} {
		name := "absolute"
		if relative {
			name = "relative"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			web := filepath.Join(root, "web")
			if err := os.Mkdir(web, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(web, "index.html"), []byte("test UI"), 0o600); err != nil {
				t.Fatal(err)
			}
			dataDir := filepath.Join(root, "custom data's directory")
			if relative {
				dataDir = filepath.Base(dataDir)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSQLiteTemporaryFilesStayInDataDirectory$", "-test.v")
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "FILEAMENT_TEMP_TEST_DATA="+dataDir, "FILEAMENT_TEMP_TEST_WEB="+web,
				"SQLITE_TMPDIR="+root, "TMPDIR="+root)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("startup temporary storage: %v\n%s", err, output)
			}
		})
	}
}
