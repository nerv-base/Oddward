// SPDX-License-Identifier: GPL-3.0-or-later

package native

import (
	"fmt"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/cnlogin/loginui"
	"github.com/nerv-base/Oddward/internal/cnlogin/loginui/state"
)

func boolPtr(v bool) *bool { return &v }

func fixturePhone() loginui.Model {
	return loginui.Model{
		Screen:      "phone",
		Title:       "手机号登录",
		Subtitle:    "国服账号",
		CanSendSMS:  true,
		Remember:    true,
		CanOfficial: true,
	}
}

func fixtureAccounts() loginui.Model {
	return loginui.Model{
		Screen:   "accounts",
		Title:    "选择保存的账号",
		Subtitle: "国服账号",
		Accounts: []loginui.Account{
			{ID: "fixture-a", Label: "合成账号 A", MaskedPhone: "138****0000"},
			{ID: "fixture-b", Label: "合成账号 B", MaskedPhone: "139****0000"},
		},
		SelectedAccountID: "fixture-a",
		CanBack:           true,
		CanOfficial:       true,
	}
}

func fixtureNotice() loginui.Model {
	sections := make([]loginui.Section, 8)
	for i := range sections {
		sections[i] = loginui.Section{
			Title: fmt.Sprintf("主题 %d", i+1),
			Body:  "合成告知文字：用途、风险、共享、数据、存储、网络、保留与选择均由 Go 后端提供。这里不保存任何确认状态。",
		}
	}
	return loginui.Model{
		Screen:       "notice",
		Title:        "登录前请阅读",
		Subtitle:     "国服账号登录说明",
		PrimaryLabel: "我已了解并同意继续",
		Notice:       sections,
	}
}

func fixtureWelcome() loginui.Model {
	return loginui.Model{
		Screen:           "welcome",
		Banner:           "夜航星，欢迎登录",
		GameProcessID:    42,
		GameCreationTime: "133000000000000000",
	}
}

func TestNoticeViewRendersAllTopicsAndBlocksDuplicateAcknowledge(t *testing.T) {
	var emitted []loginui.Event
	v := NewViewShell(func(fx state.Effect) {
		if fx.Kind == state.EffectEmitEvent {
			emitted = append(emitted, fx.Event)
		}
	})
	v.ApplyModel(fixtureNotice())
	tt := ui.NewTester(v.View, 420, 520)

	for i := 1; i <= 8; i++ {
		if !tt.HasText(fmt.Sprintf("主题 %d", i)) {
			t.Fatalf("missing notice topic %d", i)
		}
	}
	if _, ok := tt.AccessByRole(ui.RoleButton, "使用官方启动器"); ok {
		t.Fatal("official button shown when CanOfficial=false")
	}
	if _, ok := tt.AccessByRole(ui.RoleButton, "关闭"); ok {
		t.Fatal("close button shown on notice screen")
	}
	if err := tt.Click("我已了解并同意继续"); err != nil {
		t.Fatalf("click primary: %v", err)
	}
	_ = tt.Click("我已了解并同意继续")
	if len(emitted) != 1 || emitted[0].Kind != "acknowledge" {
		t.Fatalf("expected single pending acknowledge, got %+v", emitted)
	}
}

