package mesh

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
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
	return ParseFileContext(context.Background(), path)
}

type FileInspection struct {
	Stats
	SizeBytes int64
	SHA256    string
}

// InspectFileContext validates and hashes a mesh without retaining expanded triangles.
func InspectFileContext(ctx context.Context, path string) (FileInspection, error) {
	info, _, err := parseAdmittedFile(ctx, path, false)
	return info, err
}

func ParseFileContext(ctx context.Context, path string) (Stats, []Triangle, error) {
	info, tris, err := parseAdmittedFile(ctx, path, true)
	return info.Stats, tris, err
}

func parseAdmittedFile(ctx context.Context, path string, retain bool) (FileInspection, []Triangle, error) {
	ctx, cancel := context.WithTimeout(ctx, maxParserDuration)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return FileInspection{}, nil, err
	}
	select {
	case parserSlots <- struct{}{}:
		defer func() { <-parserSlots }()
	case <-ctx.Done():
		return FileInspection{}, nil, ctx.Err()
	}
	return readMeshFile(ctx, path, defaultParserLimits(), retain)
}

func parseFileWithLimits(ctx context.Context, path string, limits parserLimits) (Stats, []Triangle, error) {
	info, tris, err := readMeshFile(ctx, path, limits, true)
	return info.Stats, tris, err
}

func readMeshFile(ctx context.Context, path string, limits parserLimits, retain bool) (FileInspection, []Triangle, error) {
	if st, err := os.Lstat(path); err != nil {
		return FileInspection{}, nil, err
	} else if !st.Mode().IsRegular() {
		return FileInspection{}, nil, errors.New("mesh must be a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return FileInspection{}, nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return FileInspection{}, nil, err
	} else if !st.Mode().IsRegular() || st.Size() <= 0 {
		return FileInspection{}, nil, errors.New("mesh is empty")
	} else if st.Size() > maxParserBytes {
		return FileInspection{}, nil, errors.New("mesh exceeds parser size limit")
	}
	var format string
	collector := triangleCollector{retain: retain, limit: limits.triangles}
	hasher := sha256.New()
	var r io.Reader = contextReader{ctx: ctx, r: io.NewSectionReader(f, 0, st.Size())}
	if !retain {
		r = io.TeeReader(r, hasher)
	}
	switch strings.ToLower(strings.TrimPrefix(filepath.Ext(path), ".")) {
	case "stl":
		format = "stl"
		err = parseSTL(ctx, bufio.NewReaderSize(r, 64<<10), st.Size(), limits, &collector)
	case "obj":
		format = "obj"
		err = parseOBJ(ctx, r, limits, &collector)
	case "3mf":
		format = "3mf"
		err = parse3MF(ctx, f, st.Size(), limits, &collector)
		if err == nil && !retain {
			_, err = io.Copy(io.Discard, r)
		}
	default:
		return FileInspection{}, nil, errors.New("unsupported mesh format")
	}
	if err != nil {
		return FileInspection{}, nil, err
	}
	if collector.count == 0 {
		return FileInspection{}, nil, errors.New("mesh contains no triangles")
	}
	result := collector.stats(format)
	if err := ctx.Err(); err != nil {
		return FileInspection{}, nil, err
	}
	if !finite(result.BBoxX) || !finite(result.BBoxY) || !finite(result.BBoxZ) {
		return FileInspection{}, nil, errors.New("invalid mesh bounds")
	}
	info := FileInspection{Stats: result, SizeBytes: st.Size()}
	if !retain {
		info.SHA256 = hex.EncodeToString(hasher.Sum(nil))
	}
	return info, collector.triangles, nil
}

func parseSTL(ctx context.Context, r io.Reader, size int64, limits parserLimits, collector *triangleCollector) error {
	header := make([]byte, 84)
	read, err := io.ReadFull(r, header)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return err
	}
	header = header[:read]
	if len(header) == 84 {
		n := binary.LittleEndian.Uint32(header[80:84])
		want := int64(84) + int64(n)*50
		if want == size {
			if uint64(n) > uint64(limits.triangles) {
				return errMeshLimit
			}
			if collector.retain {
				collector.triangles = make([]Triangle, 0, n)
			}
			var facet [50]byte
			for i := uint32(0); i < n; i++ {
				if err := ctx.Err(); err != nil {
					return err
				}
				if _, err := io.ReadFull(r, facet[:]); err != nil {
					return err
				}
				err = collector.add(Triangle{A: readVec(facet[12:]), B: readVec(facet[24:]), C: readVec(facet[36:])})
				if err != nil {
					return err
				}
			}
			return nil
		} else if n > 0 && want > size && !bytes.HasPrefix(bytes.TrimSpace(header[:80]), []byte("solid")) {
			return errors.New("truncated binary stl")
		}
	}
	var verts [3]Vec3
	vertexCount := 0
	sc := bufio.NewScanner(io.MultiReader(bytes.NewReader(header), r))
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		fields := strings.Fields(sc.Text())
		if len(fields) == 4 && strings.EqualFold(fields[0], "vertex") {
			v, err := parseVec(fields[1], fields[2], fields[3])
			if err != nil {
				return err
			}
			verts[vertexCount] = v
			vertexCount++
			if vertexCount == 3 {
				err = collector.add(Triangle{A: verts[0], B: verts[1], C: verts[2]})
				if err != nil {
					return err
				}
				vertexCount = 0
			}
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if vertexCount != 0 {
		return errors.New("incomplete ascii stl facet")
	}
	return nil
}

func readVec(b []byte) Vec3 {
	return Vec3{
		X: float64(math.Float32frombits(binary.LittleEndian.Uint32(b[0:4]))),
		Y: float64(math.Float32frombits(binary.LittleEndian.Uint32(b[4:8]))),
		Z: float64(math.Float32frombits(binary.LittleEndian.Uint32(b[8:12]))),
	}
}

func parseOBJ(ctx context.Context, r io.Reader, limits parserLimits, collector *triangleCollector) error {
	var verts []Vec3
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "v":
			if len(verts) >= limits.vertices {
				return errMeshLimit
			}
			if len(fields) < 4 {
				continue
			}
			v, err := parseVec(fields[1], fields[2], fields[3])
			if err != nil {
				return err
			}
			verts = append(verts, v)
		case "f":
			if len(fields) < 4 {
				continue
			}
			if len(fields)-3 > limits.triangles-collector.count {
				return errMeshLimit
			}
			first, previous := 0, 0
			for i, field := range fields[1:] {
				head, _, _ := strings.Cut(field, "/")
				n, err := strconv.Atoi(head)
				if err != nil {
					return err
				}
				if n < 0 {
					n = len(verts) + 1 + n
				}
				if n <= 0 || n > len(verts) {
					return errors.New("obj face index out of range")
				}
				index := n - 1
				if i == 0 {
					first = index
				} else if i >= 2 {
					if err := collector.add(Triangle{A: verts[first], B: verts[previous], C: verts[index]}); err != nil {
						return err
					}
				}
				previous = index
			}
		}
	}
	return sc.Err()
}

