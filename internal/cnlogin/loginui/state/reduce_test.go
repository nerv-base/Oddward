// SPDX-License-Identifier: GPL-3.0-or-later

package state

import (
	"testing"

	"github.com/nerv-base/Oddward/internal/cnlogin/loginui"
)

func phoneModel() loginui.Model {
	return loginui.Model{
		Screen:      "phone",
		Title:       "手机号登录",
		Subtitle:    "国服账号",
		CanSendSMS:  true,
		Remember:    true,
		CanOfficial: true,
	}
}

func accountsModel() loginui.Model {
	return loginui.Model{
		Screen:   "accounts",
		Title:    "选择保存的账号",
		Subtitle: "国服账号",
		Accounts: []loginui.Account{
			{ID: "a1", Label: "夜航星", MaskedPhone: "已关联塔吉多"},
			{ID: "a2", Label: "小号", MaskedPhone: ""},
		},
		SelectedAccountID: "a1",
		CanBack:           true,
		CanOfficial:       true,
	}
}

func noticeModel() loginui.Model {
	return loginui.Model{
		Screen:       "notice",
		Title:        "登录前请阅读",
		PrimaryLabel: "我已了解并同意继续",
		Notice: []loginui.Section{
			{Title: "主题 1", Body: "说明 1"},
			{Title: "主题 2", Body: "说明 2"},
		},
	}
}

func TestNoticeAcknowledgeBlocksDuplicateClicksAndHidesClose(t *testing.T) {
	var s State
	s, _ = Reduce(s, Event{Kind: EvSnapshot, Model: noticeModel()})
	if s.ShowClose() || s.ShowHelp() || s.ShowRemember() || s.ShowOtherAccount() {
		t.Fatal("notice screen exposed forbidden controls")
	}
	s, fx := Reduce(s, Event{Kind: EvClickClose})
	if len(fx) != 0 {
		t.Fatal("close emitted on notice screen")
	}
	s, fx = Reduce(s, Event{Kind: EvClickPrimary})
	if len(fx) != 1 || fx[0].Kind != EffectEmitEvent || fx[0].Event.Kind != "acknowledge" {
		t.Fatalf("unexpected acknowledge effects: %+v", fx)
	}
	// Duplicate click while pending must be rejected.
	s, fx = Reduce(s, Event{Kind: EvClickPrimary})
	if len(fx) != 0 {
		t.Fatal("duplicate acknowledge emitted while pending")
	}
	// Backend error snapshot re-enables actions.
	m := noticeModel()
	m.Status = "无法保存确认，请重试"
	s, _ = Reduce(s, Event{Kind: EvSnapshot, Model: m})
	if !s.CanAct() {
		t.Fatal("snapshot did not clear pending state")
	}
}

func TestPhoneSMSCountdownPreservesFormSelectionFocusAndRemember(t *testing.T) {
	for _, initial := range []bool{false, true} {
		var s State
		m := phoneModel()
		m.Remember = initial
		s, _ = Reduce(s, Event{Kind: EvSnapshot, Model: m})
		if s.Form.Remember != initial {
			t.Fatalf("initial remember=%v want %v", s.Form.Remember, initial)
		}
		s, _ = Reduce(s, Event{Kind: EvSetPhone, Text: "13800000000"})
		s, _ = Reduce(s, Event{Kind: EvSetCaptcha, Text: "123456"})
		s, _ = Reduce(s, Event{Kind: EvSetRemember, Bool: !initial})
		s, _ = Reduce(s, Event{Kind: EvFocusChanged, Focus: FocusCaptcha})
		s, _ = Reduce(s, Event{Kind: EvSelectionChanged, Focus: FocusCaptcha, Selection: Selection{Start: 2, End: 5}})
		s, _ = Reduce(s, Event{Kind: EvScrollChanged, FormScroll: 42, SetFormScroll: true})
		genBefore := s.FormGen

		// Send SMS and verify duplicate send is blocked until snapshot returns.
		s, fx := Reduce(s, Event{Kind: EvClickSendSMS})
		if len(fx) != 1 || fx[0].Event.Kind != "send_sms" || fx[0].Event.Phone != "13800000000" {
			t.Fatalf("send_sms effect mismatch: %+v", fx)
		}
		s, fx = Reduce(s, Event{Kind: EvClickSendSMS})
		if len(fx) != 0 {
			t.Fatal("duplicate send_sms allowed while pending")
		}

		// Simulate 1-second SMS countdown updates from Go session.
		for _, label := range []string{"60 秒后重发", "59 秒后重发", "58 秒后重发"} {
			tick := m
			tick.SMSLabel = label
			tick.CanSendSMS = false
			s, _ = Reduce(s, Event{Kind: EvSnapshot, Model: tick})
			if s.Form.Phone != "13800000000" || s.Form.Captcha != "123456" || s.Form.Remember != !initial {
				t.Fatalf("countdown lost unsubmitted form state: %+v", s.Form)
			}
			if s.Form.Focused != FocusCaptcha || s.Form.CaptchaSel != (Selection{Start: 2, End: 5}) || s.Form.FormScroll != 42 {
				t.Fatalf("countdown lost focus/selection/scroll: %+v", s.Form)
			}
			if s.FormGen != genBefore {
				t.Fatal("countdown unexpectedly bumped FormGen")
			}
			if s.CanSendSMS() {
				t.Fatal("SMS button remained enabled during cooldown")
			}
		}

		// Submit login_phone: must emit retained values and immediately clear memory + bump FormGen.
		s, fx = Reduce(s, Event{Kind: EvClickPrimary})
		if len(fx) != 1 || fx[0].Event.Kind != "login_phone" || fx[0].Event.Phone != "13800000000" || fx[0].Event.Captcha != "123456" || fx[0].Event.Remember != !initial {
			t.Fatalf("login_phone effect mismatch: %+v", fx)
		}
		if s.Form.Phone != "" || s.Form.Captcha != "" || s.FormGen <= genBefore {
			t.Fatal("login_phone did not clear phone/captcha or bump FormGen")
		}
		// Busy snapshot blocks all actions.
		busy := m
		busy.Busy = true
		s, _ = Reduce(s, Event{Kind: EvSnapshot, Model: busy})
		s, fx = Reduce(s, Event{Kind: EvClickPrimary})
		if len(fx) != 0 {
			t.Fatal("busy model accepted primary click")
		}
	}
}

