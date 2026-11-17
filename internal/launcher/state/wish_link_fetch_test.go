// SPDX-License-Identifier: GPL-3.0-or-later
package state

import (
	"testing"
	"time"
)

func TestWishCancelRelinkRestoresExpiredAttachment(t *testing.T) {
	m := Model{Page: WishAnalysis}
	m.Wish.Link = WishLinkState{AccountID: "saved", Phase: WishNeedsRelink}
	m, _, _ = ReduceWish(m, Event{Kind: WishLinkStart})
	m, _, _ = ReduceWish(m, Event{Kind: WishLinkCancel})
	if m.Wish.Link.Phase != WishNeedsRelink {
		t.Fatal("cancel lost expired attachment status")
	}
}
func TestWishFetchExplicitCooldownAndStale(t *testing.T) {
	now := time.Unix(1000, 0)
	m := Model{Page: WishAnalysis}
	m.Wish.Link = WishLinkState{AccountID: "saved", Phase: WishLinked}
	m, fx, _ := ReduceWish(m, Event{Kind: WishFetchRequest, Now: now})
	if len(fx) != 1 || fx[0].Kind != WishFetch || m.Wish.Link.Fetch != WishFetching {
		t.Fatal("explicit fetch missing")
	}
	token := fx[0].Token
	next, fx, _ := ReduceWish(m, Event{Kind: WishFetchRequest, Now: now.Add(time.Minute)})
	if len(fx) != 0 || next != m {
		t.Fatal("concurrent fetch")
	}
	next, _, _ = ReduceWish(m, Event{Kind: WishFetchDone, Token: token + 1})
	if next != m {
		t.Fatal("stale fetch changed state")
	}
	m, _, _ = ReduceWish(m, Event{Kind: WishFetchDone, Token: token})
	_, fx, _ = ReduceWish(m, Event{Kind: WishFetchRequest, Now: now.Add(59 * time.Second)})
	if len(fx) != 0 {
		t.Fatal("cooldown bypass")
	}
	_, fx, _ = ReduceWish(m, Event{Kind: WishFetchRequest, Now: now.Add(time.Minute)})
	if len(fx) != 1 {
		t.Fatal("cooldown never ends")
	}
}
func TestWishCancelFencesLoginAndRelinkNeverSendsSMS(t *testing.T) {
	m := Model{Page: WishAnalysis}
	m.Wish.Link = WishLinkState{AccountID: "saved", Phase: WishLinking, Step: WishLinkPhone}
	m, fx, _ := ReduceWish(m, Event{Kind: WishLinkSubmit})
	if len(fx) != 1 || fx[0].Kind != TajiduoLogin {
		t.Fatal("login missing")
	}
	token := fx[0].Token
	m, fx, _ = ReduceWish(m, Event{Kind: WishLinkCancel})
	if len(fx) != 1 || fx[0].Kind != TajiduoCancel {
		t.Fatal("pending login not discarded")
	}
	next, fx, _ := ReduceWish(m, Event{Kind: WishLinkLoginDone, Token: token})
	if next != m || len(fx) != 0 {
		t.Fatal("cancelled login resurrected")
	}
	m.Wish.Link.Phase = WishNeedsRelink
	m, fx, _ = ReduceWish(m, Event{Kind: WishFetchRequest, Now: time.Unix(1000, 0)})
	if len(fx) != 0 || m.Wish.Link.Phase != WishNeedsRelink {
		t.Fatal("relink sent background request")
	}
}
