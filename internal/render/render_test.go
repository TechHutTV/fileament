package render

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/TechHutTV/fileament/internal/mesh"
)

func TestDrawShadowUsesValidTranslucentPixels(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 128, 128))
	drawShadow(img)
	r, g, b, a := img.At(64, 93).RGBA()
	if a == 0 || a == 0xffff {
		t.Fatalf("shadow alpha = %d, want translucent", a)
	}
	if r > a || g > a || b > a {
		t.Fatalf("shadow RGBA is not premultiplied: r=%d g=%d b=%d a=%d", r, g, b, a)
	}
}

func TestRenderPNGProducesTransparentNonBlankImage(t *testing.T) {
	out := filepath.Join(t.TempDir(), "thumb.png")
	tris := []mesh.Triangle{{A: mesh.Vec3{X: 0, Y: 0, Z: 0}, B: mesh.Vec3{X: 10, Y: 0, Z: 0}, C: mesh.Vec3{X: 0, Y: 10, Z: 5}}}
	if err := RenderPNG(tris, out, 128); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	var visible int
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			_, _, _, a := img.At(x, y).RGBA()
			if a > 0 {
				visible++
			}
		}
	}
	if visible == 0 {
		t.Fatal("thumbnail is blank")
	}
	_, _, _, cornerAlpha := img.At(b.Min.X, b.Min.Y).RGBA()
	if cornerAlpha != 0 {
		t.Fatalf("corner alpha = %d, want transparent background", cornerAlpha)
	}
}

func TestRenderPNGUsesDirectionalColorShading(t *testing.T) {
	out := filepath.Join(t.TempDir(), "shaded.png")
	tris := []mesh.Triangle{
		{A: mesh.Vec3{}, B: mesh.Vec3{X: 10}, C: mesh.Vec3{Y: 10}},
		{A: mesh.Vec3{}, B: mesh.Vec3{Z: 10}, C: mesh.Vec3{X: 10}},
		{A: mesh.Vec3{}, B: mesh.Vec3{Y: 10}, C: mesh.Vec3{Z: 10}},
	}
	if err := RenderPNG(tris, out, 128); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(out)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	var tinted, darkest, lightest int
	darkest = 3 * 0xffff
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			r, g, b, _ := img.At(x, y).RGBA()
			if int(g) <= int(r)+1000 || int(g) <= int(b)+1000 {
				continue
			}
			tinted++
			level := int(r + g + b)
			if level < darkest {
				darkest = level
			}
			if level > lightest {
				lightest = level
			}
		}
	}
	if tinted < 100 {
		t.Fatalf("tinted model pixels = %d, want at least 100", tinted)
	}
	if lightest-darkest < 5000 {
		t.Fatalf("shading range = %d, want at least 5000", lightest-darkest)
	}
}
