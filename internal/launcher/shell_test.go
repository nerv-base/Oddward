// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"math"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func clickFailed(tt *ui.Tester, label string) bool { return tt.Click(label) != nil }

func TestNativeDemoFreezeDoesNotChargePausedTime(t *testing.T) {
	s := NewShell(settled(state.Downloading))
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1280, 720)
	tt.Advance(2 * time.Second)
	before := s.Model.Progress
	s.Dispatch(state.Event{Kind: state.OpenOverlay, Overlay: state.Scenarios})
	s.Dispatch(state.Event{Kind: state.ToggleDemoClock})
	s.Dispatch(state.Event{Kind: state.Dismiss})
	tt.Frame()
	tt.Advance(30 * time.Second)
	if s.Model.Progress != before || s.Model.Flow != state.Downloading {
		t.Fatal("frozen mock work advanced")
	}
	s.Dispatch(state.Event{Kind: state.OpenOverlay, Overlay: state.Scenarios})
	s.Dispatch(state.Event{Kind: state.ToggleDemoClock})
	s.Dispatch(state.Event{Kind: state.Dismiss})
	tt.Frame()
	tt.Advance(time.Second)
	if s.Model.Flow != state.Downloading || math.Abs(s.Model.Progress-before-.1) > .001 {
		t.Fatalf("resuming charged the frozen interval: before=%v after=%v flow=%v", before, s.Model.Progress, s.Model.Flow)
	}
}

func TestNativeF8TogglesCompleteMockPreview(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1024, 631)
	tt.Key(0, ui.KeyF8)
	if s.Model.Overlay != state.Scenarios {
		t.Fatal("F8 must expose the complete mock state inspector")
	}
	if _, ok := tt.AccessByRole(ui.RoleButton, "重新检查"); !ok {
		t.Fatal("state inspector lacks checking state")
	}
	tt.Key(0, ui.KeyF8)
	if s.Model.Overlay != state.NoOverlay || clickFailed(tt, "启动游戏") {
		t.Fatal("F8 must return from preview to the interactive home")
	}
}

