// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"image"
	"image/color"
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func TestStarwardBarUsesCompactBottomCenteredIcons(t *testing.T) {
	for _, size := range [][2]int{{1024, 631}, {1280, 720}, {1600, 900}} {
		s := NewShell(send(settled(state.Ready), state.MediaReady))
		s.Model.ReducedMotion = true
		tt := ui.NewTester(s.View, size[0], size[1])
		tt.Key(0, ui.KeyF6)
		labels := []string{"播放动态背景", "背景设置", "收起控制台"}
		var first, last ui.Rect
		for i, label := range labels {
			r, ok := tt.Find(label)
			if !ok || r.W != 32 || r.H != 32 || math.Abs(float64(r.Y+r.H-float32(size[1]-4))) > .02 {
				t.Errorf("%dx%d %s must be a 32-DIP target, four DIPs from the bottom: %+v found=%v", size[0], size[1], label, r, ok)
			}
			if i == 0 {
				first = r
			} else if math.Abs(float64(r.X-(last.X+last.W)-4)) > .02 {
				t.Errorf("icon targets must have a four-DIP gap: previous=%+v next=%+v", last, r)
			}
			last = r
		}
		if math.Abs(float64((first.X+last.X+last.W)/2-float32(size[0])/2)) > .02 {
			t.Error("the icon group must be centered on the window, not on an old caption")
		}
		for _, caption := range []string{"动态背景 · 静音", "静态背景", "静态背景 · 兜底", "背景加载中"} {
			if _, ok := tt.Find(caption); ok {
				t.Errorf("status belongs in the control tooltip, not a permanent text label: %s", caption)
			}
		}
		s.Dispose()
	}
}

func TestStarwardBarFeathersIntoUnblurredArtwork(t *testing.T) {
	const width, height = 480, 180
	stripes := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			v := uint8(40)
			if x%16 < 8 {
				v = 240
			}
			stripes.SetRGBA(x, y, color.RGBA{v, v, v, 255})
		}
	}
	s := NewShell(settled(state.Ready))
	t.Cleanup(s.Dispose)
	s.Model.Bar, s.Model.ReducedMotion = state.MediaBarVisible, true
	bitmap := ui.NewBitmap(stripes)
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(launcherTheme())
		ui.Box(c).Fill().Children(func() {
			ui.Image(c, bitmap).Fill()
			s.mediaBar(c)
		})
	}, width, height)
	im := tt.Image()
	// Samples are far from the centered buttons. A gradient preserves the
	// high-frequency pattern, while the old full-width Gaussian blur erases it.
	light := color.RGBAModel.Convert(im.At(100, height-30)).(color.RGBA)
	dark := color.RGBAModel.Convert(im.At(108, height-30)).(color.RGBA)
	if int(light.R)-int(dark.R) < 85 {
		t.Fatalf("wallpaper detail was blurred by the bottom strip: light=%v dark=%v", light, dark)
	}
	top := color.RGBAModel.Convert(im.At(100, height-60)).(color.RGBA)
	if top.R < 235 || absInt(int(top.R)-int(top.G)) > 1 || absInt(int(top.G)-int(top.B)) > 1 {
		t.Fatalf("top of strip must fade seamlessly into wallpaper without a tinted edge: %v", top)
	}
	bottom := color.RGBAModel.Convert(im.At(100, height-2)).(color.RGBA)
	if bottom.R >= light.R || light.R >= top.R {
		t.Fatalf("neutral darkening must increase toward the bottom: top=%v middle=%v bottom=%v", top, light, bottom)
	}
	for _, prefs := range []ui.Preferences{{ReduceTransparency: true}, {HighContrast: true}} {
		tt.SetPreferences(prefs)
		im = tt.Image()
		a := color.RGBAModel.Convert(im.At(100, height-30)).(color.RGBA)
		b := color.RGBAModel.Convert(im.At(108, height-30)).(color.RGBA)
		surface := launcherTheme().Surface
		if a != b || a != (color.RGBA{surface.R, surface.G, surface.B, surface.A}) {
			t.Fatalf("accessibility preferences require the existing opaque theme fallback: %v/%v", a, b)
		}
	}
}

func TestStarwardBarDoesNotAddUnimplementedActions(t *testing.T) {
	s := NewShell(send(settled(state.Ready), state.MediaReady))
	t.Cleanup(s.Dispose)
	tt := ui.NewTester(s.View, 1280, 720)
	tt.Key(0, ui.KeyF6)
	tt.Advance(time.Second)
	for _, label := range []string{"图库", "复制背景", "另存为背景"} {
		if _, ok := tt.Find(label); ok {
			t.Fatalf("visual-only scope must not invent a working %s action", label)
		}
	}
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
