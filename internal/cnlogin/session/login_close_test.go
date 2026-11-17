// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package session

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nerv-base/Oddward/internal/cnlogin/accounts"
	"github.com/nerv-base/Oddward/internal/cnlogin/cnproto"
	"github.com/nerv-base/Oddward/internal/cnlogin/loginui"
)

// waitClosed observes the window contract rather than a transient success model.
func (u *fakeUI) waitClosed(t *testing.T) {
	t.Helper()
	select {
	case <-u.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("login window did not close after successful delivery")
	}
}

func requireLoginWindowOpen(t *testing.T, ui *fakeUI) {
	t.Helper()
	select {
	case <-ui.closed:
		t.Fatal("login window closed before successful delivery and account commit")
	default:
	}
}

// loginHostProbe gates the SDK boundary independently of the HTTP fixture.
// A nil SendLogin result represents the actual callback acknowledgement.
type loginHostProbe struct {
	Host
	ack          <-chan error
	ackErr       error
	replyGate    <-chan struct{}
	replyStarted chan context.Context
}

func (h *loginHostProbe) SendLogin(ctx context.Context, id cnproto.LoginIdentity) error {
	if err := h.Host.SendLogin(ctx, id); err != nil {
		return err
	}
	if h.ack != nil {
		select {
		case err := <-h.ack:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return h.ackErr
}

func (h *loginHostProbe) Reply(ctx context.Context, reply cnproto.PreLoginReply) error {
	if h.replyStarted != nil {
		h.replyStarted <- ctx
	}
	if h.replyGate != nil {
		select {
		case <-h.replyGate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return h.Host.Reply(ctx, reply)
}

type observedLoginUI struct {
	UI
	closes *atomic.Int32
}

func (u *observedLoginUI) Close() error {
	u.closes.Add(1)
	return u.UI.Close()
}

func observeLoginCloses(d *deps, closes *atomic.Int32) {
	open := d.ui
	d.ui = func(ctx context.Context, model loginui.Model) (UI, error) {
		ui, err := open(ctx, model)
		if err != nil {
			return nil, err
		}
		return &observedLoginUI{UI: ui, closes: closes}, nil
	}
}

type gatedCommitAccounts struct {
	*fakeAccounts
	started chan struct{}
	release <-chan struct{}
	err     error
}

func (s *gatedCommitAccounts) Prepare(a accounts.Account, secret accounts.Secret) (accounts.Pending, error) {
	pending, err := s.fakeAccounts.Prepare(a, secret)
	if err != nil {
		return nil, err
	}
	return &gatedLoginCommit{Pending: pending, store: s}, nil
}

type gatedLoginCommit struct {
	accounts.Pending
	store *gatedCommitAccounts
}

func (p *gatedLoginCommit) Commit() error {
	close(p.store.started)
	<-p.store.release
	if p.store.err != nil {
		return p.store.err
	}
	return p.Pending.Commit()
}

func TestLoginWindowWaitsForACKAndAccountCommit(t *testing.T) {
	for _, tc := range []struct {
		name      string
		commitErr error
		commits   int
		rollbacks int
	}{
		{name: "account saved", commits: 1},
		{name: "save failure leaves login successful", commitErr: errors.New("synthetic commit failure"), rollbacks: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := newFakeAccounts()
				store.ack = true
				commitRelease, replyRelease := make(chan struct{}), make(chan struct{})
				var commitOnce, replyOnce sync.Once
				defer commitOnce.Do(func() { close(commitRelease) })
				defer replyOnce.Do(func() { close(replyRelease) })
				gatedStore := &gatedCommitAccounts{fakeAccounts: store, started: make(chan struct{}), release: commitRelease, err: tc.commitErr}
				ack := make(chan error, 1)
				host := &loginHostProbe{ack: ack, replyGate: replyRelease, replyStarted: make(chan context.Context, 1)}
				var closes atomic.Int32
				rig := newRig(t, store, testPreLoginOK, func(d *deps) {
					d.host.(*fakeHost).queryOnSend = &cnproto.Query{Type: cnproto.QueryPreLogin, ContextID: 88}
					host.Host, d.host = d.host, host
					d.accounts = gatedStore
					observeLoginCloses(d, &closes)
				})
				rig.register(t)
				ui := rig.openLogin(t)
				ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "phone" })
				ui.events <- loginui.Event{Kind: "login_phone", Phone: testPhone, Captcha: "123456", Remember: true}
				<-rig.host.logins
				synctest.Wait()
				requireLoginWindowOpen(t, ui)
				if commits, _, _ := store.counts(); commits != 0 || len(rig.host.replies) != 0 {
					t.Fatal("account committed or query answered before SendLogin ACK")
				}

				ack <- nil
				<-gatedStore.started
				synctest.Wait()
				requireLoginWindowOpen(t, ui)
				if len(rig.host.replies) != 0 {
					t.Fatal("pending query answered before account commit finished")
				}

				commitOnce.Do(func() { close(commitRelease) })
				synctest.Wait()
				ui.waitClosed(t)
				if closes.Load() != 1 || rig.host.completions() != 1 {
					t.Fatalf("successful window closes=%d completions=%d, want 1/1", closes.Load(), rig.host.completions())
				}
				commits, rollbacks, prepared := store.counts()
				if commits != tc.commits || rollbacks != tc.rollbacks || prepared != 1 {
					t.Fatalf("account prepare/commit/rollback=%d/%d/%d", prepared, commits, rollbacks)
				}
				last, _ := store.Last()
				if (last == "new-account") != (tc.commitErr == nil) {
					t.Fatalf("last saved account=%q after commit error=%v", last, tc.commitErr)
				}
				replyCtx := <-host.replyStarted
				if err := replyCtx.Err(); err != nil {
					t.Fatalf("closing successful window cancelled pending query: %v", err)
				}
				replyOnce.Do(func() { close(replyRelease) })
				reply := <-rig.host.replies
				if reply.ContextID != 88 || reply.Code != 0 || string(reply.Body) != testPreLoginOK {
					t.Fatalf("pending pre-login reply lost after close: %+v", reply)
				}
			})
		})
	}
}