func TestScreenTransitionResetsPhoneCaptchaAndSyncsRemember(t *testing.T) {
	var s State
	m := phoneModel()
	m.Remember = false
	s, _ = Reduce(s, Event{Kind: EvSnapshot, Model: m})
	s, _ = Reduce(s, Event{Kind: EvSetPhone, Text: "13800000000"})
	s, _ = Reduce(s, Event{Kind: EvSetCaptcha, Text: "654321"})
	s, _ = Reduce(s, Event{Kind: EvSetRemember, Bool: true})

	acc := accountsModel()
	acc.Remember = true
	s, _ = Reduce(s, Event{Kind: EvSnapshot, Model: acc})
	if s.Form.Phone != "" || s.Form.Captcha != "" || s.Form.Remember != true {
		t.Fatalf("transition to accounts failed to reset form: %+v", s.Form)
	}
	backPhone := phoneModel()
	backPhone.Remember = false
	s, _ = Reduce(s, Event{Kind: EvSnapshot, Model: backPhone})
	if s.Form.Remember != false {
		t.Fatal("returning to phone screen did not sync backend remember value")
	}
}

func TestAccountsSelectRenameDeleteAndKeyboardShortcuts(t *testing.T) {
	var s State
	s, _ = Reduce(s, Event{Kind: EvSnapshot, Model: accountsModel()})

	// Clicking already-selected account emits nothing.
	s, fx := Reduce(s, Event{Kind: EvSelectAccount, AccountID: "a1"})
	if len(fx) != 0 {
		t.Fatal("selecting already-selected account emitted redundant event")
	}
	// Clicking another account emits select_account.
	s, fx = Reduce(s, Event{Kind: EvSelectAccount, AccountID: "a2"})
	if len(fx) != 1 || fx[0].Event.Kind != "select_account" || fx[0].Event.AccountID != "a2" {
		t.Fatalf("select_account mismatch: %+v", fx)
	}

	// Backend updates selection back to a1.
	s, _ = Reduce(s, Event{Kind: EvSnapshot, Model: accountsModel()})

	// Rename flow: enters inline edit, disables primary button, Enter confirms.
	s, _ = Reduce(s, Event{Kind: EvBeginRename})
	if s.Form.EditMode != EditRename || s.Form.RenameDraft != "夜航星" || s.CanPrimary() {
		t.Fatalf("rename mode setup invalid: mode=%v draft=%q canPrimary=%v", s.Form.EditMode, s.Form.RenameDraft, s.CanPrimary())
	}
	s, _ = Reduce(s, Event{Kind: EvSetRenameDraft, Text: "合成改名"})
	// Escape cancels edit without closing the window.
	s, fx = Reduce(s, Event{Kind: EvPressEscape})
	if len(fx) != 0 || s.Form.EditMode != EditNone {
		t.Fatalf("Escape did not cancel inline edit: mode=%v fx=%+v", s.Form.EditMode, fx)
	}

	s, _ = Reduce(s, Event{Kind: EvBeginRename})
	s, _ = Reduce(s, Event{Kind: EvSetRenameDraft, Text: "新名称"})
	s, fx = Reduce(s, Event{Kind: EvPressEnter})
	if len(fx) != 1 || fx[0].Event.Kind != "rename_account" || fx[0].Event.AccountID != "a1" || fx[0].Event.Label != "新名称" {
		t.Fatalf("rename confirm mismatch: %+v", fx)
	}

	// Delete flow requires explicit confirmation.
	s, _ = Reduce(s, Event{Kind: EvSnapshot, Model: accountsModel()})
	s, fx = Reduce(s, Event{Kind: EvBeginDelete})
	if len(fx) != 0 || s.Form.EditMode != EditDelete || s.CanPrimary() {
		t.Fatalf("delete mode emitted immediately or left primary enabled: %+v", fx)
	}
	s, fx = Reduce(s, Event{Kind: EvConfirmDelete})
	if len(fx) != 1 || fx[0].Event.Kind != "delete_account" || fx[0].Event.AccountID != "a1" {
		t.Fatalf("delete confirm mismatch: %+v", fx)
	}

	// Login saved account via Primary / Enter.
	s, _ = Reduce(s, Event{Kind: EvSnapshot, Model: accountsModel()})
	s, fx = Reduce(s, Event{Kind: EvPressEnter})
	if len(fx) != 1 || fx[0].Event.Kind != "login_saved" || fx[0].Event.AccountID != "a1" {
		t.Fatalf("login_saved mismatch: %+v", fx)
	}

	// Escape outside edit mode requests window close.
	s, _ = Reduce(s, Event{Kind: EvSnapshot, Model: accountsModel()})
	s, fx = Reduce(s, Event{Kind: EvPressEscape})
	if len(fx) != 1 || fx[0].Kind != EffectRequestClose {
		t.Fatalf("Escape did not request close: %+v", fx)
	}
}

