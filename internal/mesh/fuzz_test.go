package mesh

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const maxFuzzMeshBytes = 128 << 10

func FuzzSTL(f *testing.F) {
	f.Add([]byte("solid p\nvertex 0 0 0\nvertex 1 0 0\nvertex 0 1 0\nendsolid p\n"))
	binarySTL := make([]byte, 134)
	binary.LittleEndian.PutUint32(binarySTL[80:], 1)
	binary.LittleEndian.PutUint32(binarySTL[108:], math.Float32bits(1))
	binary.LittleEndian.PutUint32(binarySTL[124:], math.Float32bits(2))
	f.Add(binarySTL)
	f.Add([]byte("solid p\nvertex NaN 0 0\n"))
	f.Fuzz(func(t *testing.T, data []byte) { fuzzMesh(t, "stl", data) })
}

func FuzzOBJ(f *testing.F) {
	f.Add([]byte("v 0 0 0\nv 1 0 0\nv 0 1 0\nf 1 2 3\n"))
	f.Add([]byte("v 0 0 0\nv 1 0 0\nv 0 1 0\nf -3/1 -2/2 -1/3\n"))
	f.Add([]byte("v 0 0 0\nf 0 999999999999999999 -1\n"))
	f.Fuzz(func(t *testing.T, data []byte) { fuzzMesh(t, "obj", data) })
}

func Fuzz3MFArchive(f *testing.F) {
	f.Add(fuzz3MFArchive(f, []byte(limit3MFParts()["3D/part.model"])))
	f.Add([]byte("PK\x03\x04"))
	f.Fuzz(func(t *testing.T, data []byte) { fuzzMesh(t, "3mf", data) })
}

func Fuzz3MFModel(f *testing.F) {
	f.Add([]byte(limit3MFParts()["3D/part.model"]))
	f.Add([]byte(`<model><resources><object id="1"><components><component objectid="1"/></components></object></resources><build><item objectid="1"/></build></model>`))
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > maxFuzzMeshBytes {
			t.Skip()
		}
		fuzzMesh(t, "3mf", fuzz3MFArchive(t, data))
	})
}

func fuzz3MFArchive(t testing.TB, model []byte) []byte {
	t.Helper()
	parts := limit3MFParts()
	parts["3D/part.model"] = string(model)
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for _, name := range []string{"[Content_Types].xml", "_rels/.rels", "3D/part.model"} {
		entry, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(parts[name])); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

func fuzzMesh(t *testing.T, format string, data []byte) {
	t.Helper()
	if len(data) > maxFuzzMeshBytes {
		t.Skip()
	}
	path := filepath.Join(t.TempDir(), "part."+format)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	limits := parserLimits{triangles: 512, vertices: 1024, instances: 64, depth: 16, entries: 16, expandedBytes: 256 << 10}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	geometry, triangles, parseErr := readMeshFile(ctx, path, limits, true)
	inspection, retained, inspectErr := readMeshFile(ctx, path, limits, false)
	if errors.Is(parseErr, context.DeadlineExceeded) || errors.Is(inspectErr, context.DeadlineExceeded) {
		return
	}
	if (parseErr == nil) != (inspectErr == nil) {
		t.Fatalf("geometry/inspection acceptance differs: %v / %v", parseErr, inspectErr)
	}
	if parseErr != nil {
		return
	}
	if geometry.Stats != inspection.Stats || len(retained) != 0 || geometry.TriangleCount != len(triangles) {
		t.Fatalf("inconsistent geometry/inspection: %+v / %+v", geometry, inspection)
	}
	if len(triangles) == 0 || len(triangles) > limits.triangles || geometry.Format != format {
		t.Fatalf("invalid accepted mesh: %+v", geometry)
	}
	for _, triangle := range triangles {
		if !validPoint(triangle.A) || !validPoint(triangle.B) || !validPoint(triangle.C) {
			t.Fatal("accepted invalid mesh coordinates")
		}
	}
	for _, bound := range []float64{geometry.BBoxX, geometry.BBoxY, geometry.BBoxZ} {
		if !finite(bound) || bound < 0 {
			t.Fatal("accepted invalid mesh bounds")
		}
	}
	sum := sha256.Sum256(data)
	if inspection.SizeBytes != int64(len(data)) || inspection.SHA256 != hex.EncodeToString(sum[:]) {
		t.Fatal("inspection changed the file identity")
	}
}