func TestFailedLoginKeepsWindowOpen(t *testing.T) {
	for _, tc := range []struct {
		name, channel, preLogin, status string
		ackErr                          error
		prepared                        int
	}{
		{name: "authentication rejected", channel: `{"code":17}`, preLogin: testPreLoginOK, status: "渠道账号登录未通过"},
		{name: "admission denied", channel: testChannelJSON, preLogin: testPreLoginNo, status: "实名", prepared: 1},
		{name: "callback rejected", channel: testChannelJSON, preLogin: testPreLoginOK, status: "登录失败", ackErr: errors.New("synthetic callback failure"), prepared: 1},
		{name: "callback timed out", channel: testChannelJSON, preLogin: testPreLoginOK, status: "登录已取消", ackErr: context.DeadlineExceeded, prepared: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				store := newFakeAccounts()
				store.ack = true
				rig := newRig(t, store, tc.preLogin, func(d *deps) {
					d.host.(*fakeHost).queryOnSend = &cnproto.Query{Type: cnproto.QueryPreLogin, ContextID: 91}
					d.host = &loginHostProbe{Host: d.host, ackErr: tc.ackErr}
				})
				rig.service.channel = tc.channel
				rig.register(t)
				ui := rig.openLogin(t)
				ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "phone" })
				ui.events <- loginui.Event{Kind: "login_phone", Phone: testPhone, Captcha: "123456", Remember: true}
				failed := ui.waitFor(t, func(m loginui.Model) bool { return strings.Contains(m.Status, tc.status) })
				synctest.Wait()
				requireLoginWindowOpen(t, ui)
				if failed.Busy || rig.host.completions() != 0 || len(rig.host.replies) != 0 {
					t.Fatal("failed login closed/completed the window, stayed busy, or replied with success")
				}
				commits, rollbacks, prepared := store.counts()
				if commits != 0 || prepared != tc.prepared || rollbacks != tc.prepared {
					t.Fatalf("failed login prepare/commit/rollback=%d/%d/%d", prepared, commits, rollbacks)
				}
			})
		})
	}
}

