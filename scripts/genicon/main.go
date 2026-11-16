// Command genicon draws the application icon (packaging/icons/appicon.png):
// a white "renew" arrow on a blue-to-teal rounded square. It uses only the
// standard library so the icon can be regenerated anywhere:
//
//	go run ./scripts/genicon -o packaging/icons/appicon.png
//	go run ./scripts/genicon -size 256 -o packaging/icons/appicon-256.png
//
// The .ico and .icns files are made from this PNG with `wails3 generate icons`
// (scripts/release/icons.sh).
package main

import (
	"flag"
	"image"
	"image/color"
	"image/png"
	"log"
	"math"
	"os"
)

const (
	size = 1024 // the canvas the geometry below is drawn on
	ss   = 4    // supersampling per axis
)

func main() {
	out := flag.String("o", "packaging/icons/appicon.png", "output file")
	px := flag.Int("size", size, "width and height in pixels")
	flag.Parse()
	k := float64(size) / float64(*px)
	img := image.NewNRGBA(image.Rect(0, 0, *px, *px))
	for y := 0; y < *px; y++ {
		for x := 0; x < *px; x++ {
			img.SetNRGBA(x, y, pixel(x, y, k))
		}
	}
	f, err := os.Create(*out)
	if err != nil {
		log.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		log.Fatal(err)
	}
	if err := f.Close(); err != nil {
		log.Fatal(err)
	}
}

// Geometry, in pixels of the 1024 canvas.
const (
	inset   = 64.0  // transparent margin around the tile
	corner  = 200.0 // tile corner radius
	cx, cy  = 512.0, 530.0
	ringR   = 270.0 // centre line of the ring
	ringW   = 92.0  // ring thickness
	dotR    = 62.0
	arcFrom = -58.0 // degrees, y pointing down: -90 is the top
	arcTo   = 232.0
)

var (
	top    = [3]float64{0x25, 0x63, 0xeb} // blue
	bottom = [3]float64{0x0d, 0x94, 0x88} // teal
)

// pixel averages ss×ss samples of the tile and the white glyph; k converts
// output pixels to canvas pixels.
func pixel(px, py int, k float64) color.NRGBA {
	var tile, glyph float64
	for j := 0; j < ss; j++ {
		for i := 0; i < ss; i++ {
			x := (float64(px) + (float64(i)+0.5)/ss) * k
			y := (float64(py) + (float64(j)+0.5)/ss) * k
			if inTile(x, y) {
				tile++
				if inGlyph(x, y) {
					glyph++
				}
			}
		}
	}
	n := float64(ss * ss)
	if tile == 0 {
		return color.NRGBA{}
	}
	t := ((float64(py)+0.5)*k - inset) / (size - 2*inset)
	t = math.Max(0, math.Min(1, t))
	g := glyph / tile // share of the covered area that is white
	var c [3]uint8
	for ch := range c {
		bg := top[ch] + (bottom[ch]-top[ch])*t
		c[ch] = uint8(math.Round(bg + (255-bg)*g))
	}
	return color.NRGBA{c[0], c[1], c[2], uint8(math.Round(255 * tile / n))}
}

func inTile(x, y float64) bool {
	lo, hi := inset+corner, size-inset-corner
	dx := math.Max(0, math.Max(lo-x, x-hi))
	dy := math.Max(0, math.Max(lo-y, y-hi))
	if x < inset || x > size-inset || y < inset || y > size-inset {
		return false
	}
	return dx*dx+dy*dy <= corner*corner
}

func inGlyph(x, y float64) bool {
	dx, dy := x-cx, y-cy
	r := math.Hypot(dx, dy)
	if r <= dotR {
		return true
	}
	if math.Abs(r-ringR) <= ringW/2 && inArc(math.Atan2(dy, dx)*180/math.Pi) {
		return true
	}
	return inArrow(x, y)
}

// inArc reports whether angle a (degrees) lies on the arc, which runs
// clockwise from arcFrom to arcTo.
func inArc(a float64) bool {
	for a < arcFrom {
		a += 360
	}
	return a <= arcTo
}

// inArrow is the arrow head at the end of the arc, pointing clockwise.
func inArrow(x, y float64) bool {
	th := arcTo * math.Pi / 180
	ex, ey := cx+ringR*math.Cos(th), cy+ringR*math.Sin(th) // end of the arc
	tx, ty := -math.Sin(th), math.Cos(th)                  // clockwise tangent
	nx, ny := math.Cos(th), math.Sin(th)                   // outward normal
	half := ringW * 1.15
	length := ringW * 1.45
	a := [2]float64{ex + nx*half, ey + ny*half}
	b := [2]float64{ex - nx*half, ey - ny*half}
	c := [2]float64{ex + tx*length, ey + ty*length}
	return inTriangle(x, y, a, b, c)
}

func inTriangle(x, y float64, a, b, c [2]float64) bool {
	s := func(p, q [2]float64) float64 { return (q[0]-p[0])*(y-p[1]) - (q[1]-p[1])*(x-p[0]) }
	d1, d2, d3 := s(a, b), s(b, c), s(c, a)
	neg := d1 < 0 || d2 < 0 || d3 < 0
	pos := d1 > 0 || d2 > 0 || d3 > 0
	return !(neg && pos)
}
