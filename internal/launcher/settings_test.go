// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func TestSettingsExposeOnlySupportedLaunchActions(t *testing.T) {
	for _, mode := range []struct {
		name   string
		normal bool
	}{{"demo", false}, {"normal", true}} {
		t.Run(mode.name, func(t *testing.T) {
			m := settled(state.Ready)
			if mode.normal {
				m = normalFixture(state.ManagedReady)
			}
			m.ReducedMotion = true
			s := NewShell(m)
			t.Cleanup(s.Dispose)
			tt := ui.NewTester(s.View, 1024, 631)
			if err := tt.Click("设置"); err != nil {
				t.Fatal(err)
			}
			tt.Frame()
			if _, ok := tt.AccessByRole(ui.RoleDialog, "设置"); !ok {
				t.Fatal("settings did not open")
			}
			for _, label := range []string{"高级启动诊断", "启动 Plugin 宿主", "使用 Plugin 模式"} {
				if _, ok := tt.Find(label); ok {
					t.Errorf("settings exposes removed launch action %q", label)
				}
			}
			if _, ok := tt.AccessByRole(ui.RoleButton, "游戏目录"); ok != mode.normal {
				t.Errorf("game location entry = %v, want normal mode %v", ok, mode.normal)
			}
			for _, label := range []string{"状态预览", "网络", "关于"} {
				if _, ok := tt.AccessByRole(ui.RoleButton, label); !ok {
					t.Errorf("missing supported settings action %q", label)
				}
			}
			if mode.normal {
				// Target the modal's accessible control; the covered home button
				// has the same label and is also present in the render tree.
				entry, ok := tt.AccessByRole(ui.RoleButton, "游戏目录")
				if !ok {
					t.Fatal("game location entry missing")
				}
				r := entry.Bounds
				tt.ClickAt(r.X+r.W/2, r.Y+r.H/2)
				if _, ok := tt.AccessByRole(ui.RoleButton, "浏览游戏目录"); !ok {
					t.Fatal("game location no longer reaches the native directory picker")
				}
			}
		})
	}
}
