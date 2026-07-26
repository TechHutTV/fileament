package mesh

import (
	"archive/zip"
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/xml"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	for _, f := range zr.File {
		if strings.HasSuffix(strings.ToLower(f.Name), ".model") {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			tris, err := parse3MFModel(rc)
			_ = rc.Close()
			return tris, err
		}
	}
	return nil, errors.New("3mf model payload missing")
}

func parse3MFModel(r io.Reader) ([]Triangle, error) {
	dec := xml.NewDecoder(r)
	var verts []Vec3
	var tris []Triangle
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return tris, nil
		}
		if err != nil {
			return nil, err
		}
		el, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch el.Name.Local {
		case "vertex":
			var v Vec3
			for _, a := range el.Attr {
				switch a.Name.Local {
				case "x":
					var err error
					v.X, err = strconv.ParseFloat(a.Value, 64)
					if err != nil {
						return nil, err
					}
				case "y":
					var err error
					v.Y, err = strconv.ParseFloat(a.Value, 64)
					if err != nil {
						return nil, err
					}
				case "z":
					var err error
					v.Z, err = strconv.ParseFloat(a.Value, 64)
					if err != nil {
						return nil, err
					}
				}
			}
			verts = append(verts, v)
		case "triangle":
			var idx [3]int
			seen := [3]bool{}
			for _, a := range el.Attr {
				n, err := strconv.Atoi(a.Value)
				if err != nil {
					return nil, err
				}
				switch a.Name.Local {
				case "v1":
					idx[0] = n
					seen[0] = true
				case "v2":
					idx[1] = n
					seen[1] = true
				case "v3":
					idx[2] = n
					seen[2] = true
				}
			}
			for i, n := range idx {
				if !seen[i] || n < 0 || n >= len(verts) {
					return nil, errors.New("3mf triangle index out of range")
				}
			}
			tris = append(tris, Triangle{A: verts[idx[0]], B: verts[idx[1]], C: verts[idx[2]]})
		}
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
