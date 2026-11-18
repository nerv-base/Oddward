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

func TestFrostBlursBackdropWithoutRefractionAndHasOpaqueFallback(t *testing.T) {
	stripes := image.NewRGBA(image.Rect(0, 0, 240, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 240; x++ {
			v := uint8(30)
			if x%16 < 8 {
				v = 230
			}
			stripes.SetRGBA(x, y, color.RGBA{v, v, v, 255})
		}
	}
	bitmap := ui.NewBitmap(stripes)
	view := func(c *ui.Context) {
		c.SetTheme(launcherTheme())
		ui.Box(c).Fill().Children(func() {
			ui.Image(c, bitmap).Fill()
			ui.Box(c).Absolute().Left(16).Right(16).Top(10).Bottom(10).Material(frostedSurface{Tint: ui.RGBA(18, 27, 40, .6)})
		})
	}
	tt := ui.NewTester(view, 240, 100)
	a, _, _, _ := tt.Image().At(100, 50).RGBA()
	b, _, _, _ := tt.Image().At(108, 50).RGBA()
	if math.Abs(float64(a)-float64(b)) > 700 {
		t.Fatalf("frost must blur backdrop detail rather than tint sharp stripes: %d/%d", a, b)
	}
	tt.SetPreferences(ui.Preferences{ReduceTransparency: true})
	opaque := tt.Image()
	a, _, _, _ = opaque.At(100, 50).RGBA()
	b, _, _, _ = opaque.At(108, 50).RGBA()
	if a != b {
		t.Fatal("reduced transparency must remove the backdrop entirely")
	}
	surface := launcherTheme().Surface
	want, _, _, _ := color.RGBA{surface.R, surface.G, surface.B, surface.A}.RGBA()
	if a != want {
		t.Fatalf("opaque fallback must use theme Surface, got %d want %d", a, want)
	}
}

func TestNativeConsoleIsFullWidthAndHasItsOwnBottomLane(t *testing.T) {
	for _, size := range [][2]float32{{1024, 631}, {1280, 720}, {1600, 900}} {
		s := NewShell(send(settled(state.Ready), state.MediaReady))
		s.Model.ReducedMotion = true
		tt := ui.NewTester(s.View, int(size[0]), int(size[1]))
		// Starward-like 60-DIP band, not only the last 20 pixels or the old capsule.
		tt.Move(180, size[1]-40)
		tt.Advance(time.Second)
		bar, ok := tt.AccessByRole(ui.RoleToolbar, "背景播放控制")
		if !ok {
			t.Fatal("entire bottom console lane must reveal controls")
		}
		if bar.Bounds.X != 0 || bar.Bounds.W != size[0] || bar.Bounds.Y != size[1]-60 || bar.Bounds.H != 60 {
			t.Fatalf("expected full-width 60-DIP bottom console, got %+v", bar.Bounds)
		}
		for _, label := range []string{"启动游戏", "查看官方公告"} {
			r, ok := tt.Find(label)
			if !ok || r.Y+r.H > bar.Bounds.Y-12 {
				t.Fatalf("%s intrudes into console lane: %+v", label, r)
			}
		}
		for _, label := range []string{"背景设置", "收起控制台"} {
			if _, ok := tt.AccessByRole(ui.RoleButton, label); !ok {
				t.Fatalf("missing working console action %s", label)
			}
		}
		if clickFailed(tt, "背景设置") || s.Model.Page != state.LauncherSettings || s.Model.Overlay != state.NoOverlay {
			t.Fatal("background settings must navigate to the launcher settings page")
		}
		if _, ok := tt.AccessByRole(ui.RoleToolbar, "背景播放控制"); ok {
			t.Fatal("console must yield to settings")
		}
		s.Dispose()
	}
}

func TestNativeConsoleCanDismissWithoutMouseReopening(t *testing.T) {
	s := NewShell(send(settled(state.Ready), state.MediaReady))
	t.Cleanup(s.Dispose)
	s.Model.ReducedMotion = true
	tt := ui.NewTester(s.View, 1280, 720)
	tt.Key(0, ui.KeyF6)
	if clickFailed(tt, "收起控制台") {
		t.Fatal("close action not reachable")
	}
	tt.Frame()
	if _, ok := tt.AccessByRole(ui.RoleToolbar, "背景播放控制"); ok {
		t.Fatal("dismissed console must be removed from input and access tree")
	}
	tt.Move(600, 300)
	tt.Move(600, 690)
	if _, ok := tt.AccessByRole(ui.RoleToolbar, "背景播放控制"); !ok {
		t.Fatal("new pointer enter must reveal console again")
	}
}

func TestNativeConsoleRestoresKeyboardFocusAfterSettings(t *testing.T) {
	s := NewShell(send(settled(state.Ready), state.MediaReady))
	t.Cleanup(s.Dispose)
	tt := ui.NewTester(s.View, 1280, 720)
	tt.Key(0, ui.KeyF6)
	tt.Key(0, ui.KeyTab)
	tt.Key(0, ui.KeyTab)
	tt.Key(0, ui.KeySpace)
	if s.Model.Page != state.LauncherSettings || s.Model.Overlay != state.NoOverlay {
		t.Fatal("keyboard background settings did not open")
	}
	tt.Advance(300 * time.Millisecond) // Let the modal entrance become interactive.
	tt.Move(900, 300)
	tt.Key(0, ui.KeyTab) // Enter a setting; close-button focus tooltip no longer owns Escape.
	tt.Key(0, ui.KeyEscape)
	tt.Advance(2 * time.Second)
	bar, ok := tt.AccessByRole(ui.RoleToolbar, "背景播放控制")
	if !ok || !bar.Focused {
		t.Fatalf("closing background settings must restore console keyboard focus without pointer assistance: bar=%v access=%+v model=%+v", ok, bar, s.Model)
	}
}

func TestNativeConsoleDoesNotTooltipItsWholeSurface(t *testing.T) {
	s := NewShell(send(settled(state.Ready), state.MediaReady))
	t.Cleanup(s.Dispose)
	s.Model.ReducedMotion = true
	tt := ui.NewTester(s.View, 1280, 720)
	tt.Move(100, 690)
	tt.Advance(2 * time.Second)
	for _, node := range tt.AccessAll() {
		if node.Role == ui.RoleTooltip {
			t.Fatal("hovering empty console must not place a tooltip over the artwork")
		}
	}
}

func TestNativePausedVideoShowsStaticPoster(t *testing.T) {
	s := NewShell(send(settled(state.Ready), state.MediaReady))
	t.Cleanup(s.Dispose)
	solid := func(c color.RGBA) *image.RGBA {
		im := image.NewRGBA(image.Rect(0, 0, 16, 9))
		for y := 0; y < 9; y++ {
			for x := 0; x < 16; x++ {
				im.SetRGBA(x, y, c)
			}
		}
		return im
	}
	s.Poster = ui.NewBitmap(solid(color.RGBA{230, 30, 70, 255}))
	p, err := ui.NewVideoPlayer(&posterProbeDecoder{frame: solid(color.RGBA{0, 0, 255, 255})})
	if err != nil {
		t.Fatal(err)
	}
	s.Player = p
	s.Dispatch(state.Event{Kind: state.ToggleMotion})
	tt := ui.NewTester(s.stage, 1280, 720)
	r, g, b, _ := tt.Image().At(700, 320).RGBA()
	if r < 50000 || g > 12000 || b > 25000 {
		t.Fatalf("paused background must show supplied static poster, got %d/%d/%d", r, g, b)
	}
}