func TestClosedSuccessfulLoginAnswersLateQueriesAndAllowsLoginAfterLogout(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeAccounts()
		store.ack = true
		replyRelease := make(chan struct{})
		var release sync.Once
		defer release.Do(func() { close(replyRelease) })
		host := &loginHostProbe{replyGate: replyRelease, replyStarted: make(chan context.Context, 2)}
		var closes atomic.Int32
		rig := newRig(t, store, testPreLoginOK, func(d *deps) {
			host.Host, d.host = d.host, host
			observeLoginCloses(d, &closes)
		})
		rig.register(t)
		first := rig.openLogin(t)
		first.waitFor(t, func(m loginui.Model) bool { return m.Screen == "phone" })
		first.events <- loginui.Event{Kind: "login_phone", Phone: testPhone, Captcha: "123456"}
		synctest.Wait()
		first.waitClosed(t)

		rig.host.events <- HostEvent{Kind: HostQuery, Query: cnproto.Query{Type: cnproto.QueryPreLogin, ContextID: 77}}
		synctest.Wait()
		replyCtx := <-host.replyStarted
		if err := replyCtx.Err(); err != nil {
			t.Fatalf("late query inherited a cancelled window context: %v", err)
		}
		for range 2 {
			rig.host.events <- HostEvent{Kind: HostLoginRequested}
		}
		synctest.Wait()
		if len(rig.uiCreated) != 0 || closes.Load() != 1 {
			t.Fatalf("successful UI reopened or closed twice: new windows=%d closes=%d", len(rig.uiCreated), closes.Load())
		}
		release.Do(func() { close(replyRelease) })
		firstReply := <-rig.host.replies
		if firstReply.ContextID != 77 || firstReply.Code != 0 || string(firstReply.Body) != testPreLoginOK {
			t.Fatalf("first late reply=%+v", firstReply)
		}
		rig.host.events <- HostEvent{Kind: HostQuery, Query: cnproto.Query{Type: cnproto.QueryPreLogin, ContextID: 78}}
		secondReply := <-rig.host.replies
		if secondReply.ContextID != 78 || secondReply.Code != 0 || string(secondReply.Body) != testPreLoginOK {
			t.Fatalf("cached result was not retained for another query: %+v", secondReply)
		}

		rig.host.events <- HostEvent{Kind: HostLogout}
		second := rig.openLogin(t)
		model := second.waitFor(t, func(m loginui.Model) bool { return m.Screen == "phone" })
		if model.Busy {
			t.Fatal("logout did not restore usable login controls")
		}
		second.events <- loginui.Event{Kind: "login_phone", Phone: testPhone, Captcha: "123456"}
		synctest.Wait()
		second.waitClosed(t)
		if closes.Load() != 2 || len(rig.host.logins) != 2 {
			t.Fatalf("second login: closes=%d identities=%d, want 2/2", closes.Load(), len(rig.host.logins))
		}
	})
}

func TestSuccessfulLoginClosesWindowWithoutPreLoginQuery(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeAccounts()
		store.ack = true
		rig := newRig(t, store, testPreLoginOK)
		rig.register(t)
		ui := rig.openLogin(t)
		ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "phone" })
		ui.events <- loginui.Event{Kind: "login_phone", Phone: testPhone, Captcha: "123456"}
		synctest.Wait()

		select {
		case <-ui.closed:
		default:
			t.Fatal("login window stayed open after SendLogin succeeded without a pre-login query")
		}
		if rig.host.completions() != 1 {
			t.Fatalf("completed login requests=%d, want 1", rig.host.completions())
		}
		rig.host.mu.Lock()
		hostClosed := rig.host.closed
		rig.host.mu.Unlock()
		rig.game.mu.Lock()
		gameClosed := rig.game.closed
		rig.game.mu.Unlock()
		if hostClosed || gameClosed || rig.session.ctx.Err() != nil {
			t.Fatal("closing the successful login window stopped the SDK/game session")
		}
	})
}
