// SPDX-License-Identifier: GPL-3.0-or-later

package state

import (
	"testing"
	"time"
)

func loginRequested(t *testing.T, saved ...SavedLogin) Model {
	t.Helper()
	m := InitialNormal()
	m, fx := Reduce(m, Event{Kind: LoginRequested, Logins: saved})
	if len(fx) != 0 {
		t.Fatalf("a login request must not cause work: %+v", fx)
	}
	return m
}

func TestLoginRequestOpensSavedAccountsOrThePhoneForm(t *testing.T) {
	m := loginRequested(t)
	if m.Login.Phase != LoginPhone {
		t.Fatalf("no saved accounts must open the phone form, got %v", m.Login.Phase)
	}
	m = loginRequested(t, SavedLogin{ID: "a", Label: "138****0000"}, SavedLogin{ID: "b", Label: "139****1111"})
	if m.Login.Phase != LoginChoose || m.Login.Count != 2 || m.Login.Accounts[1].Label != "139****1111" || m.Login.Selected != 0 {
		t.Fatalf("saved accounts must open the picker with the first selected: %+v", m.Login)
	}
	if !LoginOpen(m.Login.Phase) {
		t.Fatal("picker must count as an open login window")
	}
}

func TestLoginPickerSelectsSubmitsAndFallsBackToPhone(t *testing.T) {
	m := loginRequested(t, SavedLogin{ID: "a", Label: "A"}, SavedLogin{ID: "b", Label: "B"})
	m, _ = Reduce(m, Event{Kind: LoginSelect, Index: 1})
	m, fx := Reduce(m, Event{Kind: LoginSubmitSaved})
	if m.Login.Phase != LoginVerifying || len(fx) != 1 || fx[0].Kind != SubmitSavedLogin || fx[0].Path != "b" || fx[0].Token != m.Login.Request {
		t.Fatalf("saved login must submit the selected id: %+v %+v", m.Login, fx)
	}
	m, fx = Reduce(m, Event{Kind: LoginSubmitSaved})
	if len(fx) != 0 {
		t.Fatal("a second submit while verifying must be ignored")
	}
	m, _ = Reduce(m, Event{Kind: LoginFailed, Token: m.Login.Request, Message: "登录已失效，请用短信重新登录"})
	if m.Login.Phase != LoginChoose || m.Login.Error == "" {
		t.Fatalf("a failed saved login must return to the picker with the reason: %+v", m.Login)
	}
	m, _ = Reduce(m, Event{Kind: LoginUsePhone})
	if m.Login.Phase != LoginPhone || m.Login.Error != "" {
		t.Fatalf("other account must open a clean phone form: %+v", m.Login)
	}
	m, _ = Reduce(m, Event{Kind: LoginBack})
	if m.Login.Phase != LoginChoose {
		t.Fatal("back must return to the saved accounts when there are any")
	}
	only := loginRequested(t)
	only, _ = Reduce(only, Event{Kind: LoginBack})
	if only.Login.Phase != LoginPhone {
		t.Fatal("back without saved accounts has nowhere to go")
	}
}

func TestLoginCodeCooldownIsLocalAndNeverRetriesItself(t *testing.T) {
	now := time.Date(2026, 11, 13, 16, 0, 0, 0, time.UTC)
	m := loginRequested(t)
	m, fx := Reduce(m, Event{Kind: LoginSendCode, Now: now})
	if m.Login.Phase != LoginSending || len(fx) != 1 || fx[0].Kind != SendLoginCode {
		t.Fatalf("send code must request exactly one SMS: %+v %+v", m.Login, fx)
	}
	m, fx = Reduce(m, Event{Kind: LoginSendCode, Now: now})
	if len(fx) != 0 {
		t.Fatal("double send must be ignored while sending")
	}
	m, _ = Reduce(m, Event{Kind: LoginCodeSent, Token: m.Login.Request, Now: now})
	if m.Login.Phase != LoginPhone || !m.Login.CodeSent || LoginCooldown(m.Login, now.Add(1*time.Second)) != 59 {
		t.Fatalf("sent code must start a 60 s cooldown: %+v", m.Login)
	}
	m, fx = Reduce(m, Event{Kind: LoginSendCode, Now: now.Add(30 * time.Second)})
	if len(fx) != 0 || m.Login.Phase != LoginPhone {
		t.Fatal("cooldown must block another SMS")
	}
	if LoginCooldown(m.Login, now.Add(61*time.Second)) != 0 {
		t.Fatal("cooldown must end after 60 s")
	}
	m, fx = Reduce(m, Event{Kind: LoginSendCode, Now: now.Add(61 * time.Second)})
	if len(fx) != 1 {
		t.Fatal("after the cooldown the user may ask again")
	}
	m, _ = Reduce(m, Event{Kind: LoginCodeFailed, Token: m.Login.Request, Message: "发送过于频繁"})
	if m.Login.Phase != LoginPhone || m.Login.Error != "发送过于频繁" || LoginCooldown(m.Login, now.Add(62*time.Second)) == 0 {
		t.Fatalf("a failed send must report and still hold a cooldown: %+v", m.Login)
	}
}

