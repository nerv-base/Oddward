// SPDX-License-Identifier: GPL-3.0-or-later

package state

import "testing"

func TestHomeAccountsLoadFillsTheRowAndCurrentChoice(t *testing.T) {
	m := Initial()
	next, effects := Reduce(m, Event{Kind: HomeAccountsLoad})
	if len(effects) != 1 || effects[0].Kind != LoadHomeAccounts {
		t.Fatalf("load effects = %+v", effects)
	}
	accounts := []SavedAccount{{ID: "a", Label: "138****0001"}, {ID: "b", Label: "备用"}}
	next, effects = Reduce(next, Event{Kind: HomeAccountsLoaded, Accounts: accounts, Path: "b"})
	if len(effects) != 0 {
		t.Fatalf("loaded effects = %+v", effects)
	}
	if next.HomeAccounts.Count != 2 || next.HomeAccounts.Current != "b" {
		t.Fatalf("home accounts = %+v", next.HomeAccounts)
	}
	if next.HomeAccounts.Accounts[1].Label != "备用" {
		t.Fatalf("second account = %+v", next.HomeAccounts.Accounts[1])
	}
}

func TestHomeAccountsLoadedCapsAtSixteen(t *testing.T) {
	m := Initial()
	many := make([]SavedAccount, MaxHomeAccounts+3)
	for i := range many {
		many[i] = SavedAccount{ID: string(rune('a' + i)), Label: "x"}
	}
	m, _ = Reduce(m, Event{Kind: HomeAccountsLoaded, Accounts: many})
	if m.HomeAccounts.Count != MaxHomeAccounts {
		t.Fatalf("count = %d", m.HomeAccounts.Count)
	}
	if m.HomeAccounts.Accounts[MaxHomeAccounts-1].ID != string(rune('a'+MaxHomeAccounts-1)) {
		t.Fatalf("last kept = %+v", m.HomeAccounts.Accounts[MaxHomeAccounts-1])
	}
}

func TestHomeAccountsToggleOnlyOnHomeWithoutOverlay(t *testing.T) {
	m := Initial()
	m.Page = LauncherSettings
	m, _ = Reduce(m, Event{Kind: HomeAccountsToggle})
	if m.HomeAccounts.Open {
		t.Fatal("opened over launcher settings")
	}
	m.Page = Home
	m.Overlay = ConfirmQuit
	m, _ = Reduce(m, Event{Kind: HomeAccountsToggle})
	if m.HomeAccounts.Open {
		t.Fatal("opened under an overlay")
	}
	m.Overlay = NoOverlay
	m, _ = Reduce(m, Event{Kind: HomeAccountsToggle})
	if !m.HomeAccounts.Open {
		t.Fatal("toggle did not open on home")
	}
	m, _ = Reduce(m, Event{Kind: HomeAccountsClose})
	if m.HomeAccounts.Open {
		t.Fatal("close did not close")
	}
}

func TestHomeAccountUseRemembersTheChoice(t *testing.T) {
	m := Initial()
	m.HomeAccounts.Count, m.HomeAccounts.Accounts[0] = 1, SavedAccount{ID: "id-1", Label: "138****0001"}
	m.HomeAccounts.Open = true
	m, effects := Reduce(m, Event{Kind: HomeAccountUse, Index: 0})
	if len(effects) != 1 || effects[0].Kind != RememberHomeAccount || effects[0].Path != "id-1" {
		t.Fatalf("use effects = %+v", effects)
	}
	if m.HomeAccounts.Current != "id-1" || m.HomeAccounts.Open {
		t.Fatalf("after use = %+v", m.HomeAccounts)
	}
	if _, again := Reduce(m, Event{Kind: HomeAccountUse, Index: 5}); len(again) != 0 {
		t.Fatalf("out of range emitted %+v", again)
	}
}
