package render

import (
	"bytes"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"

	"github.com/TechHutTV/fileament/internal/mesh"
)

func TestRenderJPEGProducesNonBlankImage(t *testing.T) {
	out := filepath.Join(t.TempDir(), "thumb.jpg")
	tris := []mesh.Triangle{{A: mesh.Vec3{X: 0, Y: 0, Z: 0}, B: mesh.Vec3{X: 10, Y: 0, Z: 0}, C: mesh.Vec3{X: 0, Y: 10, Z: 5}}}
	if err := RenderJPEG(tris, out, 128); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	img, err := jpeg.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	var dark int
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if r+g+b < 3*0xffff {
				dark++
			}
		}
	}
	if dark == 0 {
		t.Fatal("thumbnail is blank")
	}
}
