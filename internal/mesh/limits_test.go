package mesh

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/qmuntal/go3mf"
)

func TestRejectsNonFiniteMeshCoordinates(t *testing.T) {
	for _, value := range []string{"NaN", "+Inf", "-Inf"} {
		for _, format := range []string{"obj", "stl"} {
			t.Run(format+"/"+value, func(t *testing.T) {
				body := "v " + value + " 0 0\nv 1 0 0\nv 0 1 0\nf 1 2 3\n"
				if format == "stl" {
					body = "solid p\nvertex " + value + " 0 0\nvertex 1 0 0\nvertex 0 1 0\nendsolid p\n"
				}
				if _, _, err := ParseFile(writeLimitMesh(t, "part."+format, []byte(body))); err == nil {
					t.Fatal("parser accepted a non-finite coordinate")
				}
			})
		}
	}
	data := make([]byte, 134)
	binary.LittleEndian.PutUint32(data[80:], 1)
	binary.LittleEndian.PutUint32(data[96:], math.Float32bits(float32(math.NaN())))
	if _, _, err := ParseFile(writeLimitMesh(t, "binary.stl", data)); err == nil {
		t.Fatal("binary STL accepted a non-finite coordinate")
	}
}

func TestGeometryLimits(t *testing.T) {
	obj := "v 0 0 0\nv 1 0 0\nv 0 1 0\nv 0 0 1\nf 1 2 3 4\n"
	ascii := "solid p\n" + strings.Repeat("vertex 0 0 0\nvertex 1 0 0\nvertex 0 1 0\n", 2) + "endsolid p\n"
	binarySTL := make([]byte, 184)
	binary.LittleEndian.PutUint32(binarySTL[80:], 2)
	for name, data := range map[string][]byte{"part.obj": []byte(obj), "ascii.stl": []byte(ascii), "binary.stl": binarySTL} {
		t.Run(name, func(t *testing.T) {
			path := writeLimitMesh(t, name, data)
			limits := defaultParserLimits()
			limits.triangles = 2
			if _, tris, err := parseFileWithLimits(context.Background(), path, limits); err != nil || len(tris) != 2 {
				t.Fatalf("boundary parse: triangles=%d err=%v", len(tris), err)
			}
			limits.triangles = 1
			if _, _, err := parseFileWithLimits(context.Background(), path, limits); !errors.Is(err, errMeshLimit) {
				t.Fatalf("triangle overflow = %v", err)
			}
		})
	}
	limits := defaultParserLimits()
	limits.vertices = 3
	if _, _, err := parseFileWithLimits(context.Background(), writeLimitMesh(t, "vertices.obj", []byte(obj)), limits); !errors.Is(err, errMeshLimit) {
		t.Fatalf("vertex overflow = %v", err)
	}
	for _, format := range []string{"obj", "stl"} {
		if _, _, err := ParseFile(writeLimitMesh(t, "line."+format, []byte(strings.Repeat("x", (1<<20)+1)))); err == nil {
			t.Fatal("accepted oversized line")
		}
	}
}

func Test3MFArchiveLimits(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(map[string]string, *parserLimits)
	}{
		{"expanded attachment", func(parts map[string]string, limits *parserLimits) {
			parts["Metadata/padding.bin"] = strings.Repeat("0", 32<<10)
			limits.expandedBytes = 16 << 10
		}},
		{"entry count", func(parts map[string]string, limits *parserLimits) { limits.entries = 2 }},
		{"XML depth", func(parts map[string]string, limits *parserLimits) {
			parts["Metadata/deep.xml"] = strings.Repeat("<a>", 65) + strings.Repeat("</a>", 65)
		}},
		{"vertices", func(parts map[string]string, limits *parserLimits) { limits.vertices = 2 }},
		{"traversal", func(parts map[string]string, limits *parserLimits) { parts["../model.xml"] = "<a/>" }},
		{"duplicate path case", func(parts map[string]string, limits *parserLimits) { parts["3D/PART.MODEL"] = parts["3D/part.model"] }},
		{"multiple XML roots", func(parts map[string]string, limits *parserLimits) { parts["Metadata/meta.xml"] = "<a/><b/>" }},
		{"invalid content types", func(parts map[string]string, limits *parserLimits) { parts["[Content_Types].xml"] = "<a/>" }},
		{"invalid transform", func(parts map[string]string, limits *parserLimits) {
			parts["3D/part.model"] = strings.Replace(parts["3D/part.model"], "objectid=\"1\"", "objectid=\"1\" transform=\"1 0 0 0 1 0 0 0 1 NaN 0 0\"", 1)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parts, limits := limit3MFParts(), defaultParserLimits()
			tc.change(parts, &limits)
			if _, _, err := parseFileWithLimits(context.Background(), writeLimitArchive(t, parts, zip.Deflate), limits); err == nil {
				t.Fatal("accepted invalid package")
			}
		})
	}
	parts := limit3MFParts()
	parts["Metadata/attachment.bin"] = strings.Repeat("0", 5<<20)
	if _, _, err := ParseFile(writeLimitArchive(t, parts, zip.Store)); err != nil {
		t.Fatalf("valid attachment larger than metadata budget: %v", err)
	}
}