func TestPhoneViewInputSMSCountdownAndSubmitClear(t *testing.T) {
	var emitted []loginui.Event
	v := NewViewShell(func(fx state.Effect) {
		if fx.Kind == state.EffectEmitEvent {
			emitted = append(emitted, fx.Event)
		}
	})
	m := fixturePhone()
	v.ApplyModel(m)
	tt := ui.NewTester(v.View, 420, 520)

	if err := tt.Click("请输入手机号"); err != nil {
		t.Fatalf("focus phone: %v", err)
	}
	tt.Type("13800000000")
	tt.Frame()
	if v.State.Form.Phone != "13800000000" {
		t.Fatalf("phone=%q want 13800000000", v.State.Form.Phone)
	}

	if err := tt.Click("发送验证码"); err != nil {
		t.Fatalf("click send_sms: %v", err)
	}
	_ = tt.Click("发送验证码")
	if len(emitted) != 1 || emitted[0].Kind != "send_sms" || emitted[0].Phone != "13800000000" {
		t.Fatalf("send_sms mismatch: %+v", emitted)
	}

	// Simulate SMS countdown snapshot from backend: phone input must survive.
	cd := m
	cd.SMSLabel = "58 秒后重发"
	cd.CanSendSMS = false
	v.ApplyModel(cd)
	tt.Frame()
	if v.State.Form.Phone != "13800000000" {
		t.Fatalf("countdown wiped phone input: %q", v.State.Form.Phone)
	}

	if err := tt.Click("请输入验证码"); err != nil {
		t.Fatalf("focus captcha: %v", err)
	}
	tt.Type("123456")
	tt.Frame()
	if err := tt.Click("登录"); err != nil {
		t.Fatalf("click primary login: %v", err)
	}
	if len(emitted) != 2 || emitted[1].Kind != "login_phone" || emitted[1].Phone != "13800000000" || emitted[1].Captcha != "123456" || !emitted[1].Remember {
		t.Fatalf("login_phone mismatch: %+v", emitted)
	}
	if v.State.Form.Phone != "" || v.State.Form.Captcha != "" {
		t.Fatal("submitted phone/captcha not cleared from memory")
	}
}

func TestOverlayCardCenteredProportionalAndRespectsPreferences(t *testing.T) {
	for _, tc := range []struct {
		hc, trans bool
		alpha     uint8
	}{
		{false, true, 166},
		{false, false, 255},
		{true, true, 255},
		{true, false, 255},
	} {
		got := OverlayMaskColor(tc.hc, tc.trans)
		if got.A != tc.alpha || got.R != 0 || got.G != 0 || got.B != 0 {
			t.Fatalf("mask color hc=%v trans=%v got=%+v want alpha=%d", tc.hc, tc.trans, got, tc.alpha)
		}
	}

	for _, sz := range [][2]int{{1280, 720}, {600, 900}, {360, 240}} {
		v := NewViewShell(nil)
		v.OverlayMode = true
		v.HighContrastOverride = boolPtr(false)
		v.TransparencyOverride = boolPtr(true)
		v.ApplyModel(fixturePhone())
		tt := ui.NewTester(v.View, sz[0], sz[1])
		tt.Frame() // second frame settles zoom
		dialog, ok := tt.AccessByRole(ui.RoleDialog, "社区/非官方登录窗口")
		if !ok {
			t.Fatalf("%dx%d: login card dialog missing from accessibility tree", sz[0], sz[1])
		}
		cx := float64(dialog.Bounds.X + dialog.Bounds.W/2)
		cy := float64(dialog.Bounds.Y + dialog.Bounds.H/2)
		if math.Abs(cx-float64(sz[0])/2) > 1.5 || math.Abs(cy-float64(sz[1])/2) > 1.5 {
			t.Fatalf("%dx%d: card not centered: bounds=%+v", sz[0], sz[1], dialog.Bounds)
		}
		if float64(dialog.Bounds.W) > float64(sz[0])*0.9+1.5 || float64(dialog.Bounds.H) > float64(sz[1])*0.8+1.5 {
			t.Fatalf("%dx%d: card exceeds 90%%x80%% budget: bounds=%+v", sz[0], sz[1], dialog.Bounds)
		}
		ratio := float64(dialog.Bounds.W) / float64(dialog.Bounds.H)
		if math.Abs(ratio-float64(cardWidth)/float64(cardHeight)) > 0.01 {
			t.Fatalf("%dx%d: card aspect ratio drifted: %.4f", sz[0], sz[1], ratio)
		}
		if _, ok := tt.AccessByRole(ui.RoleTextField, "请输入手机号"); !ok {
			t.Fatalf("%dx%d: phone input missing accessibility node inside zoomed overlay", sz[0], sz[1])
		}
	}
}