func TestCaptureAndRestoreFormAcrossRecreation(t *testing.T) {
	var s State
	s, _ = Reduce(s, Event{Kind: EvSnapshot, Model: phoneModel()})
	s, _ = Reduce(s, Event{Kind: EvSetPhone, Text: "13800000000"})
	s, _ = Reduce(s, Event{Kind: EvSetCaptcha, Text: "123456"})
	s, _ = Reduce(s, Event{Kind: EvSetRemember, Bool: false})
	s, _ = Reduce(s, Event{Kind: EvFocusChanged, Focus: FocusCaptcha})
	s, _ = Reduce(s, Event{Kind: EvSelectionChanged, Focus: FocusPhone, Selection: Selection{Start: 2, End: 5}})
	s, _ = Reduce(s, Event{Kind: EvSelectionChanged, Focus: FocusCaptcha, Selection: Selection{Start: 1, End: 5}})

	captured := s.CaptureForm()
	// Recreated view starts from latest backend model (e.g., countdown tick).
	latest := phoneModel()
	latest.SMSLabel = "59 秒后重发"
	latest.CanSendSMS = false
	var next State
	next, _ = Reduce(next, Event{Kind: EvSnapshot, Model: latest})
	next = next.RestoreForm(captured.ClearPending())

	if next.Form.Phone != "13800000000" || next.Form.Captcha != "123456" || next.Form.Remember != false {
		t.Fatalf("restored form values lost: %+v", next.Form)
	}
	if next.Form.PhoneSel != (Selection{Start: 2, End: 5}) || next.Form.CaptchaSel != (Selection{Start: 1, End: 5}) {
		t.Fatalf("restored selections lost: %+v", next.Form)
	}
	if next.RestoreFocus != FocusCaptcha || !next.RestoreSelection {
		t.Fatalf("restore flags not armed: focus=%v sel=%v", next.RestoreFocus, next.RestoreSelection)
	}
	next, _ = Reduce(next, Event{Kind: EvConsumeRestore})
	if next.RestoreFocus != FocusNone || next.RestoreSelection {
		t.Fatal("consume restore did not clear flags")
	}
}

func TestWelcomeSnapshotEmitsBannerEffect(t *testing.T) {
	var s State
	s, fx := Reduce(s, Event{Kind: EvSnapshot, Model: loginui.Model{Screen: "welcome", Banner: "夜航星，欢迎登录"}})
	if len(fx) != 1 || fx[0].Kind != EffectEnterBanner {
		t.Fatalf("welcome effect mismatch: %+v", fx)
	}
	if s.ShowClose() || s.ShowHelp() || s.ShowRemember() || s.CanPrimary() {
		t.Fatal("welcome screen exposed login controls")
	}
}
