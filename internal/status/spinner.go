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

// pulseFrameCount and pulseTickMS control the connected-state "live" pulse:
// a solid dot with an outer ring that expands and fades, looping — the
// same idea as macOS's own recording/live indicators.
const pulseFrameCount = 24
const pulseTickMS = 60

// renderPulseFrames draws pulseFrameCount frames of a solid center dot
// (dotDiameter) plus a ring expanding from the dot's own radius out to
// canvasDiameter/2, fading out as it grows, looping back to the start.
func renderPulseFrames(tint color.RGBA, dotDiameter, canvasDiameter int) []*qt.QPixmap {
	frames := make([]*qt.QPixmap, pulseFrameCount)
	center := float64(canvasDiameter) / 2
	dotRadius := float64(dotDiameter) / 2
	maxRingRadius := center

	for f := 0; f < pulseFrameCount; f++ {
		img := image.NewRGBA(image.Rect(0, 0, canvasDiameter, canvasDiameter))
		t := float64(f) / float64(pulseFrameCount)

		ringRadius := dotRadius + t*(maxRingRadius-dotRadius)
		ringAlpha := uint8((1 - t) * 130)
		drawRing(img, center, center, ringRadius, 2, color.RGBA{R: tint.R, G: tint.G, B: tint.B, A: ringAlpha})

		drawDot(img, center, center, dotRadius, tint)

		var buf bytes.Buffer
		_ = png.Encode(&buf, img)
		pixmap := qt.NewQPixmap()
		pixmap.LoadFromDataWithData(buf.Bytes())
		frames[f] = pixmap
	}
	return frames
}

// drawRing draws a thin circular outline (not a filled disc): every pixel
// whose distance from center falls within [radius-thickness, radius] is
// painted, which is all a "ring" is — the pulse's expanding halo.
func drawRing(img *image.RGBA, cx, cy, radius, thickness float64, c color.RGBA) {
	if radius <= 0 {
		return
	}
	inner := radius - thickness
	outer := radius
	minX, maxX := int(cx-outer-1), int(cx+outer+1)
	minY, maxY := int(cy-outer-1), int(cy+outer+1)
	inner2, outer2 := inner*inner, outer*outer
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			ddx, ddy := float64(x)-cx, float64(y)-cy
			d2 := ddx*ddx + ddy*ddy
			if d2 <= outer2 && d2 >= inner2 {
				draw.Draw(img, image.Rect(x, y, x+1, y+1), &image.Uniform{C: c}, image.Point{}, draw.Over)
			}
		}
	}
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
