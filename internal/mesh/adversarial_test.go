package mesh

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

func TestMalformedMeshesReturnErrors(t *testing.T) {
	dir := t.TempDir()
	cases := map[string]string{
		"bad.obj": "v 0 0 0\nf 1 2 3\n",
		"bad.stl": "solid p\nfacet normal 0 0 1\nouter loop\nvertex 0 0 0\nendsolid p\n",
	}
	for name, body := range cases {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, _, err := ParseFile(path); err == nil {
			t.Fatalf("%s parsed without error", name)
		}
	}
}

func TestMalformed3MFIndexReturnsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.3mf")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	w, err := zw.Create("3D/3dmodel.model")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = w.Write([]byte(`<model><resources><object><mesh><vertices><vertex x="0" y="0" z="0"/></vertices><triangles><triangle v1="0" v2="1" v3="2"/></triangles></mesh></object></resources></model>`))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ParseFile(path); err == nil {
		t.Fatal("malformed 3mf parsed without error")
	}
}
