// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"context"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func TestHomeAccountRowListsAndSwitchesTheNextAccount(t *testing.T) {
	m := normalFixture(state.ManagedReady)
	m.Flow = state.Ready
	s := NewShell(m)
	defer s.Dispose()
	var remembered string
	s.HomeAccountUse = func(_ context.Context, id string) error { remembered = id; return nil }
	s.Dispatch(state.Event{Kind: state.HomeAccountsLoaded, Path: "a", Accounts: []state.SavedAccount{
		{ID: "a", Label: "138****0001"},
		{ID: "b", Label: "夜航星"},
	}})
	if s.Model.HomeAccounts.Count != 2 {
		t.Fatalf("state count = %d", s.Model.HomeAccounts.Count)
	}
	tt := ui.NewTester(s.View, 1280, 720)
	tt.Frame()
	if !tt.HasText("当前账号 138****0001") {
		t.Fatal("account row missing")
	}
	if clickFailed(tt, "当前账号 138****0001") {
		t.Fatal("account row not clickable")
	}
	tt.Frame()
	if !tt.HasText("下次使用的账号") || !tt.HasText("夜航星") {
		t.Fatal("account popup missing")
	}
	if clickFailed(tt, "夜航星") {
		t.Fatal("saved account not clickable")
	}
	tt.Frame()
	if s.Model.HomeAccounts.Current != "b" {
		t.Fatalf("current = %q", s.Model.HomeAccounts.Current)
	}
	if s.Model.HomeAccounts.Open {
		t.Fatal("popup stayed open after choosing")
	}
	if remembered != "b" {
		t.Fatalf("remembered = %q", remembered)
	}
	if !tt.HasText("当前账号 夜航星") {
		t.Fatal("row did not follow the choice")
	}
}

func TestHomeAccountRowHiddenWithoutSavedAccounts(t *testing.T) {
	m := normalFixture(state.ManagedReady)
	m.Flow = state.Ready
	s := NewShell(m)
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1280, 720)
	tt.Frame()
	if tt.HasText("当前账号") || tt.HasText("下次使用的账号") {
		t.Fatal("empty account row must not render")
	}
}
