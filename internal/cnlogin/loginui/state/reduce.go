// SPDX-License-Identifier: GPL-3.0-or-later

package state

import "github.com/nerv-base/Oddward/internal/cnlogin/loginui"

// Reduce applies ev to s and returns the next state and emitted side effects.
func Reduce(s State, ev Event) (State, []Effect) {
	switch ev.Kind {
	case EvSnapshot:
		return applySnapshot(s, ev.Model)
	case EvSetPhone:
		s.Form.Phone = ev.Text
		return s, nil
	case EvSetCaptcha:
		s.Form.Captcha = ev.Text
		return s, nil
	case EvSetRemember:
		s.Form.Remember = ev.Bool
		return s, nil
	case EvSetRenameDraft:
		s.Form.RenameDraft = ev.Text
		return s, nil
	case EvFocusChanged:
		s.Form.Focused = ev.Focus
		return s, nil
	case EvSelectionChanged:
		switch ev.Focus {
		case FocusPhone:
			s.Form.PhoneSel = ev.Selection
		case FocusCaptcha:
			s.Form.CaptchaSel = ev.Selection
		case FocusRename:
			s.Form.RenameSel = ev.Selection
		}
		return s, nil
	case EvScrollChanged:
		if ev.SetFormScroll {
			s.Form.FormScroll = ev.FormScroll
		}
		if ev.SetAccScroll {
			s.Form.AccountsScroll = ev.AccountsScroll
		}
		return s, nil
	case EvConsumeRestore:
		s.RestoreFocus = FocusNone
		s.RestoreSelection = false
		return s, nil
	case EvClickPrimary:
		return clickPrimary(s)
	case EvClickSendSMS:
		if !s.CanSendSMS() {
			return s, nil
		}
		return emitIntent(s, loginui.Event{Kind: "send_sms", Phone: s.Form.Phone})
	case EvClickOtherAccount:
		if !s.CanAct() || s.Model.Screen != "accounts" {
			return s, nil
		}
		return emitIntent(s, loginui.Event{Kind: "other_account"})
	case EvClickOfficial:
		if !s.CanAct() || !s.Model.CanOfficial {
			return s, nil
		}
		return emitIntent(s, loginui.Event{Kind: "official"})
	case EvClickHelp:
		if !s.CanAct() || !s.ShowHelp() {
			return s, nil
		}
		return emitIntent(s, loginui.Event{Kind: "help"})
	case EvClickBack:
		if !s.CanAct() || !s.Model.CanBack {
			return s, nil
		}
		return emitIntent(s, loginui.Event{Kind: "back"})
	case EvClickClose:
		if !s.ShowClose() {
			return s, nil
		}
		return s, []Effect{{Kind: EffectRequestClose}}
	case EvSelectAccount:
		if !s.CanAct() || s.Model.Screen != "accounts" || s.Form.EditMode != EditNone {
			return s, nil
		}
		if ev.AccountID == "" || ev.AccountID == s.Model.SelectedAccountID || !hasAccount(s.Model.Accounts, ev.AccountID) {
			return s, nil
		}
		return emitIntent(s, loginui.Event{Kind: "select_account", AccountID: ev.AccountID})
	case EvBeginRename:
		if !s.CanAct() || s.Model.Screen != "accounts" {
			return s, nil
		}
		sel := s.SelectedAccount()
		if sel == nil {
			return s, nil
		}
		s.Form.EditMode = EditRename
		s.Form.EditAccountID = sel.ID
		s.Form.RenameDraft = sel.Label
		s.Form.RenameSel = Selection{Start: 0, End: len([]rune(sel.Label))}
		s.Form.Focused = FocusRename
		s.RestoreFocus = FocusRename
		s.RestoreSelection = true
		return s, nil
	case EvBeginDelete:
		if !s.CanAct() || s.Model.Screen != "accounts" {
			return s, nil
		}
		sel := s.SelectedAccount()
		if sel == nil {
			return s, nil
		}
		s.Form.EditMode = EditDelete
		s.Form.EditAccountID = sel.ID
		s.Form.RenameDraft = ""
		return s, nil
	case EvCancelEdit:
		if s.Form.EditMode == EditNone {
			return s, nil
		}
		s.Form.EditMode = EditNone
		s.Form.EditAccountID = ""
		s.Form.RenameDraft = ""
		s.Form.RenameSel = Selection{}
		s.Form.Focused = FocusNone
		return s, nil
	case EvConfirmRename:
		if !s.CanAct() || s.Form.EditMode != EditRename || s.Form.EditAccountID == "" {
			return s, nil
		}
		return emitIntent(s, loginui.Event{
			Kind:      "rename_account",
			AccountID: s.Form.EditAccountID,
			Label:     s.Form.RenameDraft,
		})
	case EvConfirmDelete:
		if !s.CanAct() || s.Form.EditMode != EditDelete || s.Form.EditAccountID == "" {
			return s, nil
		}
		return emitIntent(s, loginui.Event{
			Kind:      "delete_account",
			AccountID: s.Form.EditAccountID,
		})
	case EvPressEnter:
		switch s.Form.EditMode {
		case EditRename:
			return Reduce(s, Event{Kind: EvConfirmRename})
		case EditDelete:
			return Reduce(s, Event{Kind: EvConfirmDelete})
		default:
			return clickPrimary(s)
		}
	case EvPressEscape:
		if s.Form.EditMode != EditNone {
			return Reduce(s, Event{Kind: EvCancelEdit})
		}
		if s.ShowClose() {
			return s, []Effect{{Kind: EffectRequestClose}}
		}
		return s, nil
	default:
		return s, nil
	}
}

