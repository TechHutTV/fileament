package mesh

import (
	"errors"
	"math"
)

type triangleCollector struct {
	retain    bool
	limit     int
	count     int
	min, max  Vec3
	triangles []Triangle
}

func (c *triangleCollector) add(tri Triangle) error {
	if c.count >= c.limit {
		return errMeshLimit
	}
	if !validPoint(tri.A) || !validPoint(tri.B) || !validPoint(tri.C) {
		return errors.New("invalid mesh coordinates")
	}
	if c.count == 0 {
		c.min, c.max = tri.A, tri.A
	}
	for _, v := range [3]Vec3{tri.A, tri.B, tri.C} {
		c.min.X = math.Min(c.min.X, v.X)
		c.min.Y = math.Min(c.min.Y, v.Y)
		c.min.Z = math.Min(c.min.Z, v.Z)
		c.max.X = math.Max(c.max.X, v.X)
		c.max.Y = math.Max(c.max.Y, v.Y)
		c.max.Z = math.Max(c.max.Z, v.Z)
	}
	c.count++
	if c.retain {
		c.triangles = append(c.triangles, tri)
	}
	return nil
}

func (c *triangleCollector) stats(format string) Stats {
	return Stats{Format: format, TriangleCount: c.count, BBoxX: c.max.X - c.min.X, BBoxY: c.max.Y - c.min.Y, BBoxZ: c.max.Z - c.min.Z}
}
