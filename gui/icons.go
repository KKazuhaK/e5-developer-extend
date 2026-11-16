package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"math"
	"sync"

	"github.com/KKazuhaK/e5-renew/gui/trayhost"
)

// The tray icons are drawn in code: a round badge in a state colour. On
// macOS a template icon (black, with alpha) is used so the system tints it.

const iconSize = 64

var (
	iconOnce  sync.Once
	iconCache map[trayhost.Icon][]byte
	template  []byte
)

func buildIcons() {
	iconCache = map[trayhost.Icon][]byte{
		trayhost.IconOK:   drawIcon(color.RGBA{0x2e, 0xa0, 0x43, 0xff}, false),
		trayhost.IconWarn: drawIcon(color.RGBA{0xf2, 0x9b, 0x0c, 0xff}, true),
		trayhost.IconIdle: drawIcon(color.RGBA{0x8a, 0x8f, 0x98, 0xff}, false),
	}
	template = drawIcon(color.RGBA{0, 0, 0, 0xff}, false)
}

// iconPNG returns the PNG of the icon for state.
func iconPNG(i trayhost.Icon) []byte {
	iconOnce.Do(buildIcons)
	return iconCache[i]
}

// templatePNG returns the single-colour icon for macOS.
func templatePNG() []byte {
	iconOnce.Do(buildIcons)
	return template
}

// drawIcon draws an anti-aliased disc; with mark set, a white exclamation mark on it.
func drawIcon(c color.RGBA, mark bool) []byte {
	img := image.NewRGBA(image.Rect(0, 0, iconSize, iconSize))
	const r = iconSize/2 - 3
	cx, cy := float64(iconSize)/2, float64(iconSize)/2
	for y := 0; y < iconSize; y++ {
		for x := 0; x < iconSize; x++ {
			d := math.Hypot(float64(x)+.5-cx, float64(y)+.5-cy)
			a := clamp(float64(r) - d + .5)
			if a <= 0 {
				continue
			}
			img.SetRGBA(x, y, color.RGBA{c.R, c.G, c.B, uint8(a * 255)})
		}
	}
	if mark {
		white := color.RGBA{255, 255, 255, 255}
		for y := 16; y < 38; y++ {
			for x := 29; x < 35; x++ {
				img.SetRGBA(x, y, white)
			}
		}
		for y := 42; y < 48; y++ {
			for x := 29; x < 35; x++ {
				img.SetRGBA(x, y, white)
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err) // encoding into memory cannot fail
	}
	return buf.Bytes()
}

func clamp(v float64) float64 { return math.Max(0, math.Min(1, v)) }
