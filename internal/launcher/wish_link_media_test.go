// SPDX-License-Identifier: GPL-3.0-or-later
package launcher

import (
	"context"
	"github.com/nerv-base/Oddward/internal/launcher/state"
	"testing"
)

type fakeWishLinkBackend struct{ calls chan string }

func (f *fakeWishLinkBackend) Status(context.Context) (WishLinkStatus, error) {
	return WishLinkStatus{AccountID: "fake-account", Linked: true}, nil
}
func (f *fakeWishLinkBackend) SendSMS(_ context.Context, id, phone string) error {
	f.calls <- "sms"
	return nil
}
func (f *fakeWishLinkBackend) Login(_ context.Context, id, phone, code string) error {
	f.calls <- "login"
	return nil
}
func (f *fakeWishLinkBackend) Roles(context.Context, string) ([]WishLinkRole, error) {
	return []WishLinkRole{{ID: "123", Name: "合成角色", Bound: true}}, nil
}
func (f *fakeWishLinkBackend) Bind(context.Context, string, string) error {
	f.calls <- "bind"
	return nil
}
func (f *fakeWishLinkBackend) Complete(context.Context, string, string) error {
	f.calls <- "complete"
	return nil
}
func (f *fakeWishLinkBackend) Cancel(context.Context, string) error { return nil }
func (f *fakeWishLinkBackend) Unlink(context.Context, string) error { return nil }
func (f *fakeWishLinkBackend) Fetch(context.Context, string, string) (WishImportReport, error) {
	f.calls <- "fetch"
	return WishImportReport{}, nil
}
func TestWishLinkWorkerReturnsOnlyThroughUIQueue(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	s.Model.Page = state.WishAnalysis
	f := &fakeWishLinkBackend{calls: make(chan string, 2)}
	s.WishLinkBackend = f
	s.Model.Wish.Link = state.WishLinkState{AccountID: "fake-account", Phase: state.WishLinking, Step: state.WishLinkPhone}
	s.wishLink.wasWish = true
	s.wishLink.phone, s.wishLink.code = "13800000000", "123456"
	s.Dispatch(state.Event{Kind: state.WishLinkSubmit})
	if got := <-f.calls; got != "login" {
		t.Fatalf("call=%s", got)
	}
	r := <-s.wishLink.out
	if s.Model.Wish.Link.Step != state.WishLinkPhone {
		t.Fatal("worker changed UI model")
	}
	if s.wishLink.code != "" {
		t.Fatal("code retained after submission")
	}
	s.wishLink.out <- r
	s.syncWishLink()
	roles := <-s.wishLink.out
	s.wishLink.out <- roles
	s.syncWishLink()
	if s.Model.Wish.Link.Step != state.WishLinkRole || len(s.wishLink.roles) != 1 {
		t.Fatal("UI did not apply role result")
	}
	select {
	case call := <-f.calls:
		t.Fatalf("automatic call: %s", call)
	default:
	}
}
func TestWishLinkStaleUIResultCannotReplaceRoleData(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	s.Model.Wish.Link.Token = 3
	s.wishLink.out <- wishLinkResult{effect: state.Effect{Kind: state.TajiduoRoles, Token: 2}, roles: []WishLinkRole{{ID: "stale"}}}
	s.syncWishLink()
	if len(s.wishLink.roles) != 0 {
		t.Fatal("stale result changed role list")
	}
}
