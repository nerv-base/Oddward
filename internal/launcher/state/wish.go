// SPDX-License-Identifier: GPL-3.0-or-later

package state

// WishFormat identifies the archive interchange format.
type WishFormat uint8

const (
	WishOddward WishFormat = iota
	WishNTEUID
)

// WishPhase describes the local archive operation.
type WishPhase uint8

const (
	WishIdle WishPhase = iota
	WishLoading
	WishReady
	WishFailed
)

// WishFailureKind is a backend failure category, without backend dependencies.
type WishFailureKind uint8

const (
	WishFailureNone WishFailureKind = iota
	WishFailureIO
	WishFailureCorrupt
	WishFailureRoleMismatch
	WishFailureFormat
)

// WishConfirmationKind identifies an operation awaiting explicit consent.
type WishConfirmationKind uint8

const (
	WishConfirmNone WishConfirmationKind = iota
	WishConfirmDelete
	WishConfirmImport
	WishConfirmLossy
	WishConfirmUnlink
)

// WishConfirmation captures the exact archive operation to confirm.
type WishConfirmation struct {
	Kind   WishConfirmationKind
	RoleID string
	Count  int
	Path   string
}

// WishState contains only comparable data; archives stay outside the model.
type WishState struct {
	Link                         WishLinkState
	RoleID                       string
	Phase                        WishPhase
	Busy                         bool
	ImportReported               bool
	Token                        uint64
	Failure                      WishFailureKind
	Pool                         string
	Confirm                      WishConfirmation
	NewCount, Outside, Conflicts int
}

// ReduceWish handles local wish archive intents and token-fenced results.
// Dialogs preserve phase and failure; Busy prevents concurrent user operations.
func ReduceWish(m Model, e Event) (Model, []Effect, bool) {
	if next, effects, handled := reduceWishLink(m, e); handled {
		return next, effects, true
	}
	if e.Kind < WishRequestList || e.Kind > WishImportNeedsConfirmation {
		return m, nil, false
	}
	if m.Closed {
		return m, nil, true
	}
	w := &m.Wish
	if e.Kind >= WishListed {
		// Results can finish behind an overlay or after navigating away.
		if e.Token != w.Token {
			return m, nil, true
		}
		// Chained results immediately start the next operation below.
		w.Busy = false
	} else {
		if m.Page != WishAnalysis || m.Overlay != NoOverlay {
			return m, nil, true
		}
		if e.Kind == WishAcceptConfirm || e.Kind == WishCancelConfirm {
			if w.Confirm.Kind == WishConfirmNone || (e.Kind == WishAcceptConfirm && w.Busy) {
				return m, nil, true
			}
		} else if w.Busy || w.Confirm.Kind != WishConfirmNone {
			return m, nil, true
		}
	}
	start := func(effect Effect) (Model, []Effect, bool) {
		w.Token++
		w.Busy = true
		switch effect.Kind {
		case WishList, WishLoad, WishDelete:
			w.Failure = WishFailureNone
		}
		effect.Token = w.Token
		return m, []Effect{effect}, true
	}
	clearImportReport := func() {
		w.ImportReported = false
		w.NewCount, w.Outside, w.Conflicts = 0, 0, 0
	}
	switch e.Kind {
	case WishRequestList:
		clearImportReport()
		w.Phase = WishLoading
		return start(Effect{Kind: WishList})
	case WishListed:
		clearImportReport()
		if e.WishCount > 0 {
			w.RoleID, w.Phase = e.WishRoleID, WishLoading
			return start(Effect{Kind: WishLoad, RoleID: w.RoleID})
		}
		w.RoleID, w.Phase = "", WishReady
	case WishSelectRole:
		clearImportReport()
		w.RoleID, w.Phase = e.WishRoleID, WishLoading
		return start(Effect{Kind: WishLoad, RoleID: w.RoleID})
	case WishLoaded:
		w.Phase, w.Failure = WishReady, WishFailureNone
	case WishExported:
		w.Phase = WishReady
	case WishRequestDelete:
		deletable := w.Phase == WishReady || (w.Phase == WishFailed && w.Failure == WishFailureCorrupt)
		if w.RoleID != "" && deletable {
			w.Confirm = WishConfirmation{Kind: WishConfirmDelete, RoleID: w.RoleID, Count: e.WishCount}
		}
	case WishAcceptConfirm:
		confirm := w.Confirm
		w.Confirm = WishConfirmation{}
		switch confirm.Kind {
		case WishConfirmUnlink:
			if w.Link.AccountID != "" && !w.Link.Busy {
				w.Link.Token++
				w.Link.Busy = true
				return m, []Effect{{Kind: TajiduoUnlink, Token: w.Link.Token, Path: w.Link.AccountID}}, true
			}
		case WishConfirmDelete:
			clearImportReport()
			w.Phase = WishLoading
			return start(Effect{Kind: WishDelete, RoleID: confirm.RoleID})
		case WishConfirmImport:
			return start(Effect{Kind: WishImport, RoleID: confirm.RoleID, Path: confirm.Path, Confirmed: true})
		case WishConfirmLossy:
			return start(Effect{Kind: WishPickExport, RoleID: confirm.RoleID, WishFormat: WishNTEUID})
		}
	case WishCancelConfirm:
		w.Confirm = WishConfirmation{}
	case WishDeleted:
		clearImportReport()
		w.Phase = WishLoading
		return start(Effect{Kind: WishList})
	case WishRequestImport:
		if w.Phase == WishLoading {
			break
		}
		return start(Effect{Kind: WishPickImport})
	case WishImportPicked:
		if e.Path == "" {
			break
		}
		return start(Effect{Kind: WishImport, RoleID: w.RoleID, Path: e.Path})
	case WishImportNeedsConfirmation:
		w.Confirm = WishConfirmation{Kind: WishConfirmImport, RoleID: e.WishRoleID, Count: e.WishCount, Path: e.Path}
	case WishImported:
		w.ImportReported = true
		w.RoleID, w.Phase, w.Failure = e.WishRoleID, WishReady, WishFailureNone
		w.NewCount, w.Outside, w.Conflicts = e.WishNewCount, e.WishOutside, e.WishConflicts
	case WishRequestExport:
		if w.RoleID == "" || w.Phase != WishReady {
			break
		}
		if e.WishFormat == WishNTEUID {
			w.Confirm = WishConfirmation{Kind: WishConfirmLossy, RoleID: w.RoleID}
			break
		}
		return start(Effect{Kind: WishPickExport, RoleID: w.RoleID, WishFormat: e.WishFormat})
	case WishExportPicked:
		if e.Path == "" {
			break
		}
		return start(Effect{Kind: WishExport, RoleID: w.RoleID, Path: e.Path, WishFormat: e.WishFormat})
	case WishOperationFailed:
		w.Phase, w.Failure = WishFailed, e.WishFailure
	}
	return m, nil, true
}
