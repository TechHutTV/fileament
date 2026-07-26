package mesh

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/qmuntal/go3mf"
)

const maxParserBytes int64 = 512 << 20

type Vec3 struct {
	X float64 `json:"x"`
	Y float64 `json:"y"`
	Z float64 `json:"z"`
}

type Triangle struct {
	A Vec3 `json:"a"`
	B Vec3 `json:"b"`
	C Vec3 `json:"c"`
}

type Stats struct {
	Format        string  `json:"format"`
	TriangleCount int     `json:"triangleCount"`
	BBoxX         float64 `json:"bboxX"`
	BBoxY         float64 `json:"bboxY"`
	BBoxZ         float64 `json:"bboxZ"`
}

func ParseFile(path string) (Stats, []Triangle, error) {
	if st, err := os.Stat(path); err != nil {
		return Stats{}, nil, err
	} else if st.Size() <= 0 {
		return Stats{}, nil, errors.New("mesh is empty")
	} else if st.Size() > maxParserBytes {
		return Stats{}, nil, errors.New("mesh exceeds parser size limit")
	}
	var format string
	var tris []Triangle
	var err error
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")) {
	case "stl":
		format = "stl"
		tris, err = parseSTL(path)
	case "obj":
		format = "obj"
		tris, err = parseOBJ(path)
	case "3mf":
		format = "3mf"
		tris, err = parse3MF(path)
	default:
		return Stats{}, nil, errors.New("unsupported mesh format")
	}
	if err != nil {
		return Stats{}, nil, err
	}
	if len(tris) == 0 {
		return Stats{}, nil, errors.New("mesh contains no triangles")
	}
	return stats(format, tris), tris, nil
}

func stats(format string, tris []Triangle) Stats {
	if len(tris) == 0 {
		return Stats{Format: format}
	}
	min := Vec3{math.MaxFloat64, math.MaxFloat64, math.MaxFloat64}
	max := Vec3{-math.MaxFloat64, -math.MaxFloat64, -math.MaxFloat64}
	for _, tri := range tris {
		for _, v := range []Vec3{tri.A, tri.B, tri.C} {
			min.X = math.Min(min.X, v.X)
			min.Y = math.Min(min.Y, v.Y)
			min.Z = math.Min(min.Z, v.Z)
			max.X = math.Max(max.X, v.X)
			max.Y = math.Max(max.Y, v.Y)
			max.Z = math.Max(max.Z, v.Z)
		}
	}
	return Stats{Format: format, TriangleCount: len(tris), BBoxX: max.X - min.X, BBoxY: max.Y - min.Y, BBoxZ: max.Z - min.Z}
}

func parseSTL(path string) ([]Triangle, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) >= 84 {
		n := binary.LittleEndian.Uint32(data[80:84])
		want := int64(84) + int64(n)*50
		if want == int64(len(data)) {
			tris := make([]Triangle, 0, n)
			off := 84
			for i := uint32(0); i < n; i++ {
				if off+50 > len(data) {
					return nil, errors.New("truncated binary stl")
				}
				off += 12
				a := readVec(data[off:])
				off += 12
				b := readVec(data[off:])
				off += 12
				c := readVec(data[off:])
				off += 14
				tris = append(tris, Triangle{A: a, B: b, C: c})
			}
			return tris, nil
		} else if n > 0 && want > int64(len(data)) && !bytes.HasPrefix(bytes.TrimSpace(data[:min(len(data), 80)]), []byte("solid")) {
			return nil, errors.New("truncated binary stl")
		}
	}
	var tris []Triangle
	var verts []Vec3
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 4 && strings.EqualFold(fields[0], "vertex") {
			v, err := parseVec(fields[1], fields[2], fields[3])
			if err != nil {
				return nil, err
			}
			verts = append(verts, v)
			if len(verts) == 3 {
				tris = append(tris, Triangle{A: verts[0], B: verts[1], C: verts[2]})
				verts = nil
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if len(verts) != 0 {
		return nil, errors.New("incomplete ascii stl facet")
	}
	return tris, nil
}

func readVec(b []byte) Vec3 {
	return Vec3{
		X: float64(math.Float32frombits(binary.LittleEndian.Uint32(b[0:4]))),
		Y: float64(math.Float32frombits(binary.LittleEndian.Uint32(b[4:8]))),
		Z: float64(math.Float32frombits(binary.LittleEndian.Uint32(b[8:12]))),
	}
}

func parseOBJ(path string) ([]Triangle, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var verts []Vec3
	var tris []Triangle
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "v":
			if len(fields) < 4 {
				continue
			}
			v, err := parseVec(fields[1], fields[2], fields[3])
			if err != nil {
				return nil, err
			}
			verts = append(verts, v)
		case "f":
			if len(fields) < 4 {
				continue
			}
			var idx []int
			for _, field := range fields[1:] {
				head := strings.Split(field, "/")[0]
				n, err := strconv.Atoi(head)
				if err != nil {
					return nil, err
				}
				if n < 0 {
					n = len(verts) + 1 + n
				}
				if n <= 0 || n > len(verts) {
					return nil, errors.New("obj face index out of range")
				}
				idx = append(idx, n-1)
			}
			for i := 1; i+1 < len(idx); i++ {
				tris = append(tris, Triangle{A: verts[idx[0]], B: verts[idx[i]], C: verts[idx[i+1]]})
			}
		}
	}
	return tris, sc.Err()
}