func TestAccountsViewRenameDeleteAndLogin(t *testing.T) {
	var emitted []loginui.Event
	v := NewViewShell(func(fx state.Effect) {
		if fx.Kind == state.EffectEmitEvent {
			emitted = append(emitted, fx.Event)
		}
	})
	v.ApplyModel(fixtureAccounts())
	tt := ui.NewTester(v.View, 420, 520)

	if len(emitted) != 0 {
		t.Fatal("initial snapshot emitted an action")
	}
	if err := tt.Click("重命名"); err != nil {
		t.Fatalf("click rename: %v", err)
	}
	if err := tt.Click("账号名称"); err != nil {
		t.Fatalf("focus account_label: %v", err)
	}
	// Replace existing label text via state event / select-all + type.
	tt.Key(ui.Ctrl, ui.KeyA)
	tt.Type("合成改名")
	tt.Frame()
	if err := tt.Click("保存名称"); err != nil {
		t.Fatalf("click confirm_rename: %v", err)
	}
	if len(emitted) != 1 || emitted[0].Kind != "rename_account" || emitted[0].AccountID != "fixture-a" || emitted[0].Label != "合成改名" {
		t.Fatalf("rename mismatch: %+v", emitted)
	}

	v.ApplyModel(fixtureAccounts())
	tt.Frame()
	if err := tt.Click("删除账号"); err != nil {
		t.Fatalf("click delete: %v", err)
	}
	if len(emitted) != 1 {
		t.Fatal("delete emitted without confirmation")
	}
	if err := tt.Click("确认删除"); err != nil {
		t.Fatalf("click confirm_delete: %v", err)
	}
	if len(emitted) != 2 || emitted[1].Kind != "delete_account" || emitted[1].AccountID != "fixture-a" {
		t.Fatalf("delete mismatch: %+v", emitted)
	}

	v.ApplyModel(fixtureAccounts())
	tt.Frame()
	if err := tt.Click("登录"); err != nil {
		t.Fatalf("click login_saved: %v", err)
	}
	if len(emitted) != 3 || emitted[2].Kind != "login_saved" || emitted[2].AccountID != "fixture-a" {
		t.Fatalf("login_saved mismatch: %+v", emitted)
	}
}

func TestWelcomeBannerGeometryTrimmingPreferencesAndTiming(t *testing.T) {
	for _, sz := range [][2]int{{1280, 720}, {600, 900}, {360, 240}} {
		v := NewViewShell(nil)
		v.HighContrastOverride = boolPtr(false)
		v.TransparencyOverride = boolPtr(true)
		v.ApplyModel(fixtureWelcome())
		tt := ui.NewTester(v.View, sz[0], sz[1])
		card, ok := tt.Find("欢迎登录横幅")
		if !ok {
			t.Fatalf("%dx%d: banner card not found", sz[0], sz[1])
		}
		cx := float64(card.X + card.W/2)
		if math.Abs(cx-float64(sz[0])/2) > 1.5 {
			t.Fatalf("%dx%d: banner not horizontally centered: %+v", sz[0], sz[1], card)
		}
		if math.Abs(float64(card.Y)-float64(bannerTopInset)) > 1.5 {
			t.Fatalf("%dx%d: banner top inset %.1f want %.1f", sz[0], sz[1], card.Y, bannerTopInset)
		}
		mark, okMark := tt.Find("Oddward")
		text, okText := tt.Find("夜航星，欢迎登录")
		if !okMark || !okText {
			t.Fatalf("%dx%d: banner mark/text missing: mark=%v text=%v", sz[0], sz[1], okMark, okText)
		}
		if mark.W < 15 || mark.H < 15 || mark.X+mark.W > text.X {
			t.Fatalf("%dx%d: mark must precede text: mark=%+v text=%+v", sz[0], sz[1], mark, text)
		}
	}

	// Narrow client trims a long account greeting cleanly inside client width.
	narrow := NewViewShell(nil)
	longWelcome := fixtureWelcome()
	longWelcome.Banner = strings.Repeat("长", 60)
	narrow.ApplyModel(longWelcome)
	ttNarrow := ui.NewTester(narrow.View, 360, 240)
	card, ok := ttNarrow.Find("欢迎登录横幅")
	if !ok || card.W > 360.5 {
		t.Fatalf("long banner overflowed narrow client: ok=%v bounds=%+v", ok, card)
	}

	// Preference opaque fallbacks.
	bgNormal, _, _ := BannerColors(false, true)
	bgOpaque, _, _ := BannerColors(false, false)
	bgHC, _, fgHC := BannerColors(true, true)
	if bgNormal.A != 235 || bgOpaque.A != 255 || bgHC.A != 255 || fgHC != colWhite {
		t.Fatalf("banner colors mismatch: normal=%+v opaque=%+v hc=%+v/%+v", bgNormal, bgOpaque, bgHC, fgHC)
	}

	// Animation curve timing (220ms fade-in, 2400ms hold, 380ms fade-out) & ReduceMotion.
	if a, done := BannerOpacity(0, false); a != 0 || done {
		t.Fatalf("t=0: got (%v,%v)", a, done)
	}
	if a, done := BannerOpacity(BannerFadeIn, false); a != 1 || done {
		t.Fatalf("t=220ms: got (%v,%v)", a, done)
	}
	if a, done := BannerOpacity(BannerFadeIn+BannerHold-time.Millisecond, false); a != 1 || done {
		t.Fatalf("end of hold: got (%v,%v)", a, done)
	}
	if a, done := BannerOpacity(BannerTotal, false); a != 0 || !done {
		t.Fatalf("t=3000ms: got (%v,%v)", a, done)
	}
	if a, done := BannerOpacity(0, true); a != 1 || done {
		t.Fatalf("reduceMotion t=0: got (%v,%v)", a, done)
	}
	if a, done := BannerOpacity(BannerHold, true); a != 0 || !done {
		t.Fatalf("reduceMotion t=2400ms: got (%v,%v)", a, done)
	}
}

