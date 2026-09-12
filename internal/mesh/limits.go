package mesh

import (
	"context"
	"errors"
	"io"
	"math"
	"time"
)

const maxParserDuration = 30 * time.Second

var parserSlots = make(chan struct{}, 2)

type parserLimits struct {
	triangles, vertices, instances, depth, entries int
	expandedBytes                                  int64
}

func defaultParserLimits() parserLimits {
	return parserLimits{triangles: 1_000_000, vertices: 2_000_000, instances: 100_000, depth: 64, entries: 2048, expandedBytes: 64 << 20}
}

var errMeshLimit = errors.New("mesh exceeds processing limits")

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func finite(value float64) bool {
	return !math.IsNaN(value) && !math.IsInf(value, 0)
}

func validPoint(v Vec3) bool {
	return finite(v.X) && finite(v.Y) && finite(v.Z) && math.Abs(v.X) <= 1e12 && math.Abs(v.Y) <= 1e12 && math.Abs(v.Z) <= 1e12
}