func applySnapshot(s State, m loginui.Model) (State, []Effect) {
	if m.Screen == "welcome" {
		s.HasModel = true
		s.Model = m
		s.Form.Screen = m.Screen
		s.Form.Pending = false
		s.Form.Phone = ""
		s.Form.Captcha = ""
		s.Form.EditMode = EditNone
		s.Form.EditAccountID = ""
		s.Form.RenameDraft = ""
		return s, []Effect{{Kind: EffectEnterBanner}}
	}
	screenChanged := !s.HasModel || s.Model.Screen != m.Screen
	if screenChanged {
		s.Form = FormState{
			Screen:   m.Screen,
			Remember: m.Remember,
		}
		s.FormGen++
		s.RestoreFocus = FocusNone
		s.RestoreSelection = false
	} else {
		// Same-screen updates (such as 1-second SMS countdown ticks) preserve
		// unsubmitted phone, captcha, remember, selection, focus and scroll.
		s.Form.Screen = m.Screen
		s.Form.Pending = false
		s.Form.EditMode = EditNone
		s.Form.EditAccountID = ""
		s.Form.RenameDraft = ""
		s.Form.RenameSel = Selection{}
	}
	s.HasModel = true
	s.Model = m
	return s, nil
}

func clickPrimary(s State) (State, []Effect) {
	if !s.CanPrimary() {
		return s, nil
	}
	switch s.Model.Screen {
	case "notice":
		return emitIntent(s, loginui.Event{Kind: "acknowledge"})
	case "phone":
		ev := loginui.Event{
			Kind:     "login_phone",
			Phone:    s.Form.Phone,
			Captcha:  s.Form.Captcha,
			Remember: s.Form.Remember,
		}
		// Immediately wipe unsubmitted phone/captcha from memory and bump
		// FormGen so text editors discard any undo history of the captcha.
		s.Form.Phone = ""
		s.Form.Captcha = ""
		s.Form.PhoneSel = Selection{}
		s.Form.CaptchaSel = Selection{}
		s.FormGen++
		return emitIntent(s, ev)
	case "accounts":
		sel := s.SelectedAccount()
		if sel == nil {
			return s, nil
		}
		return emitIntent(s, loginui.Event{Kind: "login_saved", AccountID: sel.ID})
	default:
		return s, nil
	}
}

func emitIntent(s State, ev loginui.Event) (State, []Effect) {
	s.Form.Pending = true
	return s, []Effect{{Kind: EffectEmitEvent, Event: ev}}
}

func hasAccount(accounts []loginui.Account, id string) bool {
	for _, a := range accounts {
		if a.ID == id {
			return true
		}
	}
	return false
}
