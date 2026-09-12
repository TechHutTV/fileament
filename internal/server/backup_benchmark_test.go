package server

import (
	"archive/zip"
	"context"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"testing/fstest"
	"time"

	"github.com/TechHutTV/fileament/internal/config"
)

func BenchmarkBackupSnapshot(b *testing.B) {
	size := 32 << 20
	if os.Getenv("FILEAMENT_BENCH_LARGE_BACKUP") == "1" {
		size = 2 << 30
	}
	app, err := New(config.Config{DataDir: b.TempDir(), MaxBackupMB: 4096}, fstest.MapFS{"index.html": {Data: []byte("benchmark")}})
	if err != nil {
		b.Fatal(err)
	}
	defer app.Close()
	dir := filepath.Join(app.cfg.DataDir, "models", "benchmark", "files")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		b.Fatal(err)
	}
	block := make([]byte, 2<<20)
	_, _ = rand.New(rand.NewSource(1)).Read(block)
	for i := 0; i < size/len(block); i++ {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%06d.stl", i)), block, 0o600); err != nil {
			b.Fatal(err)
		}
	}
	var initial storageUsage
	if err := initial.measure(context.Background(), app.cfg.DataDir); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	b.SetBytes(int64(size))
	var locked time.Duration
	for i := 0; i < b.N; i++ {
		app.dataMu.Lock()
		start := time.Now()
		snapshot, err := app.captureBackupSnapshot(context.Background())
		locked += time.Since(start)
		app.dataMu.Unlock()
		if err != nil {
			b.Fatal(err)
		}
		path, err := app.archiveBackupSnapshot(context.Background(), snapshot, filepath.Join(app.cfg.DataDir, "tmp", "backups"))
		if err != nil {
			b.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			b.Fatal(err)
		}
		b.ReportMetric(float64(info.Size()), "archive-B")
		b.StopTimer()
		var peak storageUsage
		if err := peak.measure(context.Background(), app.cfg.DataDir); err != nil {
			b.Fatal(err)
		}
		if initial.DiskBytes != nil && peak.DiskBytes != nil {
			b.ReportMetric(float64(*peak.DiskBytes-*initial.DiskBytes), "workspace-disk-B")
		}
		b.StartTimer()
		if err := os.Remove(path); err != nil {
			b.Fatal(err)
		}
		if err := os.RemoveAll(snapshot.root); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(locked.Microseconds())/float64(b.N)/1000, "lock-ms")
}

func BenchmarkBackupCompression(b *testing.B) {
	payload := make([]byte, 16<<20)
	_, _ = rand.New(rand.NewSource(1)).Read(payload)
	for name, method := range map[string]uint16{"deflate": zip.Deflate, "store": zip.Store} {
		b.Run(name, func(b *testing.B) {
			b.SetBytes(int64(len(payload)))
			for b.Loop() {
				zw := zip.NewWriter(io.Discard)
				entry, err := zw.CreateHeader(&zip.FileHeader{Name: "compressed.3mf", Method: method})
				if err != nil {
					b.Fatal(err)
				}
				if _, err := entry.Write(payload); err != nil {
					b.Fatal(err)
				}
				if err := zw.Close(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
