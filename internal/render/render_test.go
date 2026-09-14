package render

import (
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/TechHutTV/fileament/internal/mesh"
)

func TestRenderLimitsAndCancellationPreserveExistingThumbnail(t *testing.T) {
	path := filepath.Join(t.TempDir(), "thumb.png")
	if err := os.WriteFile(path, []byte("existing thumbnail"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RenderPNG(nil, path, 2049); err == nil {
		t.Fatal("accepted oversized render")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := RenderPNGContext(ctx, nil, path, 128); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled render = %v", err)
	}
	tris := make([]mesh.Triangle, 100_000)
	for i := range tris {
		tris[i] = mesh.Triangle{A: mesh.Vec3{}, B: mesh.Vec3{X: 10}, C: mesh.Vec3{Y: 10}}
	}
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := RenderPNGContext(ctx, tris, path, 512); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("overdraw render = %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "existing thumbnail" {
		t.Fatalf("failed render changed existing output: %q, %v", data, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary output leaked: %v, %v", entries, err)
	}
}

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
