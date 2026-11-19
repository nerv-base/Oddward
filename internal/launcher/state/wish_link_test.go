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

func TestWishLinkSavedCredentialNeedsConsentAndStaysOpenOnFailure(t *testing.T) {
	m := Model{Page: WishAnalysis}
	m.Wish.Link.AccountID = "saved"
	m, _, _ = ReduceWish(m, Event{Kind: WishLinkStart})
	next, effects, _ := ReduceWish(m, Event{Kind: WishLinkUseSaved})
	if len(effects) != 0 || next.Wish.Link.Busy {
		t.Fatal("saved credential used before consent")
	}
	m, _, _ = ReduceWish(m, Event{Kind: WishLinkAgree, Enabled: true})
	m, effects, _ = ReduceWish(m, Event{Kind: WishLinkUseSaved})
	if len(effects) != 1 || effects[0].Kind != TajiduoLoginSaved || !m.Wish.Link.Busy {
		t.Fatalf("saved credential did not start: effects=%+v state=%+v", effects, m.Wish.Link)
	}
	next, effects, _ = ReduceWish(m, Event{Kind: WishLinkFailed, Token: effects[0].Token, WishLinkFailure: WishLinkGameCredential})
	if len(effects) != 0 || next.Wish.Link.Busy || next.Wish.Link.Phase != WishLinking || next.Wish.Link.Step != WishLinkNotice {
		t.Fatalf("saved failure closed the dialog: %+v", next.Wish.Link)
	}
	if next.Wish.Link.Failure != WishLinkGameCredential {
		t.Fatalf("failure=%v", next.Wish.Link.Failure)
	}
	next, effects, _ = ReduceWish(next, Event{Kind: WishLinkContinue})
	if len(effects) != 0 || next.Wish.Link.Step != WishLinkPhone || next.Wish.Link.Failure != WishLinkFailureNone {
		t.Fatalf("SMS fallback kept stale failure: %+v effects=%+v", next.Wish.Link, effects)
	}
}

func TestWishFetchUsesLinkedRoleWhenArchiveIsEmpty(t *testing.T) {
	m := Model{Page: WishAnalysis}
	m.Wish.Link = WishLinkState{AccountID: "saved", Phase: WishLinked, RoleID: "218"}
	_, effects, _ := ReduceWish(m, Event{Kind: WishFetchRequest})
	if len(effects) != 1 || effects[0].Kind != WishFetch || effects[0].RoleID != "218" {
		t.Fatalf("empty archive did not fall back to linked role: %+v", effects)
	}
	m.Wish.RoleID = "999"
	_, effects, _ = ReduceWish(m, Event{Kind: WishFetchRequest})
	if len(effects) != 1 || effects[0].RoleID != "999" {
		t.Fatalf("selected archive role was not preserved: %+v", effects)
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
