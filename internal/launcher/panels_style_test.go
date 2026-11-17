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
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1280, 720)
	if clickFailed(tt, "设置") {
		t.Fatal("settings unreachable")
	}
	tt.Advance(time.Second)
	tt.Frame()
	panel, ok := tt.AccessByRole(ui.RoleDialog, "设置")
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

func TestFeaturePageLeadsWithATonePill(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1280, 720)
	if clickFailed(tt, "自动签到") {
		t.Fatal("feature page unreachable")
	}
	tt.Frame()
	b, ok := tt.Find("模拟签到")
	if !ok {
		t.Fatal("feature action missing")
	}
	img := tt.Image()
	want := color.RGBA{defaultAccent.R, defaultAccent.G, defaultAccent.B, 255}
	if got := img.RGBAAt(int(b.X+14), int(b.Y+b.H/2)); !sameColor(got, want, 12) {
		t.Fatalf("the page's main mock action must wear the tone: %+v", got)
	}
	if got := img.RGBAAt(int(b.X+1), int(b.Y+1)); sameColor(got, want, 12) {
		t.Fatalf("the main action must be a pill: %+v", got)
	}
	back, _ := tt.Find("返回游戏")
	if got := img.RGBAAt(int(back.X+14), int(back.Y+back.H/2)); sameColor(got, want, 40) {
		t.Fatal("secondary actions must stay quiet glass")
	}
}
