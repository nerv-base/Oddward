// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"image"
	"math"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func TestNativeSingleGameHasFunctionalNavigation(t *testing.T) {
	s := NewShell(settled(state.Ready))
	t.Cleanup(s.Dispose)
	tt := ui.NewTester(s.View, 1280, 720)
	for _, label := range []string{"游戏主页", "抽卡分析", "自动签到"} {
		item, ok := tt.AccessByRole(ui.RoleButton, label)
		if !ok || item.Bounds.X > 30 || item.Bounds.W < 44 {
			t.Fatalf("functional rail entry missing: %s %+v", label, item)
		}
	}
	settings, ok := tt.Find("启动器设置")
	if !ok || settings.X > 30 || settings.Y < 550 {
		t.Fatalf("settings must anchor the bottom of the functional rail, got %+v", settings)
	}
	if clickFailed(tt, "启动器设置") || clickFailed(tt, "关于") || (s.Model.Page != state.LauncherSettings || s.Model.Overlay != state.NoOverlay || s.Model.SettingsSection != state.SettingsAbout) {
		t.Fatal("about must remain available through settings")
	}
}

func TestNativeOfficialLogoUsesSuppliedArtwork(t *testing.T) {
	for _, size := range [][2]int{{1024, 631}, {1280, 720}, {1600, 900}} {
		s := NewShell(settled(state.Ready))
		t.Cleanup(s.Dispose)
		s.Logo = ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 277, 195)))
		s.Slogan = ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 1097, 500)))
		tt := ui.NewTester(s.View, size[0], size[1])
		logo, ok := tt.Find("异环官方标志")
		if !ok || logo.X != homeLeft || logo.Y < 28 || logo.Y > 40 || logo.W < 112 || logo.W > 124 {
			t.Fatalf("official bitmap must anchor the upper-left, not a fake wordmark: %+v", logo)
		}
		if math.Abs(float64(logo.W/logo.H)-277.0/195.0) > .01 {
			t.Fatalf("official logo must not be stretched: %+v", logo)
		}
		slogan, _ := tt.Find("异环官网活动标语")
		// Full artwork boxes share a version-neutral safe area; no measured
		// transparent padding from one particular campaign enters the layout.
		if logo.Y+logo.H > slogan.Y-3.98 {
			t.Fatalf("logo and campaign hierarchy collide: %+v %+v", logo, slogan)
		}
		if tt.HasText("NTE") || clickFailed(tt, "启动游戏") {
			t.Fatal("no typed imitation or decorative hit target may replace supplied artwork")
		}
	}
}

func TestNativeMissingLogoDoesNotInventAMark(t *testing.T) {
	s := NewShell(settled(state.Ready))
	t.Cleanup(s.Dispose)
	tt := ui.NewTester(s.View, 1280, 720)
	if tt.HasText("NTE") {
		t.Fatal("missing artwork must not fall back to a hand-typeset fake logo")
	}
	if !tt.HasText("异环") || clickFailed(tt, "启动游戏") {
		t.Fatal("unbranded fallback still needs a readable game name and usable action")
	}
}

func TestNativeProgressIsAccessibleAbovePrimary(t *testing.T) {
	for _, size := range [][2]int{{1024, 631}, {1280, 720}} {
		m := settled(state.Paused)
		m.Progress = .42
		s := NewShell(m)
		t.Cleanup(s.Dispose)
		tt := ui.NewTester(s.View, size[0], size[1])
		progress, ok := tt.AccessByRole(ui.RoleProgress, "任务进度")
		if !ok {
			t.Fatal("progress must use a native accessible progress indicator")
		}
		cta, _ := tt.Find("继续下载")
		if progress.Bounds.X < cta.X || progress.Bounds.Y+progress.Bounds.H > cta.Y || progress.Now != .42 {
			t.Fatalf("accessible progress must sit in the deck above the CTA: %+v %+v", progress.Bounds, cta)
		}
		if !tt.HasText("42%") || clickFailed(tt, "继续下载") || s.Model.Flow != state.Downloading {
			t.Fatal("moving progress must preserve the reducer-driven primary action")
		}
	}
}

func TestNativeSwitchChangeDispatchesOnlyOnce(t *testing.T) {
	s := NewShell(settled(state.Ready))
	t.Cleanup(s.Dispose)
	tt := ui.NewTester(s.View, 1024, 631)
	if clickFailed(tt, "启动器设置") || clickFailed(tt, "减少动态效果") {
		t.Fatal("switch unavailable")
	}
	for range 3 {
		tt.Frame()
	}
	if !s.Model.ReducedMotion {
		t.Fatal("a controlled switch must not repeat its change across frames")
	}
	tt.Key(0, ui.KeySpace)
	if s.Model.ReducedMotion {
		t.Fatal("native switch must dispatch keyboard changes through the reducer")
	}
}
