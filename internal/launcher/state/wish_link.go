// SPDX-License-Identifier: GPL-3.0-or-later
package state

import "time"

type WishLinkPhase uint8

const (
	WishLinkNone WishLinkPhase = iota
	WishLinking
	WishLinked
	WishNeedsRelink
)

type WishLinkStep uint8

const (
	WishLinkNotice WishLinkStep = iota
	WishLinkPhone
	WishLinkRole
)

type WishLinkFailure uint8

const (
	WishLinkFailureNone WishLinkFailure = iota
	WishLinkNetwork
	WishLinkAuth
	WishLinkProtocol
	WishLinkChallenge
	WishLinkMismatch
	WishLinkStorage
	WishLinkNoRole
	WishLinkInvalid
	WishLinkRoleMismatch
	WishLinkGameCredential
)

type WishFetchPhase uint8

const (
	WishFetchIdle WishFetchPhase = iota
	WishFetching
	WishFetchFailed
)

// WishLinkState holds public identity and flow data only.
type WishLinkState struct {
	AccountID               string
	RoleID                  string
	Phase                   WishLinkPhase
	ReturnPhase             WishLinkPhase
	Step                    WishLinkStep
	Agreed, Busy, CodeSent  bool
	Token                   uint64
	Failure                 WishLinkFailure
	Fetch                   WishFetchPhase
	CooldownUntil, SMSUntil time.Time
}

const (
	WishLinkStart EventKind = 70 + iota
	WishLinkAgree
	WishLinkContinue
	WishLinkUseSaved
	WishLinkCancel
	WishLinkSendSMS
	WishLinkSMSDone
	WishLinkSubmit
	WishLinkLoginDone
	WishLinkRolesDone
	WishLinkBind
	WishLinkBound
	WishLinkSelectBound
	WishLinkCompleted
	WishLinkFailed
	WishLinkUnlink
	WishLinkUnlinked
	WishFetchRequest
	WishFetchDone
	WishLinkStatus
)
const (
	TajiduoSendSMS EffectKind = 70 + iota
	TajiduoLogin
	TajiduoLoginSaved
	TajiduoRoles
	TajiduoBindRole
	TajiduoComplete
	TajiduoCancel
	TajiduoUnlink
	WishFetch
)

