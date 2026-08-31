package tray

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
)

// badgeRed is the fill of the "update available" dot. A saturated red that reads
// as an alert on any of the four state-coloured base icons.
var badgeRed = color.RGBA{R: 0xE0, G: 0x2B, B: 0x20, A: 0xFF}

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

// padToSquare centers src on a transparent square canvas sized to its
// longer side and re-encodes as PNG. The embedded base icons are 45x32 —
// fine under Fyne's tray, which apparently normalized this itself, but
// Qt's QSystemTrayIcon renders a QIcon's native aspect ratio scaled to the
// menu bar's fixed height, so a landscape source icon shows up as a
// visibly widened rectangle instead of the usual square tray glyph.
// Padding to square here, once, at construction time, fixes that without
// touching the artwork or composeBadge's badge-placement math (which
// already sizes relative to the shorter side and is unaffected by the
// added transparent margin).
func padToSquare(src []byte) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	side := max(b.Dx(), b.Dy())
	dst := image.NewRGBA(image.Rect(0, 0, side, side))
	offX := (side - b.Dx()) / 2
	offY := (side - b.Dy()) / 2
	draw.Draw(dst, image.Rect(offX, offY, offX+b.Dx(), offY+b.Dy()), img, b.Min, draw.Src)

	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
