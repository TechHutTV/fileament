package mesh

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestParseASCIISTL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "part.stl")
	if err := os.WriteFile(path, []byte(`solid p
facet normal 0 0 1
outer loop
vertex 0 0 0
vertex 10 0 0
vertex 0 20 0
endloop
endfacet
endsolid p`), 0o644); err != nil {
		t.Fatal(err)
	}
	stats, tris, err := ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Format != "stl" || stats.TriangleCount != 1 || stats.BBoxX != 10 || stats.BBoxY != 20 || stats.BBoxZ != 0 {
		t.Fatalf("unexpected stats %#v", stats)
	}
	if len(tris) != 1 {
		t.Fatalf("triangles = %d", len(tris))
	}
}

func TestParseOBJ(t *testing.T) {
	path := filepath.Join(t.TempDir(), "part.obj")
	if err := os.WriteFile(path, []byte("v 0 0 0\nv 1 0 0\nv 0 2 0\nv 0 0 3\nf 1 2 3 4\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stats, tris, err := ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Format != "obj" || stats.TriangleCount != 2 || stats.BBoxZ != 3 {
		t.Fatalf("unexpected stats %#v", stats)
	}
	if len(tris) != 2 {
		t.Fatalf("triangles = %d", len(tris))
	}
}

func TestParse3MF(t *testing.T) {
	path := filepath.Join(t.TempDir(), "part.3mf")
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, _ := zw.Create("3D/3dmodel.model")
	_, _ = w.Write([]byte(`<model><resources><object id="1"><mesh><vertices>
<vertex x="0" y="0" z="0"/><vertex x="5" y="0" z="0"/><vertex x="0" y="6" z="0"/>
</vertices><triangles><triangle v1="0" v2="1" v3="2"/></triangles></mesh></object></resources></model>`))
	_ = zw.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	stats, tris, err := ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Format != "3mf" || stats.TriangleCount != 1 || stats.BBoxX != 5 || stats.BBoxY != 6 {
		t.Fatalf("unexpected stats %#v", stats)
	}
	if len(tris) != 1 {
		t.Fatalf("triangles = %d", len(tris))
	}
}