func WishLinkCooldown(until, now time.Time) int {
	left := until.Sub(now)
	if left <= 0 {
		return 0
	}
	return int((left + time.Second - 1) / time.Second)
}
func reduceWishLink(m Model, e Event) (Model, []Effect, bool) {
	if e.Kind < WishLinkStart || e.Kind > WishLinkStatus {
		return m, nil, false
	}
	if m.Closed {
		return m, nil, true
	}
	l := &m.Wish.Link
	if e.Kind == WishLinkStatus {
		if l.Busy || l.Phase == WishLinking || l.Fetch == WishFetching {
			return m, nil, true
		}
		if l.AccountID != e.WishAccountID {
			*l = WishLinkState{Token: l.Token + 1}
		}
		l.AccountID, l.RoleID = e.WishAccountID, e.WishRoleID
		l.Phase = WishLinkNone
		if e.WishLinked {
			l.Phase = WishLinked
		}
		if e.WishRelink {
			l.Phase = WishNeedsRelink
		}
		return m, nil, true
	}
	if l.AccountID == "" {
		return m, nil, true
	}
	start := func(kind EffectKind, role string) (Model, []Effect, bool) {
		l.Token++
		l.Busy = true
		l.Failure = WishLinkFailureNone
		return m, []Effect{{Kind: kind, Token: l.Token, Path: l.AccountID, RoleID: role}}, true
	}
	switch e.Kind {
	case WishLinkStart:
		if m.Page == WishAnalysis && m.Overlay == NoOverlay && !l.Busy && !m.Wish.Busy && l.Fetch != WishFetching && m.Wish.Confirm.Kind == WishConfirmNone {
			l.Token++
			l.ReturnPhase = l.Phase
			l.Phase, l.Step, l.Agreed, l.CodeSent, l.Failure = WishLinking, WishLinkNotice, false, false, WishLinkFailureNone
		}
	case WishLinkAgree:
		if l.Phase == WishLinking && l.Step == WishLinkNotice {
			l.Agreed = e.Enabled
		}
	case WishLinkContinue:
		if l.Phase == WishLinking && l.Step == WishLinkNotice && l.Agreed {
			l.Step, l.Failure = WishLinkPhone, WishLinkFailureNone
		}
	case WishLinkUseSaved:
		if l.Phase == WishLinking && l.Step == WishLinkNotice && !l.Busy && l.Agreed {
			return start(TajiduoLoginSaved, "")
		}
	case WishLinkCancel:
		if l.Phase == WishLinking {
			l.Token++
			l.Phase, l.Busy, l.Agreed = l.ReturnPhase, false, false
			return m, []Effect{{Kind: TajiduoCancel, Token: l.Token, Path: l.AccountID}}, true
		}
	case WishLinkSendSMS:
		if l.Phase == WishLinking && l.Step == WishLinkPhone && !l.Busy && WishLinkCooldown(l.SMSUntil, e.Now) == 0 {
			l.SMSUntil = e.Now.Add(time.Minute)
			return start(TajiduoSendSMS, "")
		}
	case WishLinkSubmit:
		if l.Phase == WishLinking && l.Step == WishLinkPhone && !l.Busy {
			return start(TajiduoLogin, "")
		}
	case WishLinkSMSDone, WishLinkLoginDone, WishLinkRolesDone, WishLinkBound, WishLinkCompleted, WishLinkFailed, WishLinkUnlinked, WishFetchDone:
		if e.Token != l.Token || !l.Busy {
			return m, nil, true
		}
		l.Busy = false
		switch e.Kind {
		case WishLinkSMSDone:
			l.CodeSent = true
		case WishLinkLoginDone:
			l.Step = WishLinkRole
			return start(TajiduoRoles, "")
		case WishLinkRolesDone:
			l.Step = WishLinkRole
		case WishLinkBound:
			return start(TajiduoComplete, e.WishRoleID)
		case WishLinkCompleted:
			l.Phase, l.Failure, l.RoleID = WishLinked, WishLinkFailureNone, e.WishRoleID
		case WishLinkUnlinked:
			l.Phase, l.Failure, l.RoleID = WishLinkNone, WishLinkFailureNone, ""
		case WishFetchDone:
			l.Fetch, l.Failure, l.RoleID = WishFetchIdle, WishLinkFailureNone, e.WishRoleID
			m.Wish.RoleID, m.Wish.Phase = e.WishRoleID, WishReady
			m.Wish.ImportReported = true
			m.Wish.NewCount, m.Wish.Outside, m.Wish.Conflicts = e.WishNewCount, e.WishOutside, e.WishConflicts
		case WishLinkFailed:
			l.Failure = e.WishLinkFailure
			if l.Fetch == WishFetching {
				l.Fetch = WishFetchFailed
			}
			if e.WishLinkFailure == WishLinkAuth && l.Phase != WishLinking {
				l.Phase = WishNeedsRelink
			}
		}
	case WishLinkBind, WishLinkSelectBound:
		if l.Phase == WishLinking && l.Step == WishLinkRole && !l.Busy && e.WishRoleID != "" {
			if e.Kind == WishLinkBind {
				return start(TajiduoBindRole, e.WishRoleID)
			}
			return start(TajiduoComplete, e.WishRoleID)
		}
	case WishFetchRequest:
		if m.Page == WishAnalysis && m.Overlay == NoOverlay && l.Phase == WishLinked && !l.Busy && l.Fetch != WishFetching && !m.Wish.Busy && m.Wish.Confirm.Kind == WishConfirmNone && WishLinkCooldown(l.CooldownUntil, e.Now) == 0 {
			role := m.Wish.RoleID
			if role == "" {
				role = l.RoleID
			}
			l.Fetch = WishFetching
			l.CooldownUntil = e.Now.Add(time.Minute)
			return start(WishFetch, role)
		}
	case WishLinkUnlink:
		if l.Phase != WishLinkNone && !l.Busy && m.Wish.Confirm.Kind == WishConfirmNone {
			m.Wish.Confirm = WishConfirmation{Kind: WishConfirmUnlink}
		}
	}
	return m, nil, true
}
