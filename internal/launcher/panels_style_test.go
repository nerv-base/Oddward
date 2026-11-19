// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"image/color"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func sameColor(a, b color.RGBA, tolerance float32) bool {
	return near(float32(a.R), float32(b.R), tolerance) && near(float32(a.G), float32(b.G), tolerance) && near(float32(a.B), float32(b.B), tolerance)
}

func TestOverlayFloatsAsARoundedGlassSheet(t *testing.T) {
	s := NewShell(normalFixture(state.ManagedReady))
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1280, 720)
	if clickFailed(tt, "游戏设置") {
		t.Fatal("game settings unreachable")
	}
	tt.Advance(time.Second)
	tt.Frame()
	panel, ok := tt.AccessByRole(ui.RoleDialog, "游戏设置")
	if !ok {
		t.Fatal("settings dialog missing")
	}
	r := panel.Bounds
	if r.X+r.W > 1280-12 || r.Y < 12 || r.Y+r.H > 720-12 {
		t.Fatalf("the sheet must float inside the window, not glue to its edges: %+v", r)
	}
	img := tt.Image()
	corner := img.RGBAAt(int(r.X+2), int(r.Y+2))
	inside := img.RGBAAt(int(r.X+40), int(r.Y+r.H-8))
	outside := img.RGBAAt(int(r.X-6), int(r.Y+2))
	if sameColor(corner, inside, 3) && !sameColor(corner, outside, 3) {
		t.Fatalf("sheet corners must be rounded: corner %+v inside %+v outside %+v", corner, inside, outside)
	}
}

func TestLauncherSettingsFrostFollowsPosterWithOpaqueFallback(t *testing.T) {
	s := NewShell(normalFixture(state.ManagedReady))
	t.Cleanup(s.Dispose)
	tt := ui.NewTester(s.View, 1024, 631)
	if err := tt.Click("启动器设置"); err != nil {
		t.Fatal(err)
	}
	pixel := func(poster color.RGBA) color.RGBA {
		s.Poster = ui.NewBitmap(paintBands(32, 18, band{1, poster}))
		tt.Frame()
		return tt.Image().RGBAAt(850, 540)
	}
	warm, cool := color.RGBA{240, 65, 40, 255}, color.RGBA{40, 85, 240, 255}
	if a, b := pixel(warm), pixel(cool); a.R <= b.R+20 || b.B <= a.B+20 {
		t.Fatalf("settings background must pick up the poster colors, got %+v and %+v", a, b)
	}
	for _, prefs := range []ui.Preferences{{ReduceTransparency: true}, {HighContrast: true}} {
		tt.SetPreferences(prefs)
		if a, b := pixel(warm), pixel(cool); !sameColor(a, b, 2) {
			t.Fatalf("settings background must be opaque with preferences %+v, got %+v and %+v", prefs, a, b)
		}
	}
}

func TestCheckInPageLeadsWithALinkedTonePill(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1280, 720)
	if clickFailed(tt, "每日签到") {
		t.Fatal("check-in page unreachable")
	}
	// A saved account with no Tajiduo link has exactly one tone action: relink.
	loadCheckInPage(t, s, state.CheckInPayload{AccountID: "preview-account"})
	tt.Frame()
	b, ok := tt.Find("去关联塔吉多")
	if !ok {
		t.Fatal("relink action missing")
	}
	img := tt.Image()
	want := color.RGBA{defaultAccent.R, defaultAccent.G, defaultAccent.B, 255}
	if got := img.RGBAAt(int(b.X+14), int(b.Y+b.H/2)); !sameColor(got, want, 12) {
		t.Fatalf("the page's main action must wear the tone: %+v", got)
	}
	if got := img.RGBAAt(int(b.X+1), int(b.Y+1)); sameColor(got, want, 12) {
		t.Fatalf("the main action must be a pill: %+v", got)
	}

	if tt.HasText("今日已签到") {
		t.Fatal("an unlinked account cannot have a finished task")
	}
	if tt.HasText("本性像素") {
		t.Fatal("an unread status panel must not invent game numbers")
	}
}
