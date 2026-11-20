// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package session

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/cnlogin/accounts"
	"github.com/nerv-base/Oddward/internal/cnlogin/laohu"
	"github.com/nerv-base/Oddward/internal/cnlogin/loginui"
)

// savedAccount is one acknowledged, selected account whose credential unlocks.
func savedAccount() *fakeAccounts {
	store := newFakeAccounts()
	store.ack = true
	store.accounts = []accounts.Account{{ID: "account-1", Label: "夜航星"}}
	store.last = "account-1"
	store.unlock["account-1"] = accounts.Secret{DeviceID: testDeviceID, ChannelUID: "42", ChannelToken: "synthetic-token"}
	return store
}

func TestAutoLoginUsesLastSavedAccount(t *testing.T) {
	store := savedAccount()
	rig := newRig(t, store, testPreLoginOK)
	rig.register(t)
	rig.request(t)

	banner := rig.banner(t)
	model := banner.waitFor(t, func(m loginui.Model) bool { return m.Screen == "welcome" })
	if model.Banner != "夜航星，欢迎登录" {
		t.Fatalf("banner = %q", model.Banner)
	}
	identity := rig.game.Identity()
	if model.GameProcessID != identity.PID || model.GameCreationTime != strconv.FormatUint(identity.CreationTime, 10) {
		t.Fatalf("banner identity = %d/%q", model.GameProcessID, model.GameCreationTime)
	}
	select {
	case <-rig.uiCreated:
		t.Fatal("login window opened during an automatic login")
	default:
	}

	select {
	case delivered := <-rig.host.logins:
		if string(delivered.GameUID) != "42" || string(delivered.ChannelUID) != "42" {
			t.Fatalf("delivered identity = %q/%q", delivered.GameUID, delivered.ChannelUID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("identity was not delivered")
	}
	if completions := rig.host.completions(); completions != 1 {
		t.Fatalf("login request completions = %d, want 1", completions)
	}
}

func TestAutoLoginFailureOpensSavedAccountList(t *testing.T) {
	store := savedAccount()
	store.unlockErr = accounts.ErrUnlock
	rig := newRig(t, store, testPreLoginOK)
	rig.register(t)
	rig.request(t)

	ui := rig.window(t)
	reason := ui.waitFor(t, func(m loginui.Model) bool {
		return m.Screen == "accounts" && strings.Contains(m.Status, "凭据不可用")
	})
	if reason.Busy {
		t.Fatal("window still busy after the automatic login failed")
	}
	select {
	case <-rig.bannerCreated:
		t.Fatal("banner was shown without a delivered login")
	default:
	}
	rig.noLogin(t)
	if completions := rig.host.completions(); completions != 0 {
		t.Fatalf("login request completions = %d, want 0", completions)
	}
}

func TestAutoLoginServiceRejectionFallsBackToPhone(t *testing.T) {
	store := savedAccount()
	rig := newRig(t, store, testPreLoginOK)
	rig.service.validateStatus = http.StatusForbidden
	rig.register(t)
	rig.request(t)

	ui := rig.window(t)
	rejected := ui.waitFor(t, func(m loginui.Model) bool {
		return m.Screen == "phone" && strings.Contains(m.Status, "拒绝")
	})
	if rejected.SelectedAccountID != "" {
		t.Fatalf("selected account kept after falling back: %q", rejected.SelectedAccountID)
	}
	rig.noLogin(t)
}

func TestFirstLoginAfterNoticeDoesNotAutoLogin(t *testing.T) {
	store := savedAccount()
	store.ack = false
	rig := newRig(t, store, testPreLoginOK)
	rig.register(t)
	ui := rig.openLogin(t)
	ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "notice" })
	select {
	case <-rig.bannerCreated:
		t.Fatal("banner was shown before the notice was acknowledged")
	default:
	}

	ui.events <- loginui.Event{Kind: "acknowledge"}
	listed := ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "accounts" })
	if listed.SelectedAccountID != "account-1" {
		t.Fatalf("selected account = %q", listed.SelectedAccountID)
	}

	ui.events <- loginui.Event{Kind: "login_saved", AccountID: "account-1"}
	ui.waitClosed(t)
	banner := rig.banner(t)
	model := banner.waitFor(t, func(m loginui.Model) bool { return m.Screen == "welcome" })
	if model.Banner != "夜航星，欢迎登录" {
		t.Fatalf("banner = %q", model.Banner)
	}
}

func TestPhoneLoginShowsWelcomeBanner(t *testing.T) {
	store := newFakeAccounts()
	store.ack = true
	rig := newRig(t, store, testPreLoginOK)
	rig.register(t)
	ui := rig.openLogin(t)
	ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "phone" })

	ui.events <- loginui.Event{Kind: "send_sms", Phone: testPhone}
	ui.waitFor(t, func(m loginui.Model) bool { return m.Status == "验证码已发送，请查收短信。" })
	ui.events <- loginui.Event{Kind: "login_phone", Phone: testPhone, Captcha: "123456"}
	ui.waitClosed(t)

	label, err := laohu.MaskPhone(testPhone)
	if err != nil {
		t.Fatal(err)
	}
	banner := rig.banner(t)
	model := banner.waitFor(t, func(m loginui.Model) bool { return m.Screen == "welcome" })
	if model.Banner != label+"，欢迎登录" {
		t.Fatalf("banner = %q, want %q", model.Banner, label+"，欢迎登录")
	}
}

func TestLogoutClosesWelcomeBannerAndDisarmsAutoLogin(t *testing.T) {
	store := savedAccount()
	rig := newRig(t, store, testPreLoginOK)
	rig.register(t)
	rig.request(t)
	banner := rig.banner(t)
	banner.waitFor(t, func(m loginui.Model) bool { return m.Screen == "welcome" })
	select {
	case <-rig.host.logins:
	case <-time.After(2 * time.Second):
		t.Fatal("identity was not delivered")
	}

	rig.host.events <- HostEvent{Kind: HostLogout}
	select {
	case <-banner.closed:
	case <-time.After(3 * time.Second):
		t.Fatal("welcome banner was not closed on logout")
	}

	// After an explicit logout the next request asks again instead of replaying
	// the saved account.
	rig.request(t)
	ui := rig.window(t)
	ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "accounts" })
	rig.noLogin(t)
}

func TestBannerFailureDoesNotFailLogin(t *testing.T) {
	store := savedAccount()
	store.last = ""
	rig := newRig(t, store, testPreLoginOK, func(d *deps) {
		d.welcome = func(context.Context, loginui.Model) (UI, error) {
			return nil, errors.New("banner helper unavailable")
		}
	})
	rig.register(t)
	ui := rig.openLogin(t)
	ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "accounts" })
	ui.events <- loginui.Event{Kind: "login_saved", AccountID: "account-1"}
	ui.waitClosed(t)

	select {
	case delivered := <-rig.host.logins:
		if string(delivered.GameUID) != "42" {
			t.Fatalf("delivered uid = %q", delivered.GameUID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("identity was not delivered")
	}
	if completions := rig.host.completions(); completions != 1 {
		t.Fatalf("login request completions = %d, want 1", completions)
	}
}
