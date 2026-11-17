// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"image"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
	"path/filepath"

	"github.com/egoist/mygo/ui"
)

// defaultAccent is the launch tone when no key art is loaded or the art has no
// usable hue: a warm signal yellow, the genre's conventional "go" colour.
var defaultAccent = ui.Hex("#FFD84D")

// ArtAccent borrows the dominant vivid hue of a version's key art and lifts it
// into a launch tone that ink text reads on at 7:1. The art changes every
// version; the launcher's colour follows instead of fighting it.
func ArtAccent(img image.Image) (ui.Color, bool) {
	b := img.Bounds()
	if b.Empty() {
		return ui.Color{}, false
	}
	const bins = 36
	var weight [bins]float64
	var sumX, sumY [bins]float64
	var sumS [bins]float64
	samples := 0
	stepX, stepY := max(b.Dx()/96, 1), max(b.Dy()/54, 1)
	for y := b.Min.Y; y < b.Max.Y; y += stepY {
		for x := b.Min.X; x < b.Max.X; x += stepX {
			samples++
			r, g, bl, a := img.At(x, y).RGBA()
			if a < 0x8000 {
				continue
			}
			h, s, v := hsv(float64(r)/0xffff, float64(g)/0xffff, float64(bl)/0xffff)
			if s < .28 || v < .2 {
				continue
			}
			w := s * s * v
			i := int(h/10) % bins
			weight[i] += w
			rad := h * math.Pi / 180
			sumX[i] += w * math.Cos(rad)
			sumY[i] += w * math.Sin(rad)
			sumS[i] += w * s
		}
	}
	best, bestScore, total := -1, 0., 0.
	for i := range weight {
		total += weight[i]
		score := weight[i] + .5*(weight[(i+bins-1)%bins]+weight[(i+1)%bins])
		if score > bestScore {
			best, bestScore = i, score
		}
	}
	// A few stray vivid pixels are noise, not a palette.
	if best < 0 || total < .02*float64(samples) {
		return ui.Color{}, false
	}
	var x, y, ws, wt float64
	for _, i := range []int{(best + bins - 1) % bins, best, (best + 1) % bins} {
		x, y, ws, wt = x+sumX[i], y+sumY[i], ws+sumS[i], wt+weight[i]
	}
	hue := math.Atan2(y, x) * 180 / math.Pi
	if hue < 0 {
		hue += 360
	}
	sat := min(max(ws/wt, .74), .94)
	for l := .66; ; l += .02 {
		c := hslColor(hue, sat, l)
		if contrastRatio(c, ink) >= 7 || l >= .86 {
			return c, true
		}
	}
}

// PosterAccent reads the same still LoadPoster would show, without caching or
// copying it, and derives its launch tone.
func PosterAccent(videoPath, posterPath string) (ui.Color, bool) {
	if posterPath == "" && videoPath != "" {
		posterPath = filepath.Join(filepath.Dir(videoPath), "bg_0.png")
	}
	if posterPath == "" {
		return ui.Color{}, false
	}
	f, err := os.Open(posterPath)
	if err != nil {
		return ui.Color{}, false
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return ui.Color{}, false
	}
	return ArtAccent(img)
}

func hsv(r, g, b float64) (h, s, v float64) {
	hi, lo := max(r, g, b), min(r, g, b)
	v, d := hi, hi-lo
	if hi == 0 || d == 0 {
		return 0, 0, v
	}
	s = d / hi
	switch hi {
	case r:
		h = math.Mod((g-b)/d, 6)
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	h *= 60
	if h < 0 {
		h += 360
	}
	return h, s, v
}

func hslColor(h, s, l float64) ui.Color {
	c := (1 - math.Abs(2*l-1)) * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := l - c/2
	var r, g, b float64
	switch {
	case h < 60:
		r, g = c, x
	case h < 120:
		r, g = x, c
	case h < 180:
		g, b = c, x
	case h < 240:
		g, b = x, c
	case h < 300:
		r, b = x, c
	default:
		r, b = c, x
	}
	to := func(v float64) uint8 { return uint8(math.Round(min(max(v+m, 0), 1) * 255)) }
	return ui.RGBA(to(r), to(g), to(b), 1)
}

func luminance(c ui.Color) float64 {
	lin := func(v uint8) float64 {
		f := float64(v) / 255
		if f <= .04045 {
			return f / 12.92
		}
		return math.Pow((f+.055)/1.055, 2.4)
	}
	return .2126*lin(c.R) + .7152*lin(c.G) + .0722*lin(c.B)
}

func contrastRatio(a, b ui.Color) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + .05) / (lb + .05)
}
