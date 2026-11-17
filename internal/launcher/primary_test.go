// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"math"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func TestNativePrimaryCarriesItsStateInAnInkDisc(t *testing.T) {
	for flow := state.Checking; flow <= state.Failed; flow++ {
		s := NewShell(settled(flow))
		tt := ui.NewTester(s.View, 1280, 720)
		r, ok := tt.Find(state.Present(s.Model).Primary)
		if !ok {
			t.Fatal("primary action missing")
		}
		frame := tt.Image()
		// The disc edge is ink; inside it the state glyph is drawn in the tone.
		edge := frame.RGBAAt(int(r.X+14), int(r.Y+r.H/2))
		if edge.R > 40 || edge.G > 40 || edge.B > 40 {
			t.Errorf("flow %v lost the ink state disc: %v", flow, edge)
		}
		glyphInk := 0
		for y := int(r.Y + 18); y < int(r.Y+r.H-18); y++ {
			for x := int(r.X + 18); x < int(r.X+42); x++ {
				if p := frame.RGBAAt(x, y); max(p.R, p.G, p.B) > 120 {
					glyphInk++
				}
			}
		}
		if glyphInk < 4 {
			t.Errorf("flow %v state glyph disappeared from the disc: %d pixels", flow, glyphInk)
		}
		s.Dispose()
	}
}

func TestNativePrimaryCaptionAlignsWithStatusColumn(t *testing.T) {
	for _, flow := range []state.Flow{state.Ready, state.NotInstalled, state.UpdateAvailable, state.Failed} {
		s := NewShell(settled(flow))
		tt := ui.NewTester(s.View, 1280, 720)
		r, _ := tt.Find(state.Present(s.Model).Primary)
		left, right := int(r.X+r.W), int(r.X)
		frame := tt.Image()
		// Measure actual dark caption ink right of the disc, below the status
		// line. No hidden layout-only box.
		for y := int(r.Y + 30); y < int(r.Y+r.H-10); y++ {
			for x := int(r.X + 56); x < int(r.X+r.W-20); x++ {
				pixel := frame.RGBAAt(x, y)
				if pixel.R < 100 && pixel.G < 100 && pixel.B < 100 {
					left, right = min(left, x), max(right, x)
				}
			}
		}
		if left > right {
			t.Fatal("primary caption has no visible ink")
		}
		if math.Abs(float64(left)-float64(r.X+62)) > 3 {
			t.Errorf("flow %v caption drifts from the status column: ink left %d, column %.1f", flow, left, r.X+62)
		}
		s.Dispose()
	}
}

func TestNativeUnbrandedNameKeepsTheSmallerBrandHierarchy(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1280, 720)
	r, ok := tt.Find("异环")
	if !ok || r.W > 55 || r.H > 30 {
		t.Fatalf("missing-art fallback must also stay subordinate to campaign art: %+v", r)
	}
}
