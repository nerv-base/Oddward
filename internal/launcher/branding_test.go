// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func TestBrandMarkStaysInsideNavigationRail(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1280, 720)
	mark, ok := tt.Find("Oddward 标志")
	if !ok {
		t.Fatal("navigation is missing the named Oddward brand mark")
	}
	if mark.W != 40 || mark.H != 40 || mark.X < 0 || mark.X+mark.W > railWidth || mark.Y+mark.H >= tileTop {
		t.Fatalf("brand mark overlaps navigation or leaves the rail: %+v", mark)
	}
	bright := 0
	for y := int(mark.Y); y < int(mark.Y+mark.H); y++ {
		for x := int(mark.X); x < int(mark.X+mark.W); x++ {
			p := tt.Image().RGBAAt(x, y)
			if p.R > 180 && p.G > 180 && p.B > 180 {
				bright++
			}
		}
	}
	if bright < 200 {
		t.Fatalf("brand mark did not render visibly: %d bright pixels", bright)
	}
}