func parse3MF(ctx context.Context, r io.ReaderAt, size int64, limits parserLimits, collector *triangleCollector) error {
	model, err := decode3MF(ctx, r, size, limits)
	if err != nil {
		return err
	}
	scale := unitScale(model.Units)
	active := map[string]bool{}
	instances := 0
	var appendObject func(string, uint32, go3mf.Matrix, int) error
	appendObject = func(objectPath string, objectID uint32, transform go3mf.Matrix, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		instances++
		if instances > limits.instances || depth > limits.depth {
			return errMeshLimit
		}
		for _, value := range transform {
			if !finite(float64(value)) {
				return errors.New("invalid 3mf transform")
			}
		}
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
			if len(object.Mesh.Triangles) > limits.triangles-collector.count {
				return errMeshLimit
			}
			for _, triangle := range object.Mesh.Triangles {
				if err := ctx.Err(); err != nil {
					return err
				}
				i1, i2, i3 := triangle.Indices()
				if i1 >= uint32(len(object.Mesh.Vertices)) || i2 >= uint32(len(object.Mesh.Vertices)) || i3 >= uint32(len(object.Mesh.Vertices)) {
					return errors.New("3mf triangle index out of range")
				}
				err = collector.add(Triangle{
					A: transformedPoint(object.Mesh.Vertices[i1], transform, scale),
					B: transformedPoint(object.Mesh.Vertices[i2], transform, scale),
					C: transformedPoint(object.Mesh.Vertices[i3], transform, scale),
				})
				if err != nil {
					return err
				}
			}
		}
		for _, component := range object.Components {
			componentTransform := matrixOrIdentity(component.Transform)
			if err := appendObject(component.ObjectPath(objectPath), component.ObjectID, transform.Mul(componentTransform), depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if len(model.Build.Items) == 0 {
		for _, object := range model.Resources.Objects {
			if object.Mesh != nil {
				if err := appendObject("", object.ID, go3mf.Identity(), 1); err != nil {
					return err
				}
			}
		}
		return nil
	}
	for _, item := range model.Build.Items {
		if err := appendObject(item.ObjectPath(), item.ObjectID, matrixOrIdentity(item.Transform), 1); err != nil {
			return err
		}
	}
	return nil
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
	v := Vec3{X: xf, Y: yf, Z: zf}
	if !validPoint(v) {
		return Vec3{}, errors.New("invalid mesh coordinates")
	}
	return v, nil
}
