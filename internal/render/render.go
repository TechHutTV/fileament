package render

import (
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"os"

	"github.com/TechHutTV/fileament/internal/mesh"
)

type point struct {
	x, y, z float64
}

func RenderJPEG(tris []mesh.Triangle, path string, size int) error {
	if size <= 0 {
		size = 512
	}
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	bg := color.RGBA{R: 246, G: 246, B: 242, A: 255}
	for i := range img.Pix {
		if i%4 == 0 {
			img.Pix[i] = bg.R
			img.Pix[i+1] = bg.G
			img.Pix[i+2] = bg.B
			img.Pix[i+3] = 255
		}
	}
	if len(tris) == 0 {
		return encode(path, img)
	}
	center, scale := bounds(tris)
	zbuf := make([]float64, size*size)
	for i := range zbuf {
		zbuf[i] = math.Inf(-1)
	}
	for _, tri := range tris {
		a := project(norm(tri.A, center, scale), size)
		b := project(norm(tri.B, center, scale), size)
		c := project(norm(tri.C, center, scale), size)
		n := normal(a, b, c)
		if n.z <= 0 {
			continue
		}
		light := normalize(point{x: -0.4, y: -0.5, z: 1})
		shade := 0.25 + 0.75*math.Max(0, dot(normalize(n), light))
		fillTriangle(img, zbuf, a, b, c, shade)
	}
	return encode(path, img)
}

func bounds(tris []mesh.Triangle) (mesh.Vec3, float64) {
	min := mesh.Vec3{X: math.MaxFloat64, Y: math.MaxFloat64, Z: math.MaxFloat64}
	max := mesh.Vec3{X: -math.MaxFloat64, Y: -math.MaxFloat64, Z: -math.MaxFloat64}
	for _, tri := range tris {
		for _, v := range []mesh.Vec3{tri.A, tri.B, tri.C} {
			min.X = math.Min(min.X, v.X)
			min.Y = math.Min(min.Y, v.Y)
			min.Z = math.Min(min.Z, v.Z)
			max.X = math.Max(max.X, v.X)
			max.Y = math.Max(max.Y, v.Y)
			max.Z = math.Max(max.Z, v.Z)
		}
	}
	span := math.Max(max.X-min.X, math.Max(max.Y-min.Y, max.Z-min.Z))
	if span == 0 {
		span = 1
	}
	return mesh.Vec3{X: (min.X + max.X) / 2, Y: (min.Y + max.Y) / 2, Z: (min.Z + max.Z) / 2}, 1.5 / span
}

func norm(v, c mesh.Vec3, s float64) mesh.Vec3 {
	return mesh.Vec3{X: (v.X - c.X) * s, Y: (v.Y - c.Y) * s, Z: (v.Z - c.Z) * s}
}

func project(v mesh.Vec3, size int) point {
	az := math.Pi / 4
	el := math.Pi / 6
	x := v.X*math.Cos(az) - v.Y*math.Sin(az)
	y0 := v.X*math.Sin(az) + v.Y*math.Cos(az)
	y := y0*math.Sin(el) - v.Z*math.Cos(el)
	z := y0*math.Cos(el) + v.Z*math.Sin(el)
	margin := float64(size) * 0.18
	scale := float64(size)/2 - margin
	return point{x: float64(size)/2 + x*scale, y: float64(size)/2 + y*scale, z: z}
}

func fillTriangle(img *image.RGBA, zbuf []float64, a, b, c point, shade float64) {
	minX := int(math.Max(0, math.Floor(math.Min(a.x, math.Min(b.x, c.x)))))
	maxX := int(math.Min(float64(img.Bounds().Dx()-1), math.Ceil(math.Max(a.x, math.Max(b.x, c.x)))))
	minY := int(math.Max(0, math.Floor(math.Min(a.y, math.Min(b.y, c.y)))))
	maxY := int(math.Min(float64(img.Bounds().Dy()-1), math.Ceil(math.Max(a.y, math.Max(b.y, c.y)))))
	den := (b.y-c.y)*(a.x-c.x) + (c.x-b.x)*(a.y-c.y)
	if den == 0 {
		return
	}
	level := uint8(90 + 130*shade)
	col := color.RGBA{R: level, G: level, B: level, A: 255}
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			px, py := float64(x)+0.5, float64(y)+0.5
			w1 := ((b.y-c.y)*(px-c.x) + (c.x-b.x)*(py-c.y)) / den
			w2 := ((c.y-a.y)*(px-c.x) + (a.x-c.x)*(py-c.y)) / den
			w3 := 1 - w1 - w2
			if w1 < 0 || w2 < 0 || w3 < 0 {
				continue
			}
			z := w1*a.z + w2*b.z + w3*c.z
			i := y*img.Bounds().Dx() + x
			if z > zbuf[i] {
				zbuf[i] = z
				img.SetRGBA(x, y, col)
			}
		}
	}
}

func normal(a, b, c point) point {
	u := point{x: b.x - a.x, y: b.y - a.y, z: b.z - a.z}
	v := point{x: c.x - a.x, y: c.y - a.y, z: c.z - a.z}
	return point{x: u.y*v.z - u.z*v.y, y: u.z*v.x - u.x*v.z, z: u.x*v.y - u.y*v.x}
}

func normalize(p point) point {
	l := math.Sqrt(p.x*p.x + p.y*p.y + p.z*p.z)
	if l == 0 {
		return p
	}
	return point{x: p.x / l, y: p.y / l, z: p.z / l}
}

func dot(a, b point) float64 {
	return a.x*b.x + a.y*b.y + a.z*b.z
}

func encode(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return jpeg.Encode(f, img, &jpeg.Options{Quality: 85})
}
