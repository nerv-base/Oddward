// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func TestNativeLaunchBlockAcrossAllFlows(t *testing.T) {
	for _, size := range [][2]int{{1024, 631}, {1280, 720}, {1600, 900}} {
		for flow := state.Checking; flow <= state.Failed; flow++ {
			s := NewShell(settled(flow))
			tt := ui.NewTester(s.View, size[0], size[1])
			cta, ok := tt.AccessByRole(ui.RoleButton, state.Present(s.Model).Primary)
			if !ok || cta.Bounds.W != actionWidth || cta.Bounds.H != actionHeight {
				t.Fatalf("launch block must keep one native footprint: size=%v flow=%v bounds=%+v", size, flow, cta.Bounds)
			}
			if cta.Bounds.X+cta.Bounds.W > float32(size[0])-dockRight || cta.Bounds.Y+cta.Bounds.H > float32(size[1])-homeBottom {
				t.Fatalf("launch block leaves the safe area: %+v", cta.Bounds)
			}
			s.Dispose()
		}
	}
}

func TestNativeNewsCoverAndReadingAreaAreOneConnectedModule(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1280, 720)
	module, ok := tt.Find("官方公告")
	cover, coverOK := tt.Find("查看官方公告")
	tabs, tabsOK := tt.Find("公告分类")
	if !ok || !coverOK || !tabsOK {
		t.Fatal("connected news must contain a cover and a category strip")
	}
	if module.W < 520 || module.W > newsWidth || module.H != newsHeight {
		t.Fatalf("news module does not match the dispatch composition: %+v", module)
	}
	if math.Abs(float64(cover.X+cover.W-tabs.X)) > 1 || cover.Y != tabs.Y || cover.X != module.X || cover.H != module.H {
		t.Fatalf("cover and announcements must touch without a gap: module=%+v cover=%+v tabs=%+v", module, cover, tabs)
	}
	cta, _ := tt.Find("启动游戏")
	if math.Abs(float64(module.Y+module.H-cta.Y-cta.H)) > 1 {
		t.Fatalf("news and action bottom baselines drift: %+v %+v", module, cta)
	}
}

func TestNativeWindowButtonsBlendWithBackdropAtRest(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	poster := image.NewRGBA(image.Rect(0, 0, 32, 18))
	for y := range 18 {
		for x := range 32 {
			poster.SetRGBA(x, y, color.RGBA{150, 160, 170, 255})
		}
	}
	s.Poster = ui.NewBitmap(poster)
	tt := ui.NewTester(s.View, 1280, 720)
	for _, label := range []string{"最小化", "最大化或还原", "关闭窗口"} {
		b, ok := tt.AccessByRole(ui.RoleButton, label)
		if !ok || b.Bounds.W < 32 || b.Bounds.H < 32 {
			t.Fatalf("caption button lost native hit area: %s %+v", label, b)
		}
		// Round glass plates are the frame's caption language: the centre
		// must read darker than the art beside it so the glyph stays legible.
		y := int(b.Bounds.Y + b.Bounds.H/2)
		plate := tt.Image().RGBAAt(int(b.Bounds.X+5), y)
		art := tt.Image().RGBAAt(int(b.Bounds.X)-5, y)
		if int(plate.R)+int(plate.G)+int(plate.B) >= int(art.R)+int(art.G)+int(art.B) {
			t.Fatalf("caption %s needs a darker glass plate over bright art: plate=%v art=%v", label, plate, art)
		}
	}
}
func TestNativeVersionArtworkContainsArbitraryAspectRatios(t *testing.T) {
	for _, dimensions := range [][2]int{{1097, 500}, {1600, 160}, {240, 720}} {
		s := NewShell(settled(state.Ready))
		s.Slogan = ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, dimensions[0], dimensions[1])))
		tt := ui.NewTester(s.View, 1024, 631)
		r, ok := tt.Find("异环官网活动标语")
		news, _ := tt.Find("官方公告")
		if !ok || r.X != homeLeft || r.X+r.W > homeLeft+sloganWidth || r.Y < sloganTop || r.Y+r.H > news.Y-32 {
			t.Fatalf("replaceable artwork escapes its safe area: source=%v slogan=%+v news=%+v", dimensions, r, news)
		}
		if math.Abs(float64(r.W/r.H)-float64(dimensions[0])/float64(dimensions[1])) > .01 {
			t.Fatalf("replaceable artwork was distorted: %v %+v", dimensions, r)
		}
		s.Dispose()
	}
}