func TestMultipart3MFPreservesTransformsAndUnits(t *testing.T) {
	parts := limit3MFParts()
	parts["3D/child.model"] = strings.ReplaceAll(parts["3D/part.model"], "id=\"1\"", "id=\"2\"")
	parts["3D/part.model"] = `<model unit="inch" xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02" xmlns:p="http://schemas.microsoft.com/3dmanufacturing/production/2015/06"><resources><object id="1"><components><component objectid="2" p:path="/3D/child.model" transform="1 0 0 0 1 0 0 0 1 10 0 0"/></components></object></resources><build><item objectid="1"/></build></model>`
	parts["3D/_rels/part.model.rels"] = limitRelationship("child.model")
	stats, tris, err := ParseFile(writeLimitArchive(t, parts, zip.Deflate))
	if err != nil || len(tris) != 1 || math.Abs(stats.BBoxX-25.4) > 1e-9 || tris[0].A.X != 254 {
		t.Fatalf("multipart result: stats=%+v triangles=%+v err=%v", stats, tris, err)
	}
}

func TestParserCancellationAndAdmission(t *testing.T) {
	path := writeLimitMesh(t, "part.obj", []byte("v 0 0 0\nv 1 0 0\nv 0 1 0\nf 1 2 3\n"))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := ParseFileContext(ctx, path); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled parse = %v", err)
	}
	for i := 0; i < cap(parserSlots); i++ {
		parserSlots <- struct{}{}
	}
	defer func() {
		for i := 0; i < cap(parserSlots); i++ {
			<-parserSlots
		}
	}()
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, _, err := ParseFileContext(ctx, path); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued parse = %v", err)
	}
}

func TestParserRejectsNonRegularFiles(t *testing.T) {
	directory := t.TempDir()
	if _, _, err := ParseFile(directory); err == nil {
		t.Fatal("accepted a directory")
	}
	path := writeLimitMesh(t, "part.obj", []byte("v 0 0 0\nv 1 0 0\nv 0 1 0\nf 1 2 3\n"))
	link := filepath.Join(directory, "link.obj")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ParseFile(link); err == nil {
		t.Fatal("accepted a symbolic link")
	}
}

func limit3MFParts() map[string]string {
	return map[string]string{
		"[Content_Types].xml": `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="model" ContentType="application/vnd.ms-package.3dmanufacturing-3dmodel+xml"/></Types>`,
		"_rels/.rels":         limitRelationship("/3D/part.model"),
		"3D/part.model":       `<model unit="millimeter" xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02"><resources><object id="1"><mesh><vertices><vertex x="0" y="0" z="0"/><vertex x="1" y="0" z="0"/><vertex x="0" y="1" z="0"/></vertices><triangles><triangle v1="0" v2="1" v3="2"/></triangles></mesh></object></resources><build><item objectid="1"/></build></model>`,
	}
}

func limitRelationship(target string) string {
	return fmt.Sprintf(`<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="r" Type="http://schemas.microsoft.com/3dmanufacturing/2013/01/3dmodel" Target="%s"/></Relationships>`, target)
}

func writeLimitArchive(t *testing.T, parts map[string]string, method uint16) string {
	t.Helper()
	var buffer bytes.Buffer
	w := zip.NewWriter(&buffer)
	for name, data := range parts {
		entry, err := w.CreateHeader(&zip.FileHeader{Name: name, Method: method})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return writeLimitMesh(t, "part.3mf", buffer.Bytes())
}

func TestRejectsDeep3MFComponents(t *testing.T) {
	model := componentLimitModel(80, false)
	if _, _, err := ParseFile(writeLimit3MF(t, model)); err == nil {
		t.Fatal("parser accepted excessive component depth")
	}
}

func TestRejectsAmplified3MFComponents(t *testing.T) {
	model := componentLimitModel(16, true)
	if _, _, err := ParseFile(writeLimit3MF(t, model)); err == nil {
		t.Fatal("parser accepted excessive component instances")
	}
}

func TestRejectsNonFinite3MFTransform(t *testing.T) {
	model := componentLimitModel(0, false)
	model.Build.Items[0].Transform = go3mf.Identity().Translate(float32(math.Inf(1)), 0, 0)
	if _, _, err := ParseFile(writeLimit3MF(t, model)); err == nil {
		t.Fatal("parser accepted a non-finite transform")
	}
}

func componentLimitModel(depth int, double bool) *go3mf.Model {
	objects := []*go3mf.Object{{ID: 1, Mesh: &go3mf.Mesh{
		Vertices: []go3mf.Point3D{{0, 0, 0}, {1, 0, 0}, {0, 1, 0}}, Triangles: []go3mf.Triangle{go3mf.NewTriangle(0, 1, 2)},
	}}}
	for id := uint32(2); id <= uint32(depth+1); id++ {
		components := []*go3mf.Component{{ObjectID: id - 1, Transform: go3mf.Identity()}}
		if double {
			components = append(components, &go3mf.Component{ObjectID: id - 1, Transform: go3mf.Identity()})
		}
		objects = append(objects, &go3mf.Object{ID: id, Components: components})
	}
	return &go3mf.Model{Resources: go3mf.Resources{Objects: objects}, Build: go3mf.Build{Items: []*go3mf.Item{{ObjectID: uint32(depth + 1), Transform: go3mf.Identity()}}}}
}

func writeLimit3MF(t *testing.T, model *go3mf.Model) string {
	t.Helper()
	var buffer bytes.Buffer
	if err := go3mf.NewEncoder(&buffer).Encode(model); err != nil {
		t.Fatal(err)
	}
	return writeLimitMesh(t, "part.3mf", buffer.Bytes())
}

func writeLimitMesh(t *testing.T, name string, contents []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if strings.ContainsAny(name, `/\`) {
		t.Fatal("test filename must be a basename")
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}
