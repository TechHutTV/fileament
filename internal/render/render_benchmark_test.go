package render

import (
	"math"
	"path/filepath"
	"testing"

	"github.com/TechHutTV/fileament/internal/mesh"
)

func BenchmarkThumbnailGrid(b *testing.B) {
	const cells = 100
	tris := make([]mesh.Triangle, 0, cells*cells*2)
	vertex := func(x, y int) mesh.Vec3 {
		return mesh.Vec3{X: float64(x), Y: float64(y), Z: 5 * math.Sin(float64(x)/10) * math.Cos(float64(y)/10)}
	}
	for x := range cells {
		for y := range cells {
			a, c := vertex(x, y), vertex(x+1, y+1)
			tris = append(tris, mesh.Triangle{A: a, B: vertex(x+1, y), C: c}, mesh.Triangle{A: a, B: c, C: vertex(x, y+1)})
		}
	}
	path := filepath.Join(b.TempDir(), "thumb.png")
	b.ReportAllocs()
	for b.Loop() {
		if err := RenderPNG(tris, path, 512); err != nil {
			b.Fatal(err)
		}
	}
}
