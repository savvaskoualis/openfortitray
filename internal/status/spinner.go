package status

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"

	qt "github.com/mappu/miqt/qt6"
)

// spinnerFrameCount is how many rotation positions are pre-rendered. Higher
// reads as smoother motion; 12 matches the classic macOS/iOS activity
// spinner's dot count.
const spinnerFrameCount = 12

// spinnerTickMS is how long each frame is shown — one full revolution takes
// spinnerFrameCount * spinnerTickMS.
const spinnerTickMS = 80

// renderSpinnerFrames draws spinnerFrameCount PNGs of a ring of small dots
// fading around the circle — the same "arc of dots with a bright leading
// edge" look as the native macOS spinner — tinted the given color. Pure
// stdlib image code, the same technique internal/tray/badge.go already uses
// for the tray icon's badge dot.
func renderSpinnerFrames(tint color.RGBA, diameter int) []*qt.QPixmap {
	const dots = 8
	frames := make([]*qt.QPixmap, spinnerFrameCount)
	for f := 0; f < spinnerFrameCount; f++ {
		frames[f] = renderSpinnerFrame(tint, diameter, dots, f)
	}
	return frames
}

func renderSpinnerFrame(tint color.RGBA, diameter, dots, leadIndex int) *qt.QPixmap {
	img := image.NewRGBA(image.Rect(0, 0, diameter, diameter))
	center := float64(diameter) / 2
	ringRadius := center * 0.72
	dotRadius := center * 0.11

	for i := 0; i < dots; i++ {
		angle := 2 * math.Pi * float64(i) / float64(dots)
		dx := center + ringRadius*math.Cos(angle)
		dy := center + ringRadius*math.Sin(angle)

		// Fade going backward from the lead dot, so the ring reads as a
		// comet-like arc chasing itself rather than a static ring of equal
		// dots — this is what actually sells "spinning" in a still frame.
		back := (i - leadIndex + dots) % dots
		fade := 1.0 - float64(back)/float64(dots)
		alpha := uint8(40 + fade*(255-40))

		drawDot(img, dx, dy, dotRadius, color.RGBA{R: tint.R, G: tint.G, B: tint.B, A: alpha})
	}

	var buf bytes.Buffer
	_ = png.Encode(&buf, img)
	pixmap := qt.NewQPixmap()
	pixmap.LoadFromDataWithData(buf.Bytes())
	return pixmap
}

func drawDot(img *image.RGBA, cx, cy, radius float64, c color.RGBA) {
	minX, maxX := int(cx-radius-1), int(cx+radius+1)
	minY, maxY := int(cy-radius-1), int(cy+radius+1)
	r2 := radius * radius
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			ddx, ddy := float64(x)-cx, float64(y)-cy
			if ddx*ddx+ddy*ddy <= r2 {
				draw.Draw(img, image.Rect(x, y, x+1, y+1), &image.Uniform{C: c}, image.Point{}, draw.Over)
			}
		}
	}
}