func TestLoginPhoneSubmitCarriesRememberAndIgnoresStaleResults(t *testing.T) {
	m := loginRequested(t)
	m, fx := Reduce(m, Event{Kind: LoginSubmit, Enabled: true})
	if m.Login.Phase != LoginVerifying || len(fx) != 1 || fx[0].Kind != SubmitPhoneLogin || fx[0].Path != "remember" {
		t.Fatalf("submit must start one login carrying the remember choice: %+v %+v", m.Login, fx)
	}
	stale := m.Login.Request - 1
	m, _ = Reduce(m, Event{Kind: LoginSucceeded, Token: stale})
	if m.Login.Phase != LoginVerifying {
		t.Fatal("stale results must be ignored")
	}
	m, _ = Reduce(m, Event{Kind: LoginSucceeded, Token: m.Login.Request})
	if m.Login.Phase != LoginDelivered || LoginOpen(m.Login.Phase) {
		t.Fatalf("success closes the window: %+v", m.Login)
	}
	m, _ = Reduce(m, Event{Kind: LoginRequested})
	if m.Login.Phase != LoginPhone {
		t.Fatal("the game may ask again after a logout")
	}
}

func TestLoginWindowCanBeDismissedReopenedAndEndsWithTheGame(t *testing.T) {
	m := loginRequested(t, SavedLogin{ID: "a", Label: "A"})
	m, _ = Reduce(m, Event{Kind: LoginDismiss})
	if m.Login.Phase != LoginWaiting || LoginOpen(m.Login.Phase) {
		t.Fatalf("dismiss hides the window but the game is still waiting: %+v", m.Login)
	}
	m, _ = Reduce(m, Event{Kind: LoginReopen})
	if m.Login.Phase != LoginChoose {
		t.Fatal("reopen must return to the picker")
	}
	m, fx := Reduce(m, Event{Kind: LoginSubmitSaved})
	m, _ = Reduce(m, Event{Kind: LoginEnded})
	if m.Login.Phase != LoginIdle || m.Login.Count != 0 || len(fx) != 1 {
		t.Fatalf("game exit must clear login state: %+v", m.Login)
	}
	m, _ = Reduce(m, Event{Kind: LoginSucceeded, Token: m.Login.Request})
	if m.Login.Phase != LoginIdle {
		t.Fatal("a result after the game ended must not resurrect the window")
	}
}

func TestLoginForgetAccountUpdatesThePicker(t *testing.T) {
	m := loginRequested(t, SavedLogin{ID: "a", Label: "A"}, SavedLogin{ID: "b", Label: "B"})
	m, _ = Reduce(m, Event{Kind: LoginSelect, Index: 1})
	m, fx := Reduce(m, Event{Kind: LoginForget, Index: 0})
	if len(fx) != 1 || fx[0].Kind != ForgetSavedLogin || fx[0].Path != "a" {
		t.Fatalf("forget must remove by id: %+v", fx)
	}
	if m.Login.Count != 1 || m.Login.Accounts[0].ID != "b" || m.Login.Selected != 0 {
		t.Fatalf("picker must drop the account and keep the selection valid: %+v", m.Login)
	}
	m, _ = Reduce(m, Event{Kind: LoginForget, Index: 0})
	if m.Login.Phase != LoginPhone || m.Login.Count != 0 {
		t.Fatalf("forgetting the last account opens the phone form: %+v", m.Login)
	}
	m, fx = Reduce(m, Event{Kind: LoginForget, Index: 3})
	if len(fx) != 0 {
		t.Fatal("out of range forget must be ignored")
	}
}

func TestLoginEventsAreIgnoredOutsideAWaitingGame(t *testing.T) {
	m := InitialNormal()
	for _, k := range []EventKind{LoginSendCode, LoginSubmit, LoginSubmitSaved, LoginReopen, LoginUsePhone} {
		next, fx := Reduce(m, Event{Kind: k})
		if len(fx) != 0 || next.Login.Phase != LoginIdle {
			t.Fatalf("event %d must be ignored while idle", k)
		}
	}
}