// TestRenderLoginUIPreviewFrames renders visual preview PNGs into
// ODDWARD_LOGINUI_PREVIEW_OUT when set, so every screen can be inspected directly.
func TestRenderLoginUIPreviewFrames(t *testing.T) {
	out := os.Getenv("ODDWARD_LOGINUI_PREVIEW_OUT")
	if out == "" {
		t.Skip("set ODDWARD_LOGINUI_PREVIEW_OUT to render login UI previews")
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	save := func(name string, w, h int, overlay bool, m loginui.Model, setup func(*ViewShell, *ui.Tester)) {
		v := NewViewShell(nil)
		v.OverlayMode = overlay
		v.HighContrastOverride = boolPtr(false)
		v.TransparencyOverride = boolPtr(true)
		v.ApplyModel(m)
		tt := ui.NewTester(v.View, w, h)
		tt.Frame()
		if setup != nil {
			setup(v, tt)
			tt.Frame()
		}
		img := tt.Image()
		f, err := os.Create(filepath.Join(out, name))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
	}

	save("notice.png", 420, 520, false, fixtureNotice(), nil)
	phoneErr := fixturePhone()
	phoneErr.Status = "验证码已发送，请查收短信。"
	phoneErr.SMSLabel = "58 秒后重发"
	phoneErr.CanSendSMS = false
	save("phone.png", 420, 520, false, phoneErr, func(v *ViewShell, _ *ui.Tester) {
		v.Dispatch(state.Event{Kind: state.EvSetPhone, Text: "13800000000"})
		v.Dispatch(state.Event{Kind: state.EvSetCaptcha, Text: "123456"})
	})
	save("accounts.png", 420, 520, false, fixtureAccounts(), nil)
	save("accounts-rename.png", 420, 520, false, fixtureAccounts(), func(v *ViewShell, _ *ui.Tester) {
		v.Dispatch(state.Event{Kind: state.EvBeginRename})
	})
	save("overlay-1280x720.png", 1280, 720, true, fixturePhone(), nil)
	save("welcome-1280x720.png", 1280, 720, true, fixtureWelcome(), nil)
}

func TestIMECompositionClipboardNavigationAndDPIScales(t *testing.T) {
	var emitted []loginui.Event
	v := NewViewShell(func(fx state.Effect) {
		if fx.Kind == state.EffectEmitEvent {
			emitted = append(emitted, fx.Event)
		}
	})
	v.ApplyModel(fixtureAccounts())
	tt := ui.NewTester(v.View, 420, 520)

	// Open inline rename and test Chinese IME preedit + commit + Enter submit.
	if err := tt.Click("重命名"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("账号名称"); err != nil {
		t.Fatal(err)
	}
	active, noIME := tt.TextInputActive()
	if !active || noIME {
		t.Fatalf("rename input IME state active=%v noIME=%v, want (true,false)", active, noIME)
	}
	if caret, ok := tt.TextCaret(); !ok || caret.H <= 0 {
		t.Fatalf("rename input caret unavailable for IME candidate positioning: %+v %v", caret, ok)
	}
	tt.Key(ui.Ctrl, ui.KeyA)
	tt.Key(0, ui.KeyBackspace)
	// Start Chinese IME composition ("nihao"): Enter while composing must NOT submit rename.
	tt.Compose("ni'hao", 6)
	tt.Frame()
	if !v.composing {
		t.Fatal("composing flag not observed during IME preedit")
	}
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if len(emitted) != 0 {
		t.Fatalf("Enter during IME composition prematurely emitted %+v", emitted)
	}
	// Commit Chinese text ("星轨") and clear composition.
	tt.Compose("", 0)
	tt.Type("星轨")
	tt.Frame()
	if v.State.Form.RenameDraft != "星轨" {
		t.Fatalf("IME committed text=%q want 星轨", v.State.Form.RenameDraft)
	}
	// Ctrl+A, Ctrl+C, Ctrl+V clipboard round trip.
	tt.Key(ui.Ctrl, ui.KeyA)
	tt.Key(ui.Ctrl, ui.KeyC)
	if got := tt.Clipboard(); got != "星轨" {
		t.Fatalf("clipboard=%q want 星轨", got)
	}
	tt.SetClipboard("夜航星")
	tt.Key(ui.Ctrl, ui.KeyV)
	tt.Frame()
	if v.State.Form.RenameDraft != "夜航星" {
		t.Fatalf("pasted text=%q want 夜航星", v.State.Form.RenameDraft)
	}
	// Enter now submits rename_account.
	tt.Key(0, ui.KeyEnter)
	tt.Frame()
	if len(emitted) != 1 || emitted[0].Kind != "rename_account" || emitted[0].Label != "夜航星" {
		t.Fatalf("rename after IME commit mismatch: %+v", emitted)
	}

	// Verify 100%, 125%, 150%, 200% DPI rendering and hit-testing on phone overlay.
	for _, scale := range []float32{1.0, 1.25, 1.5, 2.0} {
		ov := NewViewShell(nil)
		ov.OverlayMode = true
		ov.ApplyModel(fixturePhone())
		t2 := ui.NewTester(ov.View, 1280, 720)
		t2.SetScale(scale)
		t2.Frame()
		t2.Frame()
		img := t2.Image()
		if img.Bounds().Dx() != int(math.Ceil(1280*float64(scale))) || img.Bounds().Dy() != int(math.Ceil(720*float64(scale))) {
			t.Fatalf("scale %.2f unexpected raster size %v", scale, img.Bounds())
		}
		if _, ok := t2.AccessByRole(ui.RoleTextField, "请输入手机号"); !ok {
			t.Fatalf("scale %.2f missing phone input", scale)
		}
		// Tab navigation moves focus from phone to captcha.
		if err := t2.Click("请输入手机号"); err != nil {
			t.Fatalf("scale %.2f click phone: %v", scale, err)
		}
		t2.Key(0, ui.KeyTab)
		t2.Frame()
		if !t2.Focused("请输入验证码") {
			t.Fatalf("scale %.2f Tab did not move focus to captcha", scale)
		}
		t2.Key(ui.Shift, ui.KeyTab)
		t2.Frame()
		if !t2.Focused("请输入手机号") {
			t.Fatalf("scale %.2f Shift+Tab did not return focus to phone", scale)
		}
	}
}
