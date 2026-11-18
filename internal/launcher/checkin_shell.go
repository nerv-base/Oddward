// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"context"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// applyCheckInEffect starts the background work for one check-in effect. Every
// result comes back through checkinOut, which only the UI thread drains.
func (s *Shell) applyCheckInEffect(f state.Effect) bool {
	switch f.Kind {
	case state.TajiduoCheckInState:
		s.beginCheckInState(f)
	case state.TajiduoCheckInSign:
		s.beginCheckInSign(f)
	case state.TajiduoCheckInStatus:
		s.beginCheckInStatus(f)
	case state.TajiduoCheckInAuto:
		// The switch is written by one serialized worker in syncCheckIn, not
		// by a goroutine per click: a late "on" must never overwrite a newer
		// "off" on disk.
		s.RequestFrame()
	case state.TajiduoCheckInLaunch:
		// The user asked to start the game: this is the only trigger of an
		// automatic attempt. The snapshot is refreshed first, so the attempt
		// sees the day the server is actually on.
		s.checkinLaunch = true
		s.Dispatch(state.Event{Kind: state.CheckInStateRequested, Check: state.CheckInPayload{Enabled: true}})
	default:
		return false
	}
	return true
}

func (s *Shell) beginCheckInState(f state.Effect) {
	if s.CheckInBackend == nil {
		s.Dispatch(state.Event{Kind: state.CheckInStateFailed, Token: f.Token, Check: state.CheckInPayload{Fail: state.CheckInFailureStorage}})
		return
	}
	backend, ctx, wake := s.CheckInBackend, s.ctx, s.RequestFrame
	go func() {
		snapshot, err := backend.CheckInState(ctx, f.Enabled)
		event := state.Event{Kind: state.CheckInStateLoaded, Token: f.Token, Check: checkInPayloadOfSnapshot(snapshot)}
		if err != nil {
			event = state.Event{Kind: state.CheckInStateFailed, Token: f.Token, Check: state.CheckInPayload{Fail: checkInErrorKind(err)}}
		}
		s.postCheckIn(ctx, wake, event)
	}()
}

func (s *Shell) beginCheckInSign(f state.Effect) {
	kind := f.CheckInKind
	if s.CheckInBackend == nil {
		s.Dispatch(state.Event{Kind: state.CheckInSigned, Token: f.Token, Check: checkInPayloadOfOutcome(CheckInOutcome{Kind: kind, Result: state.CheckResultFailed, Failure: state.CheckInFailureStorage})})
		return
	}
	backend, ctx, wake := s.CheckInBackend, s.ctx, s.RequestFrame
	accountID := s.Model.CheckIn.AccountID
	confirmed := f.Confirmed
	go func() {
		outcome, err := backend.Sign(ctx, accountID, kind, confirmed)
		if err != nil {
			outcome = CheckInOutcome{Kind: kind, Result: state.CheckResultFailed, Failure: checkInErrorKind(err)}
		}
		s.postCheckIn(ctx, wake, state.Event{Kind: state.CheckInSigned, Token: f.Token, Check: checkInPayloadOfOutcome(outcome)})
	}()
}

func (s *Shell) beginCheckInStatus(f state.Effect) {
	if s.CheckInBackend == nil {
		s.Dispatch(state.Event{Kind: state.CheckInStatusFailed, Token: f.Token, Check: state.CheckInPayload{Fail: state.CheckInFailureStorage}})
		return
	}
	backend, ctx, wake := s.CheckInBackend, s.ctx, s.RequestFrame
	go func() {
		role, err := backend.RoleStatus(ctx)
		event := state.Event{Kind: state.CheckInStatusLoaded, Token: f.Token, Check: state.CheckInPayload{Role: role}}
		if err != nil {
			event = state.Event{Kind: state.CheckInStatusFailed, Token: f.Token, Check: state.CheckInPayload{Fail: checkInErrorKind(err)}}
		}
		s.postCheckIn(ctx, wake, event)
	}()
}

// pumpCheckInAuto writes at most one automatic switch at a time. The worker
// compares the requested value with the persisted one, so two fast clicks
// become two ordered writes instead of two racing ones; the last intent is what
// a restart reads.
func (s *Shell) pumpCheckInAuto() {
	if s.CheckInBackend == nil || s.checkinAutoBusy {
		return
	}
	c := s.Model.CheckIn
	if !c.Loaded || !c.Linked {
		return
	}
	for k := 0; k < state.CheckInKinds; k++ {
		if c.Auto[k] == c.AutoSaved[k] {
			continue
		}
		kind, want, token := state.CheckInKind(k), c.Auto[k], c.AutoToken[k]
		saved := c.AutoSaved[k]
		accountID := c.AccountID
		backend, ctx, wake := s.CheckInBackend, s.ctx, s.RequestFrame
		s.checkinAutoBusy = true
		go func() {
			payload := state.CheckInPayload{Kind: kind, Enabled: want}
			if err := backend.SetAuto(ctx, accountID, kind, want); err != nil {
				// Nothing was written: report the value still on disk so the
				// reducer can put the switch back.
				payload.Enabled = saved
				payload.Fail = checkInErrorKind(err)
			}
			s.postCheckIn(ctx, wake, state.Event{Kind: state.CheckInAutoSaved, Token: token, Check: payload})
		}()
		return
	}
}

