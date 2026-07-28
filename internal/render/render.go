package render

import (
	"image"
	"image/color"
	"image/png"
	"math"
	"os"

	"github.com/TechHutTV/fileament/internal/mesh"
)

type point struct {
	x, y, z float64
}

func RenderPNG(tris []mesh.Triangle, path string, size int) error {
	if size <= 0 {
		size = 512
	}
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	if len(tris) == 0 {
		return encodePNG(path, img)
	}
	drawShadow(img)
	center, scale := bounds(tris)
	zbuf := make([]float64, size*size)
	for i := range zbuf {
		zbuf[i] = math.Inf(-1)
	}
	key := normalize(point{x: -0.45, y: -0.55, z: 1})
	fill := normalize(point{x: 0.7, y: 0.1, z: 0.65})
	for _, tri := range tris {
		av := view(norm(tri.A, center, scale))
		bv := view(norm(tri.B, center, scale))
		cv := view(norm(tri.C, center, scale))
		n := normal(av, bv, cv)
		if n.z <= 0 {
			continue
		}
		n = normalize(n)
		shade := 0.2 + 0.58*math.Max(0, dot(n, key)) + 0.18*math.Max(0, dot(n, fill)) + 0.08*math.Pow(1-math.Abs(n.z), 2)
		fillTriangle(img, zbuf, project(av, size), project(bv, size), project(cv, size), math.Min(1, shade))
	}
	return encodePNG(path, img)
}

func drawShadow(img *image.RGBA) {
	cx, cy := float64(img.Bounds().Dx())*0.5, float64(img.Bounds().Dy())*0.73
	rx, ry := float64(img.Bounds().Dx())*0.3, float64(img.Bounds().Dy())*0.065
	for y := int(cy - ry); y <= int(cy+ry); y++ {
		for x := int(cx - rx); x <= int(cx+rx); x++ {
			dx, dy := (float64(x)-cx)/rx, (float64(y)-cy)/ry
			q := dx*dx + dy*dy
			if q >= 1 {
				continue
			}
			alpha := 0.12 * math.Pow(1-q, 2)
			img.Set(x, y, color.NRGBA{
				R: 42,
				G: 57,
				B: 51,
				A: uint8(alpha * 255),
			})
		}
	}
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

func view(v mesh.Vec3) point {
	az := math.Pi / 4
	el := math.Pi / 6
	x := v.X*math.Cos(az) - v.Y*math.Sin(az)
	y0 := v.X*math.Sin(az) + v.Y*math.Cos(az)
	y := y0*math.Sin(el) - v.Z*math.Cos(el)
	z := y0*math.Cos(el) + v.Z*math.Sin(el)
	return point{x: x, y: y, z: z}
}

func project(v point, size int) point {
	margin := float64(size) * 0.14
	scale := float64(size)/2 - margin
	return point{x: float64(size)/2 + v.x*scale, y: float64(size)/2 + v.y*scale, z: v.z}
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
	col := color.RGBA{
		R: uint8(24 + 75*shade),
		G: uint8(64 + 110*shade),
		B: uint8(55 + 95*shade),
		A: 255,
	}
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

func encodePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}
