// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func TestLauncherSettingsSwitchesSectionsInPlace(t *testing.T) {
	m := normalFixture(state.ManagedReady)
	m.ReducedMotion = true
	s := NewShell(m)
	t.Cleanup(s.Dispose)
	tt := ui.NewTester(s.View, 1024, 631)
	for _, step := range []struct{ click, text string }{
		{"启动器设置", "关闭窗口时最小化到托盘"},
		{"外观", "动态背景"},
		{"下载与网络", "跟随系统"},
		{"文件", "游戏安装"},
		{"关于", "GPL-3.0-or-later"},
		{"常规", "关闭窗口时最小化到托盘"},
	} {
		if err := tt.Click(step.click); err != nil {
			t.Fatal(err)
		}
		if s.Model.Overlay != state.NoOverlay || s.Model.Page != state.LauncherSettings || !tt.HasText(step.text) {
			t.Fatalf("%s must show %q in the launcher settings page, page=%v overlay=%v", step.click, step.text, s.Model.Page, s.Model.Overlay)
		}
		if _, ok := tt.AccessByRole(ui.RoleDialog, "启动器设置"); ok {
			t.Fatal("launcher settings must not be modal")
		}
	}
	if err := tt.Click("游戏主页"); err != nil || s.Model.Page != state.Home {
		t.Fatalf("the main rail must stay actionable in settings: %v, page=%v", err, s.Model.Page)
	}
	if err := tt.Click("启动器设置"); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"背景来源", "状态预览", "开发工具 · 仅模拟交互流程"} {
		if tt.HasText(label) {
			t.Errorf("regular settings expose %q", label)
		}
	}
}

func TestLauncherSettingsOnlyShowsLauncherControls(t *testing.T) {
	m := normalFixture(state.ManagedReady)
	m.ReducedMotion = true
	s := NewShell(m)
	t.Cleanup(s.Dispose)
	tt := ui.NewTester(s.View, 1024, 631)
	if err := tt.Click("启动器设置"); err != nil {
		t.Fatal(err)
	}
	if s.Model.Page != state.LauncherSettings || s.Model.Overlay != state.NoOverlay {
		t.Fatal("launcher settings must occupy its own page")
	}
	for _, want := range []struct {
		role  ui.Role
		label string
	}{
		{ui.RoleTab, "常规"}, {ui.RoleTab, "外观"}, {ui.RoleTab, "下载与网络"}, {ui.RoleTab, "文件"}, {ui.RoleTab, "关于"},
	} {
		if _, ok := tt.AccessByRole(want.role, want.label); !ok {
			t.Errorf("missing launcher settings control %q", want.label)
		}
	}
	if err := tt.Click("外观"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		role  ui.Role
		label string
	}{
		{ui.RoleSwitch, "动态背景"}, {ui.RoleSwitch, "减少动态效果"}, {ui.RoleSwitch, "强调色跟随版本主视觉"},
	} {
		if _, ok := tt.AccessByRole(want.role, want.label); !ok {
			t.Errorf("missing launcher settings control %q", want.label)
		}
	}
	for _, label := range []string{"下载设置", "高级启动诊断", "启动 Plugin 宿主", "使用 Plugin 模式"} {
		if _, ok := tt.Find(label); ok {
			t.Errorf("launcher settings exposes %q", label)
		}
	}
	if _, ok := tt.AccessByRole(ui.RoleButton, "游戏目录"); ok {
		t.Fatal("launcher settings exposes the game directory picker")
	}
}

func TestLauncherUpdateDetailsStayBesideRail(t *testing.T) {
	m := normalFixture(state.ManagedReady)
	m.ReducedMotion = true
	m.Update.Phase, m.Update.Version = state.SelfUpdateAvailable, "0.2.0"
	s := NewShell(m)
	t.Cleanup(s.Dispose)
	tt := ui.NewTester(s.View, 1024, 631)
	for _, label := range []string{"启动器设置", "关于", "查看更新"} {
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
	}
	wantSettingsDrawer(t, tt, "Oddward 更新", railWidth+sheetInset)
}

func wantSettingsDrawer(t *testing.T, tt *ui.Tester, label string, left float32) {
	t.Helper()
	dialog, ok := tt.AccessByRole(ui.RoleDialog, label)
	if !ok || dialog.Bounds.X != left {
		t.Fatalf("expected %q at X=%g, found=%t bounds=%+v", label, left, ok, dialog.Bounds)
	}
}

