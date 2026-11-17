// SPDX-License-Identifier: GPL-3.0-or-later

package state

import "time"

// MaxSavedLogins bounds the saved accounts the picker shows.
const MaxSavedLogins = 16

// loginCooldown is how long the form waits before another SMS may be asked.
const loginCooldown = 60 * time.Second

// SavedLogin is the public face of one account the user chose to remember.
// The label is already masked; secrets never enter the model.
type SavedLogin struct {
	ID, Label string
}

// LoginPhase is where the login window stands. The window only exists while
// a started game is waiting for an identity.
type LoginPhase uint8

const (
	// LoginIdle: no game is waiting for a login.
	LoginIdle LoginPhase = iota
	// LoginWaiting: the game is waiting, but the user hid the window.
	LoginWaiting
	LoginChoose
	LoginPhone
	LoginSending
	LoginVerifying
	// LoginDelivered: the game received an identity.
	LoginDelivered
)

// LoginState is the login window. Phone number and code stay in the shell's
// form fields; they never enter the model or an effect.
type LoginState struct {
	Phase         LoginPhase
	Accounts      [MaxSavedLogins]SavedLogin
	Count         uint8
	Selected      uint8
	CodeSent      bool
	CooldownUntil time.Time
	Error         string
	Request       uint64
	// from is the phase a running verification returns to on failure.
	from LoginPhase
}

// LoginOpen reports whether the login window is on screen.
func LoginOpen(p LoginPhase) bool {
	return p == LoginChoose || p == LoginPhone || p == LoginSending || p == LoginVerifying
}

// LoginCooldown returns the whole seconds left before another SMS.
func LoginCooldown(l LoginState, now time.Time) int {
	left := l.CooldownUntil.Sub(now)
	if left <= 0 {
		return 0
	}
	return int((left + time.Second - 1) / time.Second)
}

const (
	// LoginRequested: the game asked for an identity. Logins lists the saved
	// accounts.
	LoginRequested EventKind = 120 + iota
	LoginSelect
	LoginSubmitSaved
	// LoginFailed carries the reason a submitted login was refused.
	LoginFailed
	LoginUsePhone
	LoginBack
	LoginSendCode
	LoginCodeSent
	LoginCodeFailed
	// LoginSubmit submits the phone form; Enabled asks to remember the account.
	LoginSubmit
	LoginSucceeded
	LoginDismiss
	LoginReopen
	// LoginEnded: the game that asked is gone.
	LoginEnded
	LoginForget
)

const (
	// SendLoginCode asks the shell to request an SMS for the form's phone.
	SendLoginCode EffectKind = 120 + iota
	// SubmitPhoneLogin logs in with the form; Path "remember" saves the account.
	SubmitPhoneLogin
	// SubmitSavedLogin logs in with the saved account Path.
	SubmitSavedLogin
	// ForgetSavedLogin deletes the saved account Path.
	ForgetSavedLogin
)

func (l *LoginState) entry() LoginPhase {
	if l.Count > 0 {
		return LoginChoose
	}
	return LoginPhone
}

func reduceLogin(m Model, e Event) (Model, []Effect, bool) {
	if e.Kind < LoginRequested || e.Kind > LoginForget {
		return m, nil, false
	}
	l := &m.Login
	switch e.Kind {
	case LoginRequested:
		request := l.Request + 1
		*l = LoginState{Request: request}
		for _, a := range e.Logins {
			if int(l.Count) == MaxSavedLogins {
				break
			}
			l.Accounts[l.Count] = a
			l.Count++
		}
		l.Phase = l.entry()
		return m, nil, true
	case LoginEnded:
		*l = LoginState{Request: l.Request + 1}
		return m, nil, true
	}
	if l.Phase == LoginIdle || l.Phase == LoginDelivered {
		return m, nil, true
	}
	switch e.Kind {
	case LoginSelect:
		if l.Phase == LoginChoose && e.Index < l.Count {
			l.Selected, l.Error = e.Index, ""
		}
	case LoginSubmitSaved:
		if l.Phase != LoginChoose || l.Count == 0 {
			break
		}
		l.Request++
		l.Phase, l.from, l.Error = LoginVerifying, LoginChoose, ""
		return m, []Effect{{Kind: SubmitSavedLogin, Path: l.Accounts[l.Selected].ID, Token: l.Request}}, true
	case LoginFailed:
		if l.Phase != LoginVerifying || e.Token != l.Request {
			break
		}
		l.Phase, l.Error = l.from, e.Message
		if l.Error == "" {
			l.Error = "登录失败，请稍后再试"
		}
	case LoginUsePhone:
		if l.Phase == LoginChoose {
			l.Phase, l.Error = LoginPhone, ""
		}
	case LoginBack:
		if l.Phase == LoginPhone && l.Count > 0 {
			l.Phase, l.Error = LoginChoose, ""
		}
	case LoginSendCode:
		if l.Phase != LoginPhone || LoginCooldown(*l, e.Now) > 0 {
			break
		}
		l.Request++
		l.Phase, l.Error = LoginSending, ""
		l.CooldownUntil = e.Now.Add(loginCooldown)
		return m, []Effect{{Kind: SendLoginCode, Token: l.Request}}, true
	case LoginCodeSent, LoginCodeFailed:
		if l.Phase != LoginSending || e.Token != l.Request {
			break
		}
		l.Phase = LoginPhone
		if e.Kind == LoginCodeSent {
			l.CodeSent = true
			if !e.Now.IsZero() {
				l.CooldownUntil = e.Now.Add(loginCooldown)
			}
			break
		}
		l.Error = e.Message
		if l.Error == "" {
			l.Error = "验证码发送失败"
		}
	case LoginSubmit:
		if l.Phase != LoginPhone {
			break
		}
		l.Request++
		l.Phase, l.from, l.Error = LoginVerifying, LoginPhone, ""
		f := Effect{Kind: SubmitPhoneLogin, Token: l.Request}
		if e.Enabled {
			f.Path = "remember"
		}
		return m, []Effect{f}, true
	case LoginSucceeded:
		if l.Phase == LoginVerifying && e.Token == l.Request {
			l.Phase, l.Error = LoginDelivered, ""
		}
	case LoginDismiss:
		if LoginOpen(l.Phase) {
			// Hiding abandons whatever request was in flight.
			l.Request++
			l.Phase = LoginWaiting
		}
	case LoginReopen:
		if l.Phase == LoginWaiting {
			l.Phase, l.Error = l.entry(), ""
		}
	case LoginForget:
		if l.Phase != LoginChoose || e.Index >= l.Count {
			break
		}
		id := l.Accounts[e.Index].ID
		copy(l.Accounts[e.Index:], l.Accounts[e.Index+1:l.Count])
		l.Count--
		l.Accounts[l.Count] = SavedLogin{}
		if l.Selected > e.Index {
			l.Selected--
		}
		if l.Count == 0 {
			l.Selected, l.Phase = 0, LoginPhone
		} else if l.Selected >= l.Count {
			l.Selected = l.Count - 1
		}
		return m, []Effect{{Kind: ForgetSavedLogin, Path: id}}, true
	}
	return m, nil, true
}
