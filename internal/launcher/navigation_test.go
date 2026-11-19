// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func TestNavigationPreservesOperationAndRejectsHiddenActions(t *testing.T) {
	m := settled(state.Downloading)
	m.Progress = .31
	next, fx := state.Reduce(m, state.Event{Kind: state.Navigate, Page: state.WishAnalysis})
	if next.Page != state.WishAnalysis || next.Flow != m.Flow || next.Token != m.Token || next.Progress != m.Progress || len(fx) != 0 {
		t.Fatalf("navigation must change only page, preserving the operation: %+v %+v", next, fx)
	}
	unchanged, fx := state.Reduce(next, state.Event{Kind: state.Primary})
	if unchanged != next || len(fx) != 0 {
		t.Fatal("hidden primary action must be rejected")
	}
	next, _ = state.Reduce(next, state.Event{Kind: state.Progressed, Token: m.Token, Progress: .52})
	if next.Progress != .52 {
		t.Fatal("operation must continue away from home")
	}
	next, _ = state.Reduce(next, state.Event{Kind: state.Navigate, Page: state.CheckIn})
	next, _ = state.Reduce(next, state.Event{Kind: state.Navigate, Page: state.Home})
	if next.Page != state.Home || next.Progress != .52 || next.Token != m.Token {
		t.Fatal("returning must not reset task")
	}
}

func TestNavigationGuardsAndScenarioReturn(t *testing.T) {
	for _, overlay := range []state.Overlay{state.LaunchNotice, state.Scenarios, state.ConfirmQuit} {
		m := settled(state.Ready)
		m.Overlay = overlay
		next, fx := state.Reduce(m, state.Event{Kind: state.Navigate, Page: state.CheckIn})
		if next != m || len(fx) != 0 {
			t.Fatalf("modal %v must block page changes", overlay)
		}
	}
	m := settled(state.Ready)
	next, _ := state.Reduce(m, state.Event{Kind: state.Navigate, Page: state.Page(255)})
	if next != m {
		t.Fatal("unknown page must not be accepted")
	}
	m.Page, m.Overlay = state.CheckIn, state.Scenarios
	next, _ = state.Reduce(m, state.Event{Kind: state.SelectScenario, Flow: state.Paused})
	if next.Page != state.Home || next.Flow != state.Paused {
		t.Fatal("scenario preview must reveal its game state on home")
	}
}

func TestNativeFeatureNavigationKeepsPagesHonest(t *testing.T) {
	for _, size := range [][2]int{{1024, 631}, {1280, 720}, {1600, 900}} {
		s := NewShell(settled(state.Ready))
		t.Cleanup(s.Dispose)
		tt := ui.NewTester(s.View, size[0], size[1])
		for _, item := range []struct {
			label string
			page  state.Page
		}{{"抽卡分析", state.WishAnalysis}, {"每日签到", state.CheckIn}} {
			if clickFailed(tt, item.label) || s.Model.Page != item.page {
				t.Fatalf("missing functional navigation %s", item.label)
			}
			if item.page == state.CheckIn {
				loadCheckInPage(t, s, linkedCheckInPayload("preview-account"))
				tt.Frame()
				if !tt.HasText("游戏签到") || !tt.HasText("社区签到") {
					t.Fatal("check-in page must show both tasks")
				}
			}
			for _, node := range tt.AccessAll() {
				if node.Role == ui.RoleText {
					for _, r := range node.Name {
						if r >= '\uE000' && r <= '\uF8FF' {
							t.Fatal("decorative icons must not be announced as private-use text")
						}
					}
				}
			}
			if tt.HasText("MOCK · 仅内存演示") {
				t.Fatal("no page may present local demo data as a mock")
			}
			if _, ok := tt.AccessByRole(ui.RoleButton, "启动游戏"); ok {
				t.Fatal("hidden game CTA must not remain accessible")
			}
			if err := tt.Click("启动器设置"); err != nil {
				t.Fatalf("settings must remain available off home (%s at %v, bar=%v): %v", item.label, size, s.Model.Bar, err)
			}
			tt.Key(0, ui.KeyEscape)
			if s.Model.Page != item.page {
				t.Fatal("closing settings must restore current page")
			}
		}
		if clickFailed(tt, "游戏主页") || clickFailed(tt, "启动游戏") {
			t.Fatal("home action must return")
		}
	}
}

func TestNativeNavigationKeepsDemoClockRunning(t *testing.T) {
	s := NewShell(settled(state.Downloading))
	t.Cleanup(s.Dispose)
	tt := ui.NewTester(s.View, 1280, 720)
	if clickFailed(tt, "抽卡分析") {
		t.Fatal("missing analysis page")
	}
	tt.Advance(time.Second)
	if s.Model.Page != state.WishAnalysis || s.Model.Progress <= 0 {
		t.Fatal("navigation stopped operation")
	}
	if clickFailed(tt, "游戏主页") || !tt.HasText("暂停下载") {
		t.Fatal("return must show active operation")
	}
}

