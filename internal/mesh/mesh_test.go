package mesh

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/qmuntal/go3mf"
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
	model := &go3mf.Model{
		Resources: go3mf.Resources{Objects: []*go3mf.Object{
			{ID: 1, Mesh: &go3mf.Mesh{Vertices: []go3mf.Point3D{{0, 0, 0}, {5, 0, 0}, {0, 6, 0}}, Triangles: []go3mf.Triangle{go3mf.NewTriangle(0, 1, 2)}}},
			{ID: 2, Mesh: &go3mf.Mesh{Vertices: []go3mf.Point3D{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}}, Triangles: []go3mf.Triangle{go3mf.NewTriangle(0, 1, 2)}}},
		}},
		Build: go3mf.Build{Items: []*go3mf.Item{
			{ObjectID: 1, Transform: go3mf.Identity()},
			{ObjectID: 2, Transform: go3mf.Identity().Translate(10, 0, 0)},
		}},
	}
	if err := go3mf.NewEncoder(&buf).Encode(model); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	stats, tris, err := ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Format != "3mf" || stats.TriangleCount != 2 || stats.BBoxX != 11 || stats.BBoxY != 6 {
		t.Fatalf("unexpected stats %#v", stats)
	}
	if len(tris) != 2 || tris[1].A.X != 10 {
		t.Fatalf("triangles = %d", len(tris))
	}
}
