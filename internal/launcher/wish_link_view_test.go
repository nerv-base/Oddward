// SPDX-License-Identifier: GPL-3.0-or-later
package launcher

import (
	"github.com/egoist/mygo/ui"
	"github.com/nerv-base/Oddward/internal/launcher/state"
	"testing"
)

func TestWishLinkNoticeConsentAndEscape(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	s.Model.Page = state.WishAnalysis
	s.Dispatch(state.Event{Kind: state.WishLinkStatus, WishAccountID: "synthetic-account"})
	s.Dispatch(state.Event{Kind: state.WishLinkStart})
	tt := ui.NewTester(func(c *ui.Context) { c.SetTheme(s.theme); s.wishLinkOverlay(c) }, 1280, 720)
	if _, ok := tt.AccessByRole(ui.RoleDialog, "关联塔吉多"); !ok {
		t.Fatal("notice dialog missing")
	}
	tt.Click("继续")
	if s.Model.Wish.Link.Step != state.WishLinkNotice {
		t.Fatal("unchecked consent continued")
	}
	tt.Click("我已阅读并同意")
	tt.Click("继续")
	if s.Model.Wish.Link.Step != state.WishLinkPhone {
		t.Fatal("consent did not advance")
	}
	tt.Key(0, ui.KeyEscape)
	if s.Model.Wish.Link.Phase == state.WishLinking {
		t.Fatal("escape did not cancel")
	}
}
func TestWishLinkRoleListNeverBindsDuringRender(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	s.Model.Page = state.WishAnalysis
	s.Model.Wish.Link = state.WishLinkState{AccountID: "synthetic-account", Phase: state.WishLinking, Step: state.WishLinkRole}
	s.wishLink.roles = []WishLinkRole{{ID: "123", Name: "测试角色", Bound: true}, {ID: "456", Name: "另一个角色"}}
	for _, prefs := range []ui.Preferences{{}, {HighContrast: true}, {ReduceTransparency: true}} {
		tt := ui.NewTester(func(c *ui.Context) { c.SetTheme(s.theme); s.wishLinkOverlay(c) }, 1280, 720)
		tt.SetPreferences(prefs)
		tt.Frame()
		a, ok := tt.AccessByRole(ui.RoleButton, "选择已绑定角色 测试角色")
		if !ok || a.Bounds.W <= 0 {
			t.Fatal("bound role selection missing")
		}
		if _, ok := tt.AccessByRole(ui.RoleButton, "绑定 另一个角色"); !ok {
			t.Fatal("explicit bind missing")
		}
		if s.Model.Wish.Link.Busy {
			t.Fatal("render bound a role")
		}
	}
}