// postCheckIn hands one finished event to the UI thread. It never blocks: the
// channel is bounded and a cancelled context drops the answer.
func (s *Shell) postCheckIn(ctx context.Context, wake func(), event state.Event) {
	if ctx.Err() != nil {
		return
	}
	select {
	case s.checkinOut <- event:
		if wake != nil {
			wake()
		}
	case <-ctx.Done():
	}
}

func checkInPayloadOfSnapshot(s CheckInSnapshot) state.CheckInPayload {
	return state.CheckInPayload{
		AccountID: s.AccountID, RoleID: s.RoleID,
		Linked: s.Linked, NeedsRelink: s.NeedsRelink,
		Auto: s.Auto, Known: s.Known, Signed: s.Signed,
		Result: s.Result, Detail: s.Detail, Failure: s.Failure, At: s.At,
		Day:  s.Day,
		Fail: s.Fail,
	}
}

// checkInPayloadOfOutcome places one result at its own task index: the reducer
// reads the arrays per kind, so a game answer can never land on the community
// card.
func checkInPayloadOfOutcome(o CheckInOutcome) state.CheckInPayload {
	var p state.CheckInPayload
	if !o.Kind.Valid() {
		return p
	}
	p.Kind = o.Kind
	p.Result[o.Kind] = o.Result
	p.Detail[o.Kind] = o.Detail
	p.Failure[o.Kind] = o.Failure
	p.At[o.Kind] = o.At
	p.Known[o.Kind] = o.Known
	p.Signed[o.Kind] = o.Signed
	return p
}

// syncCheckIn runs on the UI thread, from tick. It delivers finished work, then
// asks for the state once, then reacts to the two triggers the product has: the
// user opening the page, and the user starting the game.
func (s *Shell) syncCheckIn(c *ui.Context) {
	for {
		select {
		case event := <-s.checkinOut:
			if event.Kind == state.CheckInAutoSaved {
				s.checkinAutoBusy = false
			}
			s.Dispatch(event)
			continue
		default:
		}
		break
	}
	if s.CheckInBackend == nil {
		return
	}
	if !s.checkinStarted {
		// The ledger is local: this load reads no network. A refresh only
		// happens when the page is opened or a write is about to go out.
		s.checkinStarted = true
		s.Dispatch(state.Event{Kind: state.CheckInStateRequested, Check: state.CheckInPayload{}})
	}
	if s.Model.Page == state.CheckIn && !s.checkinVisit {
		s.checkinVisit = true
		s.Dispatch(state.Event{Kind: state.CheckInStateRequested, Check: state.CheckInPayload{Enabled: true}})
	} else if s.Model.Page != state.CheckIn {
		s.checkinVisit = false
	}
	checkIn := s.Model.CheckIn
	if checkIn.Loaded && checkIn.Linked && checkIn.StatusPhase == state.CheckStatusIdle && s.Model.Page == state.CheckIn {
		// Opening the page reads the game summary once; the panel's own 刷新
		// button asks again later.
		s.Dispatch(state.Event{Kind: state.CheckInStatusRequested})
	}
	if s.checkinLaunch && !checkIn.Loading {
		// A previous snapshot can stay visible while the launch refresh runs.
		// Use only a successful refresh for this launch's automatic attempt.
		s.checkinLaunch = false
		if checkIn.Loaded && checkIn.LoadFailure == state.CheckInFailureNone {
			s.Dispatch(state.Event{Kind: state.CheckInAutoTick, Now: s.now})
		}
	}
	s.pumpCheckInAuto()
}

// checkInProjection is the pure view data the page draws. Callbacks dispatch
// events only: no network and no model write happens in the page.
func (s *Shell) checkInProjection() checkInPageData {
	m := s.Model.CheckIn
	return checkInPageData{
		AccountID:     m.AccountID,
		RoleID:        m.RoleID,
		Linked:        m.Linked,
		NeedsRelink:   m.NeedsRelink,
		Loading:       m.Loading,
		Loaded:        m.Loaded,
		LoadFailure:   m.LoadFailure,
		Auto:          m.Auto,
		Busy:          m.Busy,
		Phase:         m.Phase,
		Result:        m.Result,
		Detail:        m.Detail,
		Failure:       m.Failure,
		At:            m.At,
		Confirm:       m.Confirm,
		StatusPhase:   m.StatusPhase,
		StatusFailure: m.StatusFailure,
		Role:          m.Role,
		Now:           s.now,
		OnSign: func(kind state.CheckInKind) {
			s.Dispatch(state.Event{Kind: state.CheckInBegin, Check: state.CheckInPayload{Kind: kind}})
		},
		OnConfirm: func(kind state.CheckInKind, armed bool) {
			s.Dispatch(state.Event{Kind: state.CheckInConfirm, Check: state.CheckInPayload{Kind: kind, Enabled: armed}})
		},
		OnRefresh: func() {
			s.Dispatch(state.Event{Kind: state.CheckInStateRequested, Check: state.CheckInPayload{Enabled: true}})
		},
		OnStatus: func() {
			s.Dispatch(state.Event{Kind: state.CheckInStatusRequested})
		},
		OnRelink: func() {
			s.Dispatch(state.Event{Kind: state.Navigate, Page: state.WishAnalysis})
		},
	}
}
