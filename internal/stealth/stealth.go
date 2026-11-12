// Package stealth checks whether the overlay shows up in a screen capture.
//
// The check works by having the overlay paint itself a solid test colour,
// capturing the display the same way a screen-sharing app would, and looking
// for that colour where the overlay is.
package stealth

import (
	"image"
)

// Verdict is the outcome of a visibility check.
type Verdict string

const (
	Hidden       Verdict = "hidden"       // the overlay is absent from the capture
	Visible      Verdict = "visible"      // the overlay appears in the capture
	Inconclusive Verdict = "inconclusive" // partially covered, off-screen or mid-redraw
)

// Rect is a rectangle in fractions (0..1) of the captured image.
type Rect struct{ X, Y, W, H float64 }

// isTestColour reports whether a pixel is the magenta test pattern. The
// thresholds are loose because the capture passes through the display's
// colour profile and JPEG compression.
func isTestColour(r, g, b uint32) bool {
	r, g, b = r>>8, g>>8, b>>8
	return r > 170 && b > 170 && g < 120 && r-g > 90 && b-g > 90
}

// Coverage returns the fraction of the overlay's area (slightly inset, to
// skip rounded corners) that shows the test colour in img. ok is false when
// the overlay has no measurable area inside the image.
func Coverage(img image.Image, frame Rect) (coverage float64, ok bool) {
	bounds := img.Bounds()
	w, h := float64(bounds.Dx()), float64(bounds.Dy())

	// Inset by 12% on each side.
	x0 := (frame.X + frame.W*0.12) * w
	x1 := (frame.X + frame.W*0.88) * w
	y0 := (frame.Y + frame.H*0.12) * h
	y1 := (frame.Y + frame.H*0.88) * h

	minX, maxX := clamp(int(x0), 0, bounds.Dx()), clamp(int(x1), 0, bounds.Dx())
	minY, maxY := clamp(int(y0), 0, bounds.Dy()), clamp(int(y1), 0, bounds.Dy())
	if maxX-minX < 4 || maxY-minY < 4 {
		return 0, false
	}

	step := 1
	if area := (maxX - minX) * (maxY - minY); area > 250_000 {
		step = 2
	}
	total, hits := 0, 0
	for y := minY; y < maxY; y += step {
		for x := minX; x < maxX; x += step {
			r, g, b, _ := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			total++
			if isTestColour(r, g, b) {
				hits++
			}
		}
	}
	if total == 0 {
		return 0, false
	}
	return float64(hits) / float64(total), true
}

// Judge turns a coverage value into a verdict.
func Judge(coverage float64) Verdict {
	switch {
	case coverage < 0.03:
		return Hidden
	case coverage > 0.5:
		return Visible
	}
	return Inconclusive
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
