package server

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/TechHutTV/fileament/internal/config"
	"github.com/TechHutTV/fileament/internal/ids"
)

func BenchmarkMeshIngestion(b *testing.B) {
	const triangles = 100_000
	binarySTL := make([]byte, 84+50*triangles)
	binary.LittleEndian.PutUint32(binarySTL[80:], triangles)
	for i := range triangles {
		facet := binarySTL[84+50*i:]
		binary.LittleEndian.PutUint32(facet[24:], math.Float32bits(1))
		binary.LittleEndian.PutUint32(facet[40:], math.Float32bits(2))
	}
	for name, data := range map[string][]byte{
		"binary.stl": binarySTL,
		"ascii.stl":  []byte("solid p\n" + strings.Repeat("vertex 0 0 0\nvertex 1 0 0\nvertex 0 2 0\n", triangles) + "endsolid p\n"),
		"part.obj":   []byte("v 0 0 0\nv 1 0 0\nv 0 2 0\n" + strings.Repeat("f 1 2 3\n", triangles)),
	} {
		b.Run(name, func(b *testing.B) {
			path := filepath.Join(b.TempDir(), name)
			if err := os.WriteFile(path, data, 0o600); err != nil {
				b.Fatal(err)
			}
			app := &App{}
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			for b.Loop() {
				file, err := app.buildFileRecord(context.Background(), "model", path, "files/"+name, 0)
				if err != nil || file.TriangleCount != triangles || file.BBoxX != 1 || file.BBoxY != 2 {
					b.Fatalf("stats=%+v error=%v", file, err)
				}
			}
		})
	}
}

func BenchmarkSidecarStartup(b *testing.B) {
	for _, count := range []int{1000, 10000} {
		b.Run(fmt.Sprint(count), func(b *testing.B) {
			cfg := config.Config{DataDir: b.TempDir(), MaxUploadMB: 32, ThumbWorkers: 0}
			web := fstest.MapFS{"index.html": &fstest.MapFile{Data: []byte("benchmark")}}
			app, err := New(cfg, web)
			if err != nil {
				b.Fatal(err)
			}
			payload := []byte(validSTL())
			sum := sha256.Sum256(payload)
			modelIDs := make([]string, count)
			for i := range count {
				id := ids.New()
				modelIDs[i] = id
				model := Model{ID: id, Title: fmt.Sprintf("Model %06d", i), Description: "Sidecar startup fixture", CreatedAt: int64(i + 1), UpdatedAt: int64(i + 1), Tags: []string{"fixtures", "parts", "tools"}, Files: []ModelFile{}}
				root := filepath.Join(cfg.DataDir, "models", id)
				if err := os.MkdirAll(filepath.Join(root, "files"), 0o755); err != nil {
					b.Fatal(err)
				}
				for j := range 2 {
					name := fmt.Sprintf("part%d.stl", j)
					if err := os.WriteFile(filepath.Join(root, "files", name), payload, 0o600); err != nil {
						b.Fatal(err)
					}
					model.Files = append(model.Files, ModelFile{ID: ids.New(), ModelID: id, Filename: name, RelPath: "files/" + name, Format: "stl", SizeBytes: int64(len(payload)), SHA256: hex.EncodeToString(sum[:]), TriangleCount: 1, BBoxX: 1, BBoxY: 1, SortOrder: j})
					model.TotalBytes += int64(len(payload))
				}
				data, err := json.Marshal(model)
				if err != nil {
					b.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "model.json"), data, 0o600); err != nil {
					b.Fatal(err)
				}
			}
			collections := []Collection{}
			for i := 0; i < count; i += 100 {
				collections = append(collections, Collection{ID: ids.New(), Name: fmt.Sprintf("Collection %06d", i), Slug: fmt.Sprintf("collection-%d", i), CreatedAt: 1, ModelIDs: modelIDs[i:min(i+100, count)]})
			}
			data, err := json.Marshal(collections)
			if err != nil {
				b.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(cfg.DataDir, "collections.json"), data, 0o600); err != nil {
				b.Fatal(err)
			}
			if err := app.rebuildFromSidecars(); err != nil {
				b.Fatal(err)
			}
			if err := app.rebuildCollectionsFromSidecar(); err != nil {
				b.Fatal(err)
			}
			if err := app.Close(); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			for b.Loop() {
				reopened, err := New(cfg, web)
				if err != nil {
					b.Fatal(err)
				}
				var changes int64
				if reopened.db.Stats().OpenConnections != 1 {
					b.Fatal("change count needs one connection")
				}
				if err := reopened.db.QueryRow(`SELECT total_changes()`).Scan(&changes); err != nil {
					b.Fatal(err)
				}
				b.ReportMetric(float64(changes), "sqlite-changes/op")
				if err := reopened.Close(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
