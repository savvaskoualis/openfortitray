package tray

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"

	"fyne.io/fyne/v2"
)

// badgeRed is the fill of the "update available" dot. A saturated red that reads
// as an alert on any of the four state-coloured base icons.
var badgeRed = color.RGBA{R: 0xE0, G: 0x2B, B: 0x20, A: 0xFF}

// badgedResource decodes a base icon PNG, overlays a red "update available" dot
// in the bottom-right quadrant, and returns the result as a fyne resource named
// name. The compose is pure Go (image/draw + image/png), so it is cross-platform
// and adds no committed assets. If the base cannot be decoded/re-encoded — it
// never should, these are our own embedded PNGs — it falls back to the plain
// icon so the menu item still signals the update.
func badgedResource(name string, base []byte) fyne.Resource {
	data, err := composeBadge(base)
	if err != nil {
		return fyne.NewStaticResource(name, base)
	}
	return fyne.NewStaticResource(name, data)
}

// composeBadge draws a filled red circle in the bottom-right quadrant of the
// decoded base image, ringed by a thin fully-transparent gap so the dot reads on
// any background, and re-encodes the result as PNG. Stdlib only.
func composeBadge(base []byte) ([]byte, error) {
	src, _, err := image.Decode(bytes.NewReader(base))
	if err != nil {
		return nil, err
	}
	b := src.Bounds()
	dst := image.NewRGBA(b)
	draw.Draw(dst, b, src, b.Min, draw.Src)

	w, h := b.Dx(), b.Dy()
	minDim := min(w, h)

	// Dot diameter ~38% of the icon's shorter side; a thin transparent gap ring
	// around it (≈1/16 of the shorter side, at least 1px) separates the dot from
	// whatever colour sits underneath it.
	radius := 0.19 * float64(minDim) // 0.19*2 = 0.38 diameter
	gap := math.Max(1, float64(minDim)/16)
	outer := radius + gap

	// Centre the dot near the bottom-right corner, keeping the transparent ring
	// fully inside the image bounds.
	cx := float64(b.Max.X) - outer
	cy := float64(b.Max.Y) - outer

	r2 := radius * radius
	o2 := outer * outer
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			dx := float64(x) + 0.5 - cx
			dy := float64(y) + 0.5 - cy
			d2 := dx*dx + dy*dy
			switch {
			case d2 <= r2:
				dst.SetRGBA(x, y, badgeRed)
			case d2 <= o2:
				// Transparent gap ring: punch a hole so the red dot never blends
				// into the base icon's colour.
				dst.SetRGBA(x, y, color.RGBA{})
			}
		}
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
