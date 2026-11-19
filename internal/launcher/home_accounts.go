// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"context"
	"log"
	"time"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// beginHomeAccounts reads the remembered accounts off the UI thread. The list
// is only a convenience for the home row; the login window remains the only
// place that can add, rename or remove an account.
func (s *Shell) beginHomeAccounts(f state.Effect) {
	if s.HomeAccounts == nil {
		return
	}
	token, out, wake, parent := f.Token, s.accountOut, s.RequestFrame, s.ctx
	if out == nil {
		return
	}
	s.accountsPending = true
	go func() {
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		defer cancel()
		list, current, err := s.HomeAccounts(ctx, token)
		if ctx.Err() != nil {
			return
		}
		e := state.Event{Kind: state.HomeAccountsLoaded, Token: token, Path: current, Accounts: list}
		if err != nil {
			e.Message = state.BriefError(err)
		}
		select {
		case out <- e:
			if wake != nil {
				wake()
			}
		case <-parent.Done():
		}
	}()
}

// rememberHomeAccount makes one saved account the next account the game login
// window proposes. It does not touch the running session.
func (s *Shell) rememberHomeAccount(f state.Effect) {
	if s.HomeAccountUse == nil || f.Path == "" {
		return
	}
	parent, out, wake := s.ctx, s.accountOut, s.RequestFrame
	go func() {
		ctx, cancel := context.WithTimeout(parent, 5*time.Second)
		defer cancel()
		if err := s.HomeAccountUse(ctx, f.Path); err != nil {
			log.Printf("remember account: %v", err)
			return
		}
		if wake != nil {
			wake()
		}
		_ = out
	}()
}