func TestHomeGameSettingsRoutes(t *testing.T) {
	m := normalFixture(state.ManagedReady)
	m.ReducedMotion = true
	s := NewShell(m)
	t.Cleanup(s.Dispose)
	tt := ui.NewTester(s.View, 1024, 631)
	click := func(label string) {
		t.Helper()
		button, ok := tt.AccessByRole(ui.RoleButton, label)
		if !ok || button.Disabled {
			t.Fatalf("button %q unavailable", label)
		}
		r := button.Bounds
		tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
	}
	var shell ui.Rect
	wantDialog := func(label string) {
		t.Helper()
		dialog, ok := tt.AccessByRole(ui.RoleDialog, label)
		r := dialog.Bounds
		if !ok || r.X < 24 || r.Y < 24 || !near(r.X+r.W/2, 512, 1) || !near(r.Y+r.H/2, 315.5, 1) {
			t.Fatalf("expected %q centered in the whole 1024×631 window, found=%t bounds=%+v", label, ok, r)
		}
		if label == "游戏设置" {
			if !near(r.W, 683, 12) || !near(r.H, 454, 12) {
				t.Fatalf("expected game settings to occupy about two-thirds of the width and 72%% of the height, bounds=%+v", r)
			}
			if shell == (ui.Rect{}) {
				shell = r
			} else if shell != r {
				t.Fatalf("game settings changed bounds from %+v to %+v", shell, r)
			}
		}
	}

	click("游戏设置")
	wantDialog("游戏设置")
	if _, ok := tt.AccessByRole(ui.RoleTab, "基本信息"); ok {
		t.Fatal("game settings is a single page now; it must not carry launcher-style tabs")
	}
	path, hasPath := tt.Find(m.Managed.Root)
	if !hasPath || path.X+path.W > shell.X+shell.W {
		t.Fatalf("expected the known game path inside the dialog, found=%t bounds=%+v", hasPath, path)
	}
	for _, label := range []string{"动态背景", "减少动态效果", "检查更新", "网络", "关于", "状态预览", "下载设置"} {
		if _, ok := tt.Find(label); ok {
			t.Errorf("game settings exposes launcher control %q", label)
		}
	}
	click("定位游戏")
	wantDialog("游戏设置")
	if browse, ok := tt.AccessByRole(ui.RoleButton, "浏览游戏目录"); !ok || browse.Disabled || browse.Bounds.X < shell.X || browse.Bounds.X+browse.Bounds.W > shell.X+shell.W {
		t.Fatalf("game settings must show the existing directory picker: found=%t browse=%+v path=%+v shell=%+v", ok, browse.Bounds, path, shell)
	}
	// A previous directory check can report a long diagnostic. Actions should
	// stay visible while the message scrolls in the dialog body.
	detail := "无法检查所选目录，请查看游戏目录或重新选择已有安装。 "
	for range 5 {
		detail += detail
	}
	s.Model.Managed.Error = detail
	tt.Frame()
	if apply, ok := tt.AccessByRole(ui.RoleButton, "定位此目录"); !ok || apply.Bounds.Y+apply.Bounds.H > shell.Y+shell.H-24 {
		t.Fatalf("directory settings must show its locate action when a long error is present, found=%t bounds=%+v", ok, apply.Bounds)
	}
	click("返回")
	s.Model.Managed.Error = ""
	wantDialog("游戏设置")
	click("关闭面板")
	if s.Model.Overlay != state.NoOverlay || s.Model.Page != state.Home {
		t.Fatalf("closing game settings must return to home, overlay=%v page=%v", s.Model.Overlay, s.Model.Page)
	}

	click("游戏设置")
	tt.Key(0, ui.KeyEscape)
	if s.Model.Overlay != state.NoOverlay || s.Model.Page != state.Home {
		t.Fatalf("Escape must close the modal and return to home, overlay=%v page=%v", s.Model.Overlay, s.Model.Page)
	}
	click("游戏设置")
	tt.ClickAt(1, 1)
	if s.Model.Overlay != state.NoOverlay || s.Model.Page != state.Home {
		t.Fatalf("a backdrop click must dismiss the modal, overlay=%v page=%v", s.Model.Overlay, s.Model.Page)
	}
}
