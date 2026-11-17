// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func TestNormalLocatePanelHasDirectNativePickerAndNoPluginSetup(t *testing.T) {
	m := normalFixture(state.ManagedUnlocated)
	m.Managed.Root = ""
	m.Overlay = state.LocateGame
	m.ReducedMotion = true
	s := NewShell(m)
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1280, 720)
	for _, label := range []string{"浏览游戏目录", "定位此目录"} {
		if _, ok := tt.AccessByRole(ui.RoleButton, label); !ok {
			t.Fatalf("missing normal locate action %q", label)
		}
	}
	if _, ok := tt.Find("游戏目录"); !ok {
		t.Fatal("no direct path paste input")
	}
	for _, forbidden := range []string{"Plugin 目录（绝对路径）", "可信宿主 SHA256（可选）", "Profile（Release094 / PinnedMain）"} {
		if _, ok := tt.Find(forbidden); ok {
			t.Fatalf("normal locate still requires technical field %q", forbidden)
		}
	}
}
func TestNormalFirstLaunchDialogHasOneDecisionAndNoTechnicalConfig(t *testing.T) {
	m := normalFixture(state.ManagedReady)
	m.Overlay = state.LaunchNotice
	m.ReducedMotion = true
	s := NewShell(m)
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1280, 720)
	for _, label := range []string{"取消", "同意并启动"} {
		n, ok := tt.AccessByRole(ui.RoleButton, label)
		if !ok || n.Bounds.Y+n.Bounds.H > 680 {
			t.Fatalf("first-use action inaccessible %q: %+v", label, n)
		}
	}
	for _, fragment := range []string{"登录窗口随启动器提供", "并非官方授权", "账号限制或封禁风险"} {
		if !tt.HasText(fragment) {
			t.Fatalf("missing first-use risk boundary %q", fragment)
		}
	}
	if _, ok := tt.Find("Plugin 目录（绝对路径）"); ok {
		t.Fatal("first use returned to manual Plugin configuration")
	}
}
func TestNormalFixedCaptionRemovesMaximizeAction(t *testing.T) {
	m := normalFixture(state.ManagedReady)
	s := NewShell(m)
	defer s.Dispose()
	s.FixedWindow = true
	tt := ui.NewTester(s.View, 1280, 720)
	if _, ok := tt.AccessByRole(ui.RoleButton, "最大化或还原"); ok {
		t.Fatal("fixed launcher still exposes maximize callback")
	}
	for _, label := range []string{"最小化", "关闭窗口"} {
		if _, ok := tt.AccessByRole(ui.RoleButton, label); !ok {
			t.Fatalf("removed necessary caption action %q", label)
		}
	}
}
func TestNormalPrimaryFailureIsVisibleWithoutHover(t *testing.T) {
	m := normalFixture(state.ManagedFailed)
	m.Managed.Error = "synthetic component download failed"
	s := NewShell(m)
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1280, 720)
	if !tt.HasText(m.Managed.Error) {
		t.Fatal("normal startup error is hidden in tooltip only")
	}
	if tt.HasText("MOCK · 交互演示") {
		t.Fatal("ordinary startup still claims fake mode")
	}
}
