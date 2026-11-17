// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"context"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// LoginBackend logs in on behalf of the running game. Each call delivers the
// identity to the game itself; the launcher only reports the outcome.
type LoginBackend interface {
	SendCode(ctx context.Context, phone string) error
	LoginPhone(ctx context.Context, phone, code string, remember bool) error
	LoginSaved(ctx context.Context, id string) error
	Forget(ctx context.Context, id string) error
}

// LoginForm holds what the user typed. It stays on the UI thread and is
// cleared once the game has an identity; it never enters the model.
type LoginForm struct {
	Phone, Code string
	Remember    bool
}

// syncLoginWindow shows or hides the login window when the model says so.
func (s *Shell) syncLoginWindow() {
	l := s.Model.Login
	if l.Phase == state.LoginIdle || l.Phase == state.LoginDelivered {
		s.LoginForm = LoginForm{}
	}
	open := state.LoginOpen(l.Phase)
	if open == s.loginShown {
		return
	}
	s.loginShown = open
	if s.ShowLogin != nil {
		s.ShowLogin(open)
	}
}

func (s *Shell) applyLoginEffect(f state.Effect) bool {
	b := s.LoginBackend
	fail := func(kind state.EventKind, err error) state.Event {
		return state.Event{Kind: kind, Token: f.Token, Message: state.BriefError(err)}
	}
	switch f.Kind {
	case state.SendLoginCode:
		phone := s.LoginForm.Phone
		s.startWork(s.workOut, func(ctx context.Context) state.Event {
			if b == nil {
				return state.Event{Kind: state.LoginCodeFailed, Token: f.Token, Message: "登录服务不可用"}
			}
			if err := b.SendCode(ctx, phone); err != nil {
				return fail(state.LoginCodeFailed, err)
			}
			return state.Event{Kind: state.LoginCodeSent, Token: f.Token}
		})
	case state.SubmitPhoneLogin, state.SubmitSavedLogin:
		form, id := s.LoginForm, f.Path
		phone := f.Kind == state.SubmitPhoneLogin
		s.startWork(s.workOut, func(ctx context.Context) state.Event {
			if b == nil {
				return state.Event{Kind: state.LoginFailed, Token: f.Token, Message: "登录服务不可用"}
			}
			var err error
			if phone {
				err = b.LoginPhone(ctx, form.Phone, form.Code, id == "remember")
			} else {
				err = b.LoginSaved(ctx, id)
			}
			if err != nil {
				return fail(state.LoginFailed, err)
			}
			return state.Event{Kind: state.LoginSucceeded, Token: f.Token}
		})
	case state.ForgetSavedLogin:
		if b != nil {
			ctx, id := s.ctx, f.Path
			go b.Forget(ctx, id) // the picker already dropped it; a stale file only reappears next time
		}
	default:
		return false
	}
	return true
}
