// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// GamePlan is a checked game installation that has not been started yet.
type GamePlan interface {
	// Summary is the human-readable, already masked description of the checked
	// installation shown before start.
	Summary() string
	Start(context.Context) (GameSession, error)
}

// GameSession hosts one started game. It answers the game's login requests
// (LoginBackend) and reports them on Notices until the game exits.
type GameSession interface {
	LoginBackend
	Notices() <-chan GameNotice
	// Wait returns nil after a confirmed clean exit, *GameExitError after a
	// confirmed failing exit, and any other error when the exit is unknown.
	Wait(context.Context) error
	Close() error // stops hosting and observing; never terminates the game
}

// GameNotice is a change in the game's login request. LoginRequested false
// means the game no longer waits for a login.
type GameNotice struct {
	LoginRequested bool
	Logins         []state.SavedLogin
}

// ErrGameUnobserved marks a start whose outcome cannot be confirmed. The
// launcher keeps its start lock rather than risk a second game.
var ErrGameUnobserved = errors.New("无法确认游戏是否已启动")

// GameExitError is a confirmed non-zero game exit.
type GameExitError struct{ Code uint32 }

func (e *GameExitError) Error() string {
	return fmt.Sprintf("游戏异常退出（代码 %d）", e.Code)
}

func (s *Shell) beginManagedStart(token uint64) {
	plan, root := s.managedPlan, s.managedPlanRoot
	s.managedPlan, s.managedPlanRoot = nil, ""
	s.cancelManagedWork()
	ctx, cancel := context.WithCancel(s.ctx)
	s.managedCancel = cancel
	out, wake := s.managedOut, s.RequestFrame
	if plan == nil || root != s.Model.Managed.Root {
		closeGamePlan(plan)
		postManaged(ctx, out, wake, managedResult{event: state.Event{Kind: state.ManagedHostFailed, Token: token, Message: "游戏目录已改变，请重新启动。"}})
		return
	}
	go func() {
		defer cancel()
		startCtx, stop := context.WithTimeout(ctx, 30*time.Second)
		session, err := plan.Start(startCtx)
		stop()
		if err != nil || session == nil {
			if session == nil {
				closeGamePlan(plan)
			}
			kind, message := state.ManagedHostFailed, "游戏未启动"
			if err != nil {
				message = state.BriefError(err)
			}
			if session == nil && err == nil || errors.Is(err, ErrGameUnobserved) {
				kind = state.ManagedHostUnobserved
			}
			postManaged(ctx, out, wake, managedResult{event: state.Event{Kind: kind, Token: token, Message: message}})
			return
		}
		defer session.Close()
		postManaged(ctx, out, wake, managedResult{event: state.Event{Kind: state.ManagedHostStarted, Token: token}, session: session})
		exited := make(chan error, 1)
		go func() { exited <- session.Wait(ctx) }()
		notices := session.Notices()
		for {
			select {
			case n, ok := <-notices:
				if !ok {
					notices = nil
					continue
				}
				e := state.Event{Kind: state.LoginEnded}
				if n.LoginRequested {
					e = state.Event{Kind: state.LoginRequested, Logins: n.Logins}
				}
				postManaged(ctx, out, wake, managedResult{event: e, session: session})
			case err := <-exited:
				if ctx.Err() != nil {
					return
				}
				kind, message := state.ManagedHostExited, ""
				if err != nil {
					var exit *GameExitError
					kind = state.ManagedHostUnobserved
					if errors.As(err, &exit) {
						kind = state.ManagedHostFailed
					}
					message = state.BriefError(err)
				}
				postManaged(ctx, out, wake, managedResult{event: state.Event{Kind: kind, Token: token, Message: message}, session: session})
				return
			}
		}
	}()
}

// adoptGameResult binds or releases the login backend of the hosted game as
// its results reach the UI thread.
func (s *Shell) adoptGameResult(r managedResult) {
	if r.session == nil {
		return
	}
	switch r.event.Kind {
	case state.ManagedHostStarted:
		if r.event.Token == s.Model.Managed.Request && s.Model.Managed.Phase == state.ManagedStarting {
			s.gameSession = r.session
		}
	case state.ManagedHostExited, state.ManagedHostFailed, state.ManagedHostUnobserved:
		if s.gameSession == r.session {
			s.releaseGameSession()
		}
	case state.LoginRequested:
		if s.gameSession == r.session {
			s.LoginBackend = r.session
		}
	}
}

func (s *Shell) releaseGameSession() {
	s.gameSession = nil
	s.LoginBackend = nil
	s.Dispatch(state.Event{Kind: state.LoginEnded})
}