func parse3MF(path string) ([]Triangle, error) {
	r, err := go3mf.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	model := new(go3mf.Model)
	if err := r.Decode(model); err != nil {
		return nil, err
	}
	scale := unitScale(model.Units)
	var tris []Triangle
	active := map[string]bool{}
	var appendObject func(string, uint32, go3mf.Matrix) error
	appendObject = func(objectPath string, objectID uint32, transform go3mf.Matrix) error {
		key := fmt.Sprintf("%s#%d", objectPath, objectID)
		if active[key] {
			return errors.New("3mf component cycle")
		}
		active[key] = true
		defer delete(active, key)
		object, ok := model.FindObject(objectPath, objectID)
		if !ok {
			return fmt.Errorf("3mf object %d not found", objectID)
		}
		transform = matrixOrIdentity(transform)
		if object.Mesh != nil {
			for _, triangle := range object.Mesh.Triangles {
				i1, i2, i3 := triangle.Indices()
				if i1 >= uint32(len(object.Mesh.Vertices)) || i2 >= uint32(len(object.Mesh.Vertices)) || i3 >= uint32(len(object.Mesh.Vertices)) {
					return errors.New("3mf triangle index out of range")
				}
				tris = append(tris, Triangle{
					A: transformedPoint(object.Mesh.Vertices[i1], transform, scale),
					B: transformedPoint(object.Mesh.Vertices[i2], transform, scale),
					C: transformedPoint(object.Mesh.Vertices[i3], transform, scale),
				})
			}
		}
		for _, component := range object.Components {
			componentTransform := matrixOrIdentity(component.Transform)
			if err := appendObject(component.ObjectPath(objectPath), component.ObjectID, transform.Mul(componentTransform)); err != nil {
				return err
			}
		}
		return nil
	}
	if len(model.Build.Items) == 0 {
		for _, object := range model.Resources.Objects {
			if object.Mesh != nil {
				if err := appendObject("", object.ID, go3mf.Identity()); err != nil {
					return nil, err
				}
			}
		}
		return tris, nil
	}
	for _, item := range model.Build.Items {
		if err := appendObject(item.ObjectPath(), item.ObjectID, matrixOrIdentity(item.Transform)); err != nil {
			return nil, err
		}
	}
	return tris, nil
}

func matrixOrIdentity(matrix go3mf.Matrix) go3mf.Matrix {
	if matrix == (go3mf.Matrix{}) {
		return go3mf.Identity()
	}
	return matrix
}

func transformedPoint(point go3mf.Point3D, transform go3mf.Matrix, scale float64) Vec3 {
	point = transform.Mul3D(point)
	return Vec3{X: float64(point.X()) * scale, Y: float64(point.Y()) * scale, Z: float64(point.Z()) * scale}
}

func unitScale(units go3mf.Units) float64 {
	switch units {
	case go3mf.UnitMicrometer:
		return 0.001
	case go3mf.UnitCentimeter:
		return 10
	case go3mf.UnitInch:
		return 25.4
	case go3mf.UnitFoot:
		return 304.8
	case go3mf.UnitMeter:
		return 1000
	default:
		return 1
	}
}

func parseVec(x, y, z string) (Vec3, error) {
	xf, err := strconv.ParseFloat(x, 64)
	if err != nil {
		return Vec3{}, err
	}
	yf, err := strconv.ParseFloat(y, 64)
	if err != nil {
		return Vec3{}, err
	}
	zf, err := strconv.ParseFloat(z, 64)
	if err != nil {
		return Vec3{}, err
	}
	return Vec3{X: xf, Y: yf, Z: zf}, nil
}