func TestNativeViewProjectsAndDispatches(t *testing.T) {
	s := NewShell(settled(state.Ready))
	tt := ui.NewTester(s.View, 1280, 720)
	if !tt.HasText("异环") || !tt.HasText("交互演示") {
		t.Fatal("missing identity or honest demo label")
	}
	if clickFailed(tt, "启动游戏") {
		t.Fatal("primary action inaccessible")
	}
	if s.Model.Flow != state.Launching {
		t.Fatal("click bypassed reducer or did not dispatch")
	}
	tt.Advance(2 * time.Second)
	if s.Model.Flow != state.Running || !tt.HasText("游戏运行中") {
		t.Fatal("demo executor did not complete launch")
	}
}
func TestNativeSettingsAndEscape(t *testing.T) {
	s := NewShell(settled(state.Ready))
	tt := ui.NewTester(s.View, 1040, 640)
	if clickFailed(tt, "启动器设置") || s.Model.Page != state.LauncherSettings || s.Model.Overlay != state.NoOverlay {
		t.Fatal("settings inaccessible")
	}
	if clickFailed(tt, "外观") {
		t.Fatal("appearance section inaccessible")
	}
	if clickFailed(tt, "减少动态效果") {
		t.Fatal("motion setting inaccessible")
	}
	if !s.Model.ReducedMotion {
		t.Fatal("setting failed to dispatch")
	}
	tt.Key(0, ui.KeyEscape)
	if s.Model.Overlay != state.NoOverlay {
		t.Fatal("Escape failed to dismiss")
	}
	if clickFailed(tt, "启动游戏") {
		t.Fatal("modal focus not restored")
	}
}
func TestNativeScenariosAndProgressControls(t *testing.T) {
	s := NewShell(settled(state.Ready))
	tt := ui.NewTester(s.View, 1280, 720)
	tt.Key(0, ui.KeyF8)
	if clickFailed(tt, "有可用更新") {
		t.Fatal("scenario picker inaccessible")
	}
	if s.Model.Flow != state.UpdateAvailable || clickFailed(tt, "更新游戏") {
		t.Fatal("scenario projection wrong")
	}
	tt.Advance(time.Second)
	if s.Model.Progress <= 0 {
		t.Fatal("download did not advance")
	}
	if clickFailed(tt, "暂停下载") {
		t.Fatal("pause inaccessible")
	}
	p := s.Model.Progress
	tt.Advance(5 * time.Second)
	if s.Model.Progress != p || s.Model.Flow != state.Paused {
		t.Fatal("paused operation changed")
	}
	if clickFailed(tt, "继续下载") {
		t.Fatal("resume inaccessible")
	}
	tt.Advance(12 * time.Second)
	tt.Advance(4 * time.Second)
	if s.Model.Flow != state.Ready {
		t.Fatalf("demo chain ended at %v", s.Model.Flow)
	}
}
func TestNativeQuitConfirmation(t *testing.T) {
	s := NewShell(settled(state.Downloading))
	tt := ui.NewTester(s.View, 1040, 640)
	if clickFailed(tt, "关闭窗口") || !tt.HasText("退出启动器？") {
		t.Fatal("missing close guard")
	}
	if clickFailed(tt, "继续留在这里") || s.Model.Overlay != state.NoOverlay {
		t.Fatal("cancel close failed")
	}
	if clickFailed(tt, "关闭窗口") || clickFailed(tt, "退出演示") || !s.Model.Closed {
		t.Fatal("confirm close failed")
	}
}
func TestNativeSystemReducedMotionIsProjected(t *testing.T) {
	s := NewShell(send(settled(state.Ready), state.MediaReady))
	t.Cleanup(s.Dispose)
	tt := ui.NewTester(s.View, 1280, 720)
	tt.Move(640, 719) // Playback state is exposed by the icon's accessible action.
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	_, showsPlay := tt.AccessByRole(ui.RoleButton, "播放动态背景")
	if s.Model.AnimateBackground() || s.Model.Media != state.MediaPaused || !showsPlay {
		t.Fatal("system reduced motion must update model and accessible playback action")
	}
	tt.SetPreferences(ui.Preferences{})
	_, showsPause := tt.AccessByRole(ui.RoleButton, "暂停动态背景")
	if !s.Model.AnimateBackground() || s.Model.Media != state.MediaPlaying || !showsPause {
		t.Fatal("clearing system preference should restore user preference and pause action")
	}
}
func TestNativeSettingsSwitchSemanticsAndPageNavigation(t *testing.T) {
	s := NewShell(settled(state.Ready))
	t.Cleanup(s.Dispose)
	tt := ui.NewTester(s.View, 1024, 631)
	if clickFailed(tt, "启动器设置") {
		t.Fatal("settings inaccessible")
	}
	if _, ok := tt.AccessByRole(ui.RoleButton, "启动游戏"); ok {
		t.Fatal("home control still visible on the settings page")
	}
	if clickFailed(tt, "外观") {
		t.Fatal("appearance section inaccessible")
	}
	sw, ok := tt.AccessByRole(ui.RoleSwitch, "动态背景")
	if !ok || !sw.Checked {
		t.Fatal("playback preference needs checked switch semantics")
	}
	if clickFailed(tt, "减少动态效果") {
		t.Fatal("reduce motion inaccessible")
	}
	sw, ok = tt.AccessByRole(ui.RoleSwitch, "减少动态效果")
	if !ok || !sw.Checked {
		t.Fatal("reduce motion switch not announced")
	}
	tt.Key(0, ui.KeyEscape)
	a, ok := tt.AccessByRole(ui.RoleButton, "游戏主页")
	if !ok || !a.Focused || s.Model.Page != state.Home {
		t.Fatal("Escape must return to the previous page and focus its rail entry")
	}
	tt.Key(0, ui.KeyF8)
	if clickFailed(tt, "重新检查") {
		t.Fatal("last scenario inaccessible at minimum client size")
	}
}
func TestNativeHomeKeepsDeveloperToolsSecondary(t *testing.T) {
	s := NewShell(settled(state.Ready))
	t.Cleanup(s.Dispose)
	tt := ui.NewTester(s.View, 1280, 720)
	if _, ok := tt.AccessByRole(ui.RoleButton, "状态预览"); ok {
		t.Fatal("developer scenarios must not occupy the game navigation")
	}
	if clickFailed(tt, "启动器设置") {
		t.Fatal("launcher settings unreachable")
	}
	if _, ok := tt.AccessByRole(ui.RoleButton, "状态预览"); ok {
		t.Fatal("developer scenarios must stay out of regular settings")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Key(0, ui.KeyF8)
	if s.Model.Overlay != state.Scenarios {
		t.Fatal("scenario lab must remain reachable through F8")
	}
}

func TestNativeBrandAndControlsUseLauncherProportions(t *testing.T) {
	s := NewShell(settled(state.Ready))
	t.Cleanup(s.Dispose)
	tt := ui.NewTester(s.View, 1280, 720)
	cta, ok := tt.Find("启动游戏")
	if !ok || cta.W != actionWidth || cta.H != actionHeight || cta.Y < 560 || cta.X+cta.W != 1280-dockRight-actionHeight-menuGap {
		t.Fatalf("primary action must anchor to the lower-right edge: %+v", cta)
	}
}

func TestNativeLayoutAtSupportedSizes(t *testing.T) {
	for _, size := range [][2]int{{1024, 631}, {1040, 640}, {1280, 720}, {1600, 900}} {
		s := NewShell(settled(state.Ready))
		tt := ui.NewTester(s.View, size[0], size[1])
		r, ok := tt.Find("启动游戏")
		if !ok {
			t.Fatalf("missing CTA at %v", size)
		}
		if r.X < 640 || r.Y < 400 || r.X+r.W > float32(size[0]) || r.Y+r.H > float32(size[1]) {
			t.Fatalf("CTA out of composition: %+v at %v", r, size)
		}
	}
}
