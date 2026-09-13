package mesh

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"os"
	"testing"
	"time"
)

func TestInspectionMatchesGeometryAndRawChecksum(t *testing.T) {
	binarySTL := make([]byte, 134)
	binary.LittleEndian.PutUint32(binarySTL[80:], 1)
	binary.LittleEndian.PutUint32(binarySTL[108:], math.Float32bits(1))
	binary.LittleEndian.PutUint32(binarySTL[124:], math.Float32bits(2))
	paths := []string{
		writeLimitMesh(t, "binary.stl", binarySTL),
		writeLimitMesh(t, "ascii.stl", []byte("solid p\nvertex 0 0 0\nvertex 1 0 0\nvertex 0 2 0\nendsolid p\n")),
		writeLimitMesh(t, "part.obj", []byte("v 0 0 0\nv 1 0 0\nv 0 2 0\nf -3/1 -2/2 -1/3\n# final comment is hashed\n")),
		writeLimit3MF(t, componentLimitModel(4, true)),
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			geometryStats, tris, err := ParseFile(path)
			if err != nil {
				t.Fatal(err)
			}
			info, err := InspectFileContext(context.Background(), path)
			if err != nil || info.Stats != geometryStats || info.TriangleCount != len(tris) {
				t.Fatalf("inspection=%+v geometry=%+v error=%v", info, geometryStats, err)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256(data)
			if info.SizeBytes != int64(len(data)) || info.SHA256 != hex.EncodeToString(sum[:]) {
				t.Fatalf("wrong file identity: %+v", info)
			}
			_, retained, err := readMeshFile(context.Background(), path, defaultParserLimits(), false)
			if err != nil || retained != nil {
				t.Fatalf("inspection retained geometry: %d %v", len(retained), err)
			}
		})
	}
}

func TestInspectionEnforcesMeshBudgets(t *testing.T) {
	paths := []string{
		writeLimitMesh(t, "ascii.stl", []byte("solid p\nvertex 0 0 0\nvertex 1 0 0\nvertex 0 1 0\nvertex 0 0 0\nvertex 1 0 0\nvertex 0 1 0\n")),
		writeLimitMesh(t, "part.obj", []byte("v 0 0 0\nv 1 0 0\nv 0 1 0\nv 0 0 1\nf 1 2 3 4\n")),
		writeLimit3MF(t, componentLimitModel(4, true)),
	}
	for _, path := range paths {
		limits := defaultParserLimits()
		limits.triangles = 1
		if _, _, err := readMeshFile(context.Background(), path, limits, false); !errors.Is(err, errMeshLimit) {
			t.Fatalf("inspection exceeded triangle budget: %s %v", path, err)
		}
	}
	for name, data := range map[string][]byte{
		"bad.stl":   []byte("solid p\nvertex NaN 0 0\nvertex 1 0 0\nvertex 0 1 0\n"),
		"short.stl": []byte("solid p\nvertex 0 0 0\nvertex 1 0 0\n"),
		"bad.obj":   []byte("v 0 0 0\nv 1 0 0\nv 0 1 0\nf 1 2 9\n"),
	} {
		if _, err := InspectFileContext(context.Background(), writeLimitMesh(t, name, data)); err == nil {
			t.Fatalf("inspection accepted %s", name)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := InspectFileContext(ctx, paths[0]); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled inspection: %v", err)
	}
	for range cap(parserSlots) {
		parserSlots <- struct{}{}
	}
	defer func() {
		for range cap(parserSlots) {
			<-parserSlots
		}
	}()
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := InspectFileContext(ctx, paths[0]); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("inspection bypassed parser admission: %v", err)
	}
}
