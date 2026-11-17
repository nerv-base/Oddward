// SPDX-License-Identifier: GPL-3.0-or-later
package state

import "testing"

func TestWishLinkStartClearsStaleCodeAndHonorsWishBusy(t *testing.T) {
	m := Model{Page: WishAnalysis}
	m.Wish.Link = WishLinkState{AccountID: "saved", CodeSent: true}
	next, effects, _ := ReduceWish(m, Event{Kind: WishLinkStart})
	if len(effects) != 0 || next.Wish.Link.Phase != WishLinking || next.Wish.Link.CodeSent {
		t.Fatalf("start must reset a stale code: %+v effects=%+v", next.Wish.Link, effects)
	}
	m.Wish.Link = WishLinkState{AccountID: "saved"}
	m.Wish.Busy = true
	next, effects, _ = ReduceWish(m, Event{Kind: WishLinkStart})
	if len(effects) != 0 || next.Wish.Link.Phase != WishLinkNone {
		t.Fatal("start must wait for the wish archive to stop loading")
	}
}

func TestWishLinkConsentAndNoAccount(t *testing.T) {
	m := Model{Page: WishAnalysis}
	// An account-free page must never start Tajiduo work.
	next, effects, _ := ReduceWish(m, Event{Kind: WishLinkStart})
	if len(effects) != 0 || next.Wish.Link.Phase != WishLinkNone {
		t.Fatal("account-free link started")
	}
	m.Wish.Link.AccountID = "saved"
	m, _, _ = ReduceWish(m, Event{Kind: WishLinkStart})
	m, effects, _ = ReduceWish(m, Event{Kind: WishLinkContinue})
	if m.Wish.Link.Step != WishLinkNotice || len(effects) != 0 {
		t.Fatal("unchecked consent advanced")
	}
	m, _, _ = ReduceWish(m, Event{Kind: WishLinkAgree, Enabled: true})
	m, effects, _ = ReduceWish(m, Event{Kind: WishLinkContinue})
	if m.Wish.Link.Step != WishLinkPhone || len(effects) != 0 {
		t.Fatal("consent did not reach phone without I/O")
	}
}
