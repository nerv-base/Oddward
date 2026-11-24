// SPDX-License-Identifier: GPL-3.0-or-later

// Package state holds the pure UI state machine for the MyGo Native UI login
// window and welcome banner. It performs no I/O, clocks, goroutines or UI
// toolkit calls; all protocol business logic remains in internal/cnlogin/session.
package state

import "github.com/nerv-base/Oddward/internal/cnlogin/loginui"

// EditMode is the inline editor mode active on the saved-accounts screen.
type EditMode uint8

const (
	EditNone EditMode = iota
	EditRename
	EditDelete
)

// FocusTarget identifies which text input should retain logical focus across
// same-screen refreshes and HWND recreations.
type FocusTarget uint8

const (
	FocusNone FocusTarget = iota
	FocusPhone
	FocusCaptcha
	FocusRename
)

// Selection records rune-offset text selection [Start, End] inside a text input.
type Selection struct {
	Start int
	End   int
}

// FormState holds memory-only unsubmitted UI state that survives same-screen
// countdown updates and controlled view recreations. It has no value-printing
// String method so phone numbers and captchas cannot leak into logs.
type FormState struct {
	Screen         string
	Phone          string
	Captcha        string
	Remember       bool
	PhoneSel       Selection
	CaptchaSel     Selection
	Focused        FocusTarget
	EditMode       EditMode
	EditAccountID  string
	RenameDraft    string
	RenameSel      Selection
	FormScroll     float32
	AccountsScroll float32
	Pending        bool
}

// State is the complete presentation state of the login UI helper.
type State struct {
	HasModel         bool
	Model            loginui.Model
	Form             FormState
	FormGen          uint64
	RestoreFocus     FocusTarget
	RestoreSelection bool
}

// EventKind classifies one input to Reduce.
type EventKind uint8

const (
	EvSnapshot EventKind = iota + 1
	EvSetPhone
	EvSetCaptcha
	EvSetRemember
	EvSetRenameDraft
	EvFocusChanged
	EvSelectionChanged
	EvScrollChanged
	EvClickPrimary
	EvClickSendSMS
	EvClickOtherAccount
	EvClickOfficial
	EvClickHelp
	EvClickBack
	EvClickClose
	EvSelectAccount
	EvBeginRename
	EvBeginDelete
	EvCancelEdit
	EvConfirmRename
	EvConfirmDelete
	EvPressEnter
	EvPressEscape
	EvConsumeRestore
)

// Event is one pure state-machine transition input.
type Event struct {
	Kind           EventKind
	Model          loginui.Model
	Text           string
	Bool           bool
	AccountID      string
	Focus          FocusTarget
	Selection      Selection
	FormScroll     float32
	AccountsScroll float32
	SetFormScroll  bool
	SetAccScroll   bool
}

// EffectKind classifies one side effect emitted by Reduce.
type EffectKind uint8

const (
	EffectEmitEvent EffectKind = iota + 1
	EffectRequestClose
	EffectEnterBanner
)

// Effect describes a side effect for the helper shell to execute.
type Effect struct {
	Kind  EffectKind
	Event loginui.Event
}

// CanAct reports whether interactive controls accept user intents.
func (s State) CanAct() bool {
	return s.HasModel && !s.Form.Pending && !s.Model.Busy
}

// SelectedAccount returns the currently selected saved account, if present.
func (s State) SelectedAccount() *loginui.Account {
	if s.Model.SelectedAccountID == "" {
		return nil
	}
	for i := range s.Model.Accounts {
		if s.Model.Accounts[i].ID == s.Model.SelectedAccountID {
			acc := s.Model.Accounts[i]
			return &acc
		}
	}
	return nil
}

// EditingAccount returns the account targeted by an active inline rename/delete.
func (s State) EditingAccount() *loginui.Account {
	if s.Form.EditMode == EditNone || s.Form.EditAccountID == "" {
		return nil
	}
	for i := range s.Model.Accounts {
		if s.Model.Accounts[i].ID == s.Form.EditAccountID {
			acc := s.Model.Accounts[i]
			return &acc
		}
	}
	return nil
}

// CanPrimary reports whether the primary footer button is interactive.
func (s State) CanPrimary() bool {
	if !s.CanAct() || s.Form.EditMode != EditNone {
		return false
	}
	if s.Model.Screen == "accounts" && s.SelectedAccount() == nil {
		return false
	}
	return s.Model.Screen == "notice" || s.Model.Screen == "phone" || s.Model.Screen == "accounts"
}

// CanSendSMS reports whether the SMS code button is interactive.
func (s State) CanSendSMS() bool {
	return s.CanAct() && s.Model.Screen == "phone" && s.Model.CanSendSMS
}

// Title returns the rendered heading title with screen-specific fallbacks.
func (s State) Title() string {
	if s.Model.Title != "" {
		return s.Model.Title
	}
	switch s.Model.Screen {
	case "notice":
		return "登录前请阅读"
	case "accounts":
		return "选择保存的账号"
	default:
		return "手机号登录"
	}
}

// PrimaryLabel returns the primary button label with screen-specific fallbacks.
func (s State) PrimaryLabel() string {
	if s.Model.PrimaryLabel != "" {
		return s.Model.PrimaryLabel
	}
	if s.Model.Screen == "notice" {
		return "我已了解并继续"
	}
	return "登录"
}

// SMSLabel returns the SMS action button label with default fallback.
func (s State) SMSLabel() string {
	if s.Model.SMSLabel != "" {
		return s.Model.SMSLabel
	}
	return "发送验证码"
}

// ShowBack reports whether the header back button is visible.
func (s State) ShowBack() bool { return s.Model.CanBack }

// ShowHelp reports whether the header help button is visible.
func (s State) ShowHelp() bool { return s.Model.Screen != "notice" && s.Model.Screen != "welcome" }

// ShowClose reports whether the header close button is visible.
func (s State) ShowClose() bool { return s.Model.Screen != "notice" && s.Model.Screen != "welcome" }

// ShowRemember reports whether the save-account checkbox is visible.
func (s State) ShowRemember() bool { return s.Model.Screen == "phone" }

// ShowOtherAccount reports whether the "other account" secondary button is visible.
func (s State) ShowOtherAccount() bool { return s.Model.Screen == "accounts" }

// ShowOfficial reports whether the "official launcher" secondary button is visible.
func (s State) ShowOfficial() bool { return s.Model.CanOfficial }

// CaptureForm returns an in-memory snapshot of unsubmitted form state.
func (s State) CaptureForm() FormState {
	f := s.Form
	f.Screen = s.Model.Screen
	return f
}

// RestoreForm restores a previously captured FormState when the active screen
// matches, arming one-shot focus and selection restoration for the new view.
func (s State) RestoreForm(captured FormState) State {
	if !s.HasModel || captured.Screen != s.Model.Screen {
		return s
	}
	s.Form = captured
	s.Form.Screen = s.Model.Screen
	s.RestoreFocus = captured.Focused
	s.RestoreSelection = true
	return s
}

// ClearPendingOnSnapshot mirrors LoginViewController's rule that a fresh
// backend snapshot arriving during view recreation clears an in-flight action.
func (f FormState) ClearPending() FormState {
	f.Pending = false
	return f
}
