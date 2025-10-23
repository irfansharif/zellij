// Package palette provides color palette generation for rendering. It
// implements HSV-based palette generation with shimmer effects.
package palette

import (
	"image/color"
	"math/rand"

	"github.com/lucasb-eyer/go-colorful"
)

// Palette holds five RGBA colors.
type Palette [5]color.RGBA

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// RandomPalette returns a palette using HSV generation.
func RandomPalette(r *rand.Rand) Palette {
	// Convert HSV to RGBA using go-colorful.
	hsb := func(h, s, b float64) color.RGBA {
		// Convert from 0-100 range to 0-360 for hue, 0-1 for saturation and brightness.
		hue := h * 3.6
		sat := clamp(s/100.0, 0, 1)
		bright := clamp(b/100.0, 0, 1)

		c := colorful.Hsv(hue, sat, bright)
		red, green, blue := c.RGB255()
		return color.RGBA{R: red, G: green, B: blue, A: 255}
	}

	p := Palette{}
	p[0] = hsb(r.Float64()*100, r.Float64()*100, r.Float64()*30)
	p[1] = color.RGBA{R: 255, G: 255, B: 255, A: 255} // keep the inner background color plain white (blends with background)
	for i := 2; i < 5; i++ {
		p[i] = hsb(r.Float64()*100, r.Float64()*50+25, r.Float64()*50+25)
	}
	return p
}

// Shimmered applies a brightness jitter to accent colors 2..4 when shimmer >= 0.
func Shimmered(p Palette, shimmer int, r *rand.Rand) Palette {
	if shimmer < 0 {
		return p
	}

	out := p
	for i := 2; i < 5; i++ {
		// Convert RGBA to HSV.
		c := colorful.Color{R: float64(out[i].R) / 255, G: float64(out[i].G) / 255, B: float64(out[i].B) / 255}
		h, s, v := c.Hsv()

		// Apply brightness jitter.
		v = clamp(v+(r.Float64()-0.5)*0.2, 0, 1)

		// Convert back to RGBA.
		newC := colorful.Hsv(h, s, v)
		red, green, blue := newC.RGB255()
		out[i] = color.RGBA{R: red, G: green, B: blue, A: 255}
	}
	return out
}
