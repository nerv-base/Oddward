// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"github.com/egoist/mygo/plugins/glass"
	"github.com/egoist/mygo/ui"
)

// frostedSurface blurs the in-window backdrop without displacing its pixels.
// It is not OS acrylic or Liquid Glass: no refraction, lens rim or specular light.
type frostedSurface struct{ Tint ui.Color }

func (f frostedSurface) PaintMaterial(p *ui.Painter, box ui.Rect, radii [4]float32) {
	prefs := p.Preferences()
	if prefs.ReduceTransparency || prefs.HighContrast {
		p.FillRadii(box, radii, p.Theme().Surface)
		return
	}
	glass.Blur{Radius: 18}.PaintMaterial(p, box, radii)
	p.FillRadii(box, radii, f.Tint)
}

func (f frostedSurface) String() string { return "frosted(18)" }