func TestNativeSpineCellsKeepAccessibleTargets(t *testing.T) {
	for _, size := range [][2]int{{1024, 631}, {1280, 720}, {1600, 900}} {
		s := NewShell(settled(state.Ready))
		t.Cleanup(s.Dispose)
		tt := ui.NewTester(s.View, size[0], size[1])

		for _, label := range []string{"游戏主页", "抽卡分析", "每日签到", "启动器设置"} {
			a, ok := tt.AccessByRole(ui.RoleButton, label)
			if !ok || a.Bounds.X != (railWidth-tileSize)/2 || a.Bounds.W != tileSize || a.Bounds.H != tileSize || !a.Focusable {
				t.Fatalf("rail must retain a centered tile target: %s %+v", label, a)
			}
			if a.Description != label {
				t.Fatalf("icon %s lost its tooltip: %+v", label, a)
			}
		}
		settings, _ := tt.AccessByRole(ui.RoleButton, "启动器设置")
		if settings.Bounds.Y+settings.Bounds.H < float32(size[1])-24 {
			t.Fatal("settings must anchor at the full-height rail bottom")
		}
		for _, a := range tt.AccessAll() {
			if a.Role == ui.RoleText && (a.Name == "游戏" || a.Name == "抽卡" || a.Name == "签到") {
				t.Fatalf("spine must be icon-only, found visible caption %q", a.Name)
			}
		}
		for _, tc := range []struct {
			label string
			page  state.Page
		}{{"抽卡分析", state.WishAnalysis}, {"每日签到", state.CheckIn}, {"游戏主页", state.Home}} {
			if clickFailed(tt, tc.label) {
				t.Fatalf("navigation %s inaccessible", tc.label)
			}
			a, _ := tt.AccessByRole(ui.RoleButton, tc.label)
			if s.Model.Page != tc.page || a.Current != ui.CurrentPage || !a.Focused {
				t.Fatalf("selected navigation lost current/focus semantics: %+v", a)
			}
		}
	}
}

func TestNativeSpineIsFullHeightAndMarksSelectionWithQuietFill(t *testing.T) {
	s := NewShell(settled(state.Ready))
	t.Cleanup(s.Dispose)
	tt := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(s.theme)
		ui.Box(c).Fill().Background(ui.Hex("#404040")).Children(func() { s.navigation(c) })
	}, 300, 640)
	for _, y := range []int{0, 639} {
		left, _, _, _ := tt.Image().At(1, y).RGBA()
		right, _, _, _ := tt.Image().At(120, y).RGBA()
		if left >= right {
			t.Fatalf("rail is not flush-left and full-height at y%d", y)
		}
	}
	for _, tc := range []struct {
		label string
		page  state.Page
	}{{"游戏主页", state.Home}, {"抽卡分析", state.WishAnalysis}, {"每日签到", state.CheckIn}} {
		s.Model.Page = tc.page
		tt.Frame()
		tt.Advance(time.Second)
		tt.Frame()
		r, _ := tt.Find(tc.label)
		if mark := tt.Image().RGBAAt(2, int(r.Y+r.H/2)); mark.R != 14 || mark.G != 15 || mark.B != 18 {
			t.Fatalf("selected navigation %s must not draw a bar, got %+v", tc.label, mark)
		}
		other, _ := tt.Find("抽卡分析")
		if tc.page == state.WishAnalysis {
			other, _ = tt.Find("游戏主页")
		}
		selected := tt.Image().RGBAAt(int(r.X+5), int(r.Y+r.H/2))
		idle := tt.Image().RGBAAt(int(other.X+5), int(other.Y+other.H/2))
		if selected.R <= idle.R || selected.G <= idle.G || selected.B <= idle.B {
			t.Fatalf("selected navigation %s needs the quiet fill: selected %+v idle %+v", tc.label, selected, idle)
		}
	}
}

func TestNativeColdWhiteSwitchRetainsHitAreaAndSemantics(t *testing.T) {
	s := NewShell(settled(state.Ready))
	t.Cleanup(s.Dispose)
	tt := ui.NewTester(s.View, 1280, 720)
	if clickFailed(tt, "启动器设置") || clickFailed(tt, "外观") {
		t.Fatal("settings unavailable")
	}
	sw, ok := tt.AccessByRole(ui.RoleSwitch, "减少动态效果")
	if !ok || sw.Bounds.W < 44 || sw.Bounds.H < 28 {
		t.Fatalf("custom switch needs a generous native hit area: %+v", sw)
	}
	if clickFailed(tt, "减少动态效果") || !s.Model.ReducedMotion {
		t.Fatal("custom face lost caption activation")
	}
	tt.Key(0, ui.KeySpace)
	if s.Model.ReducedMotion {
		t.Fatal("custom face lost keyboard activation")
	}
}
