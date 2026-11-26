// SPDX-License-Identifier: GPL-3.0-or-later

package native

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/cnlogin/loginui"
	"github.com/nerv-base/Oddward/internal/cnlogin/loginui/state"
)

// ViewShell owns the pure MyGo UI presentation of the login window and welcome
// banner. It can be rendered headlessly with ui.NewTester or inside a real
// mygo.Window; all business logic stays in internal/cnlogin/session.
type ViewShell struct {
	State       state.State
	OverlayMode bool

	// BannerAlpha scales the welcome pill opacity in [0, 1]. Zero is treated
	// as fully hidden only when BannerAlphaSet is true, so tests that omit it
	// render a visible banner by default.
	BannerAlpha    float32
	BannerAlphaSet bool

	// Optional overrides for deterministic accessibility & preference testing.
	// Nil reads the live OS preferences from ui.Context.Preferences().
	HighContrastOverride *bool
	TransparencyOverride *bool

	// OnEffect is invoked synchronously when user interaction emits an effect.
	OnEffect func(state.Effect)

	// OnInvalidate requests a redraw when local state changes outside a build.
	OnInvalidate func()

	formScroll     ui.ScrollState
	accountsScroll ui.ScrollState
	statusScroll   ui.ScrollState
	composing      bool
	lastScrollGen  uint64
}

// NewViewShell returns an initialized login UI presentation shell.
func NewViewShell(onEffect func(state.Effect)) *ViewShell {
	return &ViewShell{OnEffect: onEffect}
}

// Dispatch advances the pure state machine and forwards any emitted effects.
func (v *ViewShell) Dispatch(ev state.Event) {
	next, effects := state.Reduce(v.State, ev)
	v.State = next
	if v.State.FormGen != v.lastScrollGen {
		v.formScroll.Y = v.State.Form.FormScroll
		v.accountsScroll.Y = v.State.Form.AccountsScroll
		v.statusScroll.Y = 0
		v.lastScrollGen = v.State.FormGen
	}
	if v.OnInvalidate != nil {
		v.OnInvalidate()
	}
	if v.OnEffect != nil {
		for _, fx := range effects {
			v.OnEffect(fx)
		}
	}
}

// ApplyModel applies a backend loginui.Model snapshot.
func (v *ViewShell) ApplyModel(m loginui.Model) {
	v.Dispatch(state.Event{Kind: state.EvSnapshot, Model: m})
}

// CaptureForm returns the in-memory unsubmitted form state, including current
// scroll offsets, for controlled window recreation.
func (v *ViewShell) CaptureForm() state.FormState {
	f := v.State.CaptureForm()
	f.FormScroll = v.formScroll.Y
	f.AccountsScroll = v.accountsScroll.Y
	return f
}

// RestoreForm restores form text, selections, focus and scroll positions into
// a freshly created view for the same screen.
func (v *ViewShell) RestoreForm(captured state.FormState) {
	v.State = v.State.RestoreForm(captured)
	if v.State.HasModel && captured.Screen == v.State.Model.Screen {
		v.formScroll.Y = captured.FormScroll
		v.accountsScroll.Y = captured.AccountsScroll
	}
}

func (v *ViewShell) preferences(c *ui.Context) (highContrast, transparencyEnabled bool) {
	prefs := c.Preferences()
	highContrast = prefs.HighContrast
	transparencyEnabled = !prefs.ReduceTransparency
	if v.HighContrastOverride != nil {
		highContrast = *v.HighContrastOverride
	}
	if v.TransparencyOverride != nil {
		transparencyEnabled = *v.TransparencyOverride
	}
	return highContrast, transparencyEnabled
}

func (v *ViewShell) loginTheme(highContrast bool) *ui.Theme {
	t := *ui.LightTheme()
	t.Background = colTransparent
	t.Surface = colInputBg
	t.SurfaceHover = colInputBg
	t.SurfacePressed = colPrimaryPress
	t.Border = colInputBorder
	t.Text = colText
	t.TextMuted = colMutedText
	t.Accent = colPrimaryTop
	t.AccentHover = colWhite
	t.AccentPressed = colPrimaryPress
	t.AccentText = colPrimaryText
	t.Selection = colSelection
	t.Focus = colPrimaryText
	t.Font = loginFont
	t.FontSize = 14
	t.Radius = 12
	t.Scrollbar = colScrollbar
	if highContrast {
		t.Border = colBlack
		t.Text = colBlack
		t.TextMuted = colBlack
		t.Focus = colBlack
	}
	return &t
}

// View builds one MyGo frame for either the welcome banner or the login card.
func (v *ViewShell) View(c *ui.Context) {
	highContrast, transparencyEnabled := v.preferences(c)
	c.SetTheme(v.loginTheme(highContrast))

	if !v.State.HasModel {
		c.SetZoom(1)
		ui.Box(c).Fill().Background(colTransparent)
		return
	}

	if v.State.Model.Screen == "welcome" {
		c.SetZoom(1)
		v.bannerView(c, highContrast, transparencyEnabled)
		return
	}

	if v.OverlayMode {
		cw, ch := c.Size()
		zoom := c.Zoom()
		winW, winH := cw*zoom, ch*zoom
		c.SetZoom(OverlayCardScale(winW, winH))
	} else {
		c.SetZoom(1)
	}

	v.composing = false
	c.OnWindowShortcut(0, ui.KeyEscape, func() {
		if !v.composing {
			v.Dispatch(state.Event{Kind: state.EvPressEscape})
		}
	})
	if v.State.Model.Screen == "notice" || (v.State.Model.Screen == "accounts" && v.State.Form.EditMode != state.EditRename) {
		c.OnWindowShortcut(0, ui.KeyEnter, func() {
			if !v.composing {
				v.Dispatch(state.Event{Kind: state.EvPressEnter})
			}
		})
	}

	if v.OverlayMode {
		mask := OverlayMaskColor(highContrast, transparencyEnabled)
		ui.Column(c.Key("overlay-mask")).Fill().Justify(ui.Center).AlignItems(ui.Center).Background(mask).Children(func() {
			v.loginCard(c, highContrast)
		})
		return
	}
	ui.Column(c.Key("standalone-root")).Fill().Justify(ui.Center).AlignItems(ui.Center).Background(colTransparent).Children(func() {
		v.loginCard(c, highContrast)
	})
}

func (v *ViewShell) bannerView(c *ui.Context, highContrast, transparencyEnabled bool) {
	alpha := float32(1)
	if v.BannerAlphaSet {
		alpha = v.BannerAlpha
		if alpha < 0 {
			alpha = 0
		} else if alpha > 1 {
			alpha = 1
		}
	}
	bg, border, fg := BannerColors(highContrast, transparencyEnabled)
	cw, _ := c.Size()
	maxPillW := cw - bannerSideInset*2
	if maxPillW < 0 {
		maxPillW = 0
	}
	ui.Column(c.Key("banner-root")).Fill().Padding(bannerTopInset, bannerSideInset, 0, bannerSideInset).
		AlignItems(ui.Center).Background(colTransparent).Children(func() {
		pill := ui.Row(c.Key("banner_card")).
			Label("欢迎登录横幅").
			Padding(10, 22, 10, 18).
			Gap(9).
			Radius(22).
			MaxWidth(maxPillW).
			AlignItems(ui.Center).
			Background(bg).
			Border(1, border).
			Shadow(0, 3, 18, 0, ui.RGBA(0, 0, 0, 0.35)).
			Opacity(alpha)
		pill.Children(func() {
			ui.Icon(c.Key("banner_mark"), svgBrandMark).
				Label("Oddward").
				Size(20, 20).
				TextColor(fg).
				Shrink(0)
			textMax := maxPillW - 18 - 22 - 20 - 9 - 2
			if textMax < 0 {
				textMax = 0
			}
			ui.Text(c.Key("banner"), v.State.Model.Banner).
				Label(v.State.Model.Banner).
				Font(loginFont).
				FontSize(15).
				FontWeight(600).
				TextColor(fg).
				SingleLine().
				Ellipsis("…").
				MaxWidth(textMax)
		})
	})
}

func (v *ViewShell) loginCard(c *ui.Context, highContrast bool) {
	card := ui.Box(c.Key("login_card")).
		Role(ui.RoleDialog).
		Label("社区/非官方登录窗口").
		Size(cardWidth, cardHeight).
		Radius(cardRadius).
		Padding(bezelWidth).
		Gradient(colBezelTop, colBezelBottom, 90).
		Border(1, colBezelBorder).
		Shadow(0, 6, 28, 0, ui.RGBA(0, 0, 0, 0.42))
	if highContrast {
		card.Background(colBlack).Border(2, colWhite)
	}
	card.Children(func() {
		bodyBg := colBody
		if highContrast {
			bodyBg = colWhite
		}
		ui.Column(c.Key("login_frame")).Fill().Radius(frameRadius).Clip().Background(bodyBg).Children(func() {
			v.headerBar(c)
			ui.Box(c.Key("header_seam")).FillWidth().Height(2).Background(colSeam)
			v.cardBody(c, highContrast)
			v.cardFooter(c, highContrast)
		})
	})
}

func (v *ViewShell) headerBar(c *ui.Context) {
	canAct := v.State.CanAct()
	ui.Row(c.Key("header_bar")).FillWidth().Height(headerHeight).Background(colHeader).AlignItems(ui.Center).Children(func() {
		// Left leading actions (Back + Help).
		ui.Row(c.Key("header_leading")).Width(headerSideWidth).FillHeight().Padding(0, 0, 0, 10).AlignItems(ui.Center).Children(func() {
			if v.State.ShowBack() {
				b := v.ghostIconButton(c, "back", "返回", svgBack, 22, !canAct)
				if b.Clicked() {
					v.Dispatch(state.Event{Kind: state.EvClickBack})
				}
			}
			if v.State.ShowHelp() {
				b := v.ghostIconButton(c, "help", "登录说明", svgHelp, 24, !canAct)
				if b.Clicked() {
					v.Dispatch(state.Event{Kind: state.EvClickHelp})
				}
			}
		})

		// Center brand lockup: Anomaly Eye + ODDWARD + 非官方.
		ui.Row(c.Key("header_brand")).Grow(1).FillHeight().Justify(ui.Center).AlignItems(ui.Center).
			Label("Oddward 社区启动器 · 非官方").Children(func() {
			ui.Icon(c.Key("brand_mark"), svgBrandMark).Size(24, 24).TextColor(colWhite).Shrink(0)
			ui.Box(c).Width(9)
			ui.Text(c.Key("brand_wordmark"), "ODDWARD").
				Font(wordmarkFont).FontSize(21).FontWeight(600).TextColor(colWhite).SingleLine()
			ui.Box(c.Key("brand_tag")).
				Margin(1, 0, 0, 9).Padding(1, 5, 2, 5).Radius(4).Border(1, colTagBorder).Children(func() {
				ui.Text(c, "非官方").Font(loginFont).FontSize(10).TextColor(colTagText).SingleLine()
			})
		})

		// Right trailing action (Close).
		ui.Row(c.Key("header_trailing")).Width(headerSideWidth).FillHeight().Padding(0, 10, 0, 0).
			Justify(ui.End).AlignItems(ui.Center).Children(func() {
			if v.State.ShowClose() {
				b := v.ghostIconButton(c, "close", "关闭", svgClose, 22, false)
				if b.Clicked() {
					v.Dispatch(state.Event{Kind: state.EvClickClose})
				}
			}
		})
	})
}

func (v *ViewShell) ghostIconButton(c *ui.Context, key, label string, icon *ui.SVG, iconSize float32, disabled bool) ui.Element {
	b := ui.ButtonBase(c.Key(key)).
		Label(label).
		Size(iconBtnSize, iconBtnSize).
		Radius(iconBtnSize/2).
		Justify(ui.Center).AlignItems(ui.Center).
		Border(1.5, colTransparent).
		Disabled(disabled).
		DimDisabled(false)
	switch {
	case disabled:
		b.Opacity(0.40)
	case b.Pressed():
		b.Background(colGhostPress)
	case b.Hovered():
		b.Background(colGhostHover)
	}
	if b.FocusVisible() {
		b.Border(1.5, colGhostFocus)
	}
	b.Children(func() {
		ui.Icon(c, icon).Size(iconSize, iconSize).TextColor(colWhite)
	})
	return b
}

func (v *ViewShell) cardBody(c *ui.Context, highContrast bool) {
	ui.Column(c.Key("card_body")).Grow(1).FillWidth().AlignItems(ui.Center).Children(func() {
		// Heading.
		ui.Column(c.Key("heading")).FillWidth().Padding(22, 32, 0, 32).AlignItems(ui.Center).Children(func() {
			textCol := colText
			mutedCol := colMutedText
			if highContrast {
				textCol = colBlack
				mutedCol = colBlack
			}
			ui.Text(c.Key("title"), v.State.Title()).
				Font(loginFont).FontSize(16).Bold().TextColor(textCol).TextAlign(ui.Center)
			if v.State.Model.Subtitle != "" {
				ui.Box(c).Height(4)
				ui.Text(c.Key("subtitle"), v.State.Model.Subtitle).
					Font(loginFont).FontSize(12).TextColor(mutedCol).TextAlign(ui.Center)
			}
		})

		// Status chip (only visible when Status has non-whitespace text).
		if strings.TrimSpace(v.State.Model.Status) != "" {
			ui.Box(c.Key("status_wrap")).FillWidth().Padding(12, 32, 0, 32).Children(func() {
				ui.Scroll(c.Key("status_scroll")).
					TrackScroll(&v.statusScroll).
					FillWidth().
					MaxHeight(statusMaxH).
					Children(func() {
						chip := ui.Box(c.Key("status_chip")).
							FillWidth().
							Radius(14).
							Background(colStatus).
							Padding(9, 14)
						if highContrast {
							chip.Background(colBlack).Border(1, colWhite)
						}
						chip.Children(func() {
							ui.Text(c.Key("status"), v.State.Model.Status).
								Label("登录状态").
								Font(loginFont).FontSize(12.5).TextColor(colWhite)
						})
					})
			})
		}

		// Scrollable form body (344px centered column).
		ui.Box(c.Key("form_wrap")).Grow(1).FillWidth().Padding(16, 24, 12, 24).Children(func() {
			ui.Scroll(c.Key("form_scroll")).
				Label("登录表单").
				TrackScroll(&v.formScroll).
				Fill().
				Children(func() {
					ui.Column(c.Key("form_inner")).FillWidth().AlignItems(ui.Center).Children(func() {
						ui.Column(c.Key("form_content")).Width(fieldWidth).Children(func() {
							switch v.State.Model.Screen {
							case "notice":
								v.noticeForm(c, highContrast)
							case "accounts":
								v.accountsForm(c, highContrast)
							default:
								v.phoneForm(c, highContrast)
							}
						})
					})
				})
		})
	})
}

func (v *ViewShell) noticeForm(c *ui.Context, highContrast bool) {
	textCol := colText
	if highContrast {
		textCol = colBlack
	}
	for i, sec := range v.State.Model.Notice {
		if sec.Title != "" {
			ui.Text(c.Key(fmt.Sprintf("notice_title_%d", i)), sec.Title).
				Font(loginFont).FontSize(14).Bold().TextColor(textCol).Margin(10, 0, 8, 0)
		}
		if sec.Body != "" {
			ui.Text(c.Key(fmt.Sprintf("notice_body_%d", i)), sec.Body).
				Font(loginFont).FontSize(12.5).TextColor(textCol).Margin(4, 0, 8, 0)
		}
	}
}

func (v *ViewShell) phoneForm(c *ui.Context, highContrast bool) {
	canAct := v.State.CanAct()
	textCol := colText
	borderCol := colInputBorder
	if highContrast {
		textCol = colBlack
		borderCol = colBlack
	}

	// Phone pill input with "+86" prefix.
	phoneVal := v.State.Form.Phone
	phoneShell := ui.Row(c.Key("phone_group")).
		Label("手机号").
		Width(fieldWidth).Height(fieldHeight).
		Radius(fieldHeight/2).
		Background(colInputBg).
		Border(1.5, borderCol).
		AlignItems(ui.Center).
		Margin(0, 0, 14, 0)
	if !canAct {
		phoneShell.Opacity(0.55)
	}
	if v.State.Form.Focused == state.FocusPhone {
		phoneShell.Border(1.5, colInputFocus)
	}
	phoneShell.Children(func() {
		ui.Box(c).Width(22)
		ui.Text(c, "+86").Font(loginFont).FontSize(15).Bold().TextColor(textCol).SingleLine().Shrink(0)
		ui.Box(c).Width(14)
		ui.Box(c).Width(2).Height(24).Radius(1).Background(borderCol).Shrink(0)
		ui.Box(c).Width(14)

		phoneInput := ui.TextInput(c.Key(fmt.Sprintf("phone-%d", v.State.FormGen)), &phoneVal).
			Label("请输入手机号").
			Placeholder("请输入手机号").
			PlaceholderColor(colMutedText).
			MaxLength(11).
			Keyboard(ui.KeyboardPhone).
			Font(loginFont).FontSize(15).FontWeight(600).
			TextColor(textCol).
			Background(colTransparent).
			Border(0, colTransparent).
			Padding(0, 0).
			Grow(1).
			Height(fieldHeight - 6).
			Disabled(!canAct).
			DimDisabled(false).
			OnSubmit(func() {
				if !v.composing {
					v.Dispatch(state.Event{Kind: state.EvPressEnter})
				}
			})
		if phoneVal != v.State.Form.Phone {
			v.Dispatch(state.Event{Kind: state.EvSetPhone, Text: phoneVal})
		}
		if v.State.RestoreFocus == state.FocusPhone {
			phoneInput.Focus()
			if v.State.RestoreSelection {
				phoneInput.SetTextSelection(v.State.Form.PhoneSel.Start, v.State.Form.PhoneSel.End)
			}
			v.Dispatch(state.Event{Kind: state.EvConsumeRestore})
		} else if v.State.RestoreSelection && v.State.RestoreFocus == state.FocusNone {
			phoneInput.SetTextSelection(v.State.Form.PhoneSel.Start, v.State.Form.PhoneSel.End)
		}
		if phoneInput.Composing() {
			v.composing = true
		}
		if phoneInput.Focused() {
			s, e := phoneInput.TextSelection()
			v.State.Form.Focused = state.FocusPhone
			v.State.Form.PhoneSel = state.Selection{Start: s, End: e}
		}
		ui.Box(c).Width(22)
	})

	// Captcha pill input with embedded SMS button.
	captchaVal := v.State.Form.Captcha
	captchaShell := ui.Row(c.Key("captcha_group")).
		Label("验证码").
		Width(fieldWidth).Height(fieldHeight).
		Radius(fieldHeight/2).
		Background(colInputBg).
		Border(1.5, borderCol).
		AlignItems(ui.Center).
		Margin(0, 0, 14, 0)
	if !canAct {
		captchaShell.Opacity(0.55)
	}
	if v.State.Form.Focused == state.FocusCaptcha {
		captchaShell.Border(1.5, colInputFocus)
	}
	captchaShell.Children(func() {
		ui.Box(c).Width(22)
		captchaInput := ui.TextInput(c.Key(fmt.Sprintf("captcha-%d", v.State.FormGen)), &captchaVal).
			Label("请输入验证码").
			Placeholder("请输入验证码").
			PlaceholderColor(colMutedText).
			MaxLength(8).
			Keyboard(ui.KeyboardNumber).
			Font(loginFont).FontSize(15).FontWeight(600).
			TextColor(textCol).
			Background(colTransparent).
			Border(0, colTransparent).
			Padding(0, 0).
			Grow(1).
			Height(fieldHeight - 6).
			Disabled(!canAct).
			DimDisabled(false).
			OnSubmit(func() {
				if !v.composing {
					v.Dispatch(state.Event{Kind: state.EvPressEnter})
				}
			})
		if captchaVal != v.State.Form.Captcha {
			v.Dispatch(state.Event{Kind: state.EvSetCaptcha, Text: captchaVal})
		}
		if v.State.RestoreFocus == state.FocusCaptcha {
			captchaInput.Focus()
			if v.State.RestoreSelection {
				captchaInput.SetTextSelection(v.State.Form.CaptchaSel.Start, v.State.Form.CaptchaSel.End)
			}
			v.Dispatch(state.Event{Kind: state.EvConsumeRestore})
		} else if v.State.RestoreSelection {
			captchaInput.SetTextSelection(v.State.Form.CaptchaSel.Start, v.State.Form.CaptchaSel.End)
		}
		if captchaInput.Composing() {
			v.composing = true
		}
		if captchaInput.Focused() {
			s, e := captchaInput.TextSelection()
			v.State.Form.Focused = state.FocusCaptcha
			v.State.Form.CaptchaSel = state.Selection{Start: s, End: e}
		}

		smsBtn := v.linkButton(c, "send_sms", v.State.SMSLabel(), 120, 36, !v.State.CanSendSMS())
		if smsBtn.Clicked() {
			v.Dispatch(state.Event{Kind: state.EvClickSendSMS})
		}
		ui.Box(c).Width(10)
	})
	if v.State.RestoreSelection && v.State.RestoreFocus == state.FocusNone {
		v.Dispatch(state.Event{Kind: state.EvConsumeRestore})
	}
}

func (v *ViewShell) accountsForm(c *ui.Context, highContrast bool) {
	if v.State.Form.EditMode != state.EditNone {
		v.accountEditForm(c, highContrast)
		return
	}
	canAct := v.State.CanAct()
	ui.Scroll(c.Key("accounts_scroll")).
		Label("保存的账号").
		TrackScroll(&v.accountsScroll).
		FillWidth().
		MaxHeight(accountsMaxH).
		Children(func() {
			ui.Column(c.Key("accounts_list")).FillWidth().Gap(8).Children(func() {
				for _, acc := range v.State.Model.Accounts {
					selected := acc.ID == v.State.Model.SelectedAccountID
					v.accountItem(c, acc, selected, !canAct, highContrast)
				}
			})
		})

	sel := v.State.SelectedAccount()
	toolsDisabled := !canAct || sel == nil
	ui.Row(c.Key("accounts_tools")).FillWidth().Justify(ui.Center).Gap(8).Margin(8, 0, 0, 0).Children(func() {
		renameBtn := v.linkButton(c, "rename", "重命名", 0, 32, toolsDisabled)
		if renameBtn.Clicked() {
			v.Dispatch(state.Event{Kind: state.EvBeginRename})
		}
		deleteBtn := v.linkButton(c, "delete", "删除账号", 0, 32, toolsDisabled)
		if deleteBtn.Clicked() {
			v.Dispatch(state.Event{Kind: state.EvBeginDelete})
		}
	})
}

func (v *ViewShell) accountItem(c *ui.Context, acc loginui.Account, selected, disabled, highContrast bool) {
	accessLabel := strings.TrimSpace(acc.Label + " " + acc.MaskedPhone)
	bg := colAccBg
	border := colAccBg
	fg := colWhite
	if selected {
		bg = colWhite
		border = colWhite
		fg = colAccSelText
	}
	if highContrast {
		if selected {
			bg, border, fg = colWhite, colBlack, colBlack
		} else {
			bg, border, fg = colBlack, colWhite, colWhite
		}
	}

	b := ui.ButtonBase(c.Key("account_"+acc.ID)).
		Label(accessLabel).
		FillWidth().
		MinHeight(70).
		Radius(18).
		Padding(11, 14).
		Border(2, border).
		Disabled(disabled).
		DimDisabled(false)
	if !selected && !disabled && b.Hovered() && !highContrast {
		bg = colAccHover
		border = colAccHover
		b.Border(2, border)
	}
	if b.FocusVisible() {
		b.Border(2, colPrimaryBorder)
	}
	if disabled {
		b.Opacity(0.55)
	}
	b.Background(bg)
	b.Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
			// Avatar circle.
			ui.Box(c).Size(44, 44).Radius(22).Background(colAccAvatarBg).
				Justify(ui.Center).AlignItems(ui.Center).Shrink(0).Children(func() {
				ui.Icon(c, svgUser).Size(20, 20).TextColor(colWhite)
			})
			// Vertical divider.
			ui.Box(c).Width(2).Height(38).Radius(1).Margin(0, 14).Background(fg.Alpha(0.30)).Shrink(0)
			// Name + MaskedPhone / Tajiduo badge.
			ui.Column(c).Grow(1).Children(func() {
				ui.Text(c, acc.Label).Font(loginFont).FontSize(16).Bold().TextColor(fg).SingleLine().Ellipsis("…")
				if acc.MaskedPhone != "" {
					ui.Text(c, acc.MaskedPhone).Font(loginFont).FontSize(12.5).FontWeight(600).
						TextColor(fg.Alpha(0.80)).Margin(3, 0, 0, 0).SingleLine().Ellipsis("…")
				}
			})
			ui.Text(c, "本机").Font(loginFont).FontSize(11).TextColor(fg.Alpha(0.70)).Margin(0, 0, 0, 8).SingleLine().Shrink(0)
		})
	})
	if b.Clicked() {
		v.Dispatch(state.Event{Kind: state.EvSelectAccount, AccountID: acc.ID})
	}
}

func (v *ViewShell) accountEditForm(c *ui.Context, highContrast bool) {
	acc := v.State.EditingAccount()
	if acc == nil {
		v.Dispatch(state.Event{Kind: state.EvCancelEdit})
		return
	}
	canAct := v.State.CanAct()
	deletion := v.State.Form.EditMode == state.EditDelete
	textCol := colText
	borderCol := colInputBorder
	if highContrast {
		textCol = colBlack
		borderCol = colBlack
	}

	heading := "修改账号名称"
	if deletion {
		heading = "删除本机保存的账号？"
	}
	ui.Text(c.Key("edit_heading"), heading).Font(loginFont).FontSize(14).Bold().TextColor(textCol).Margin(2, 0, 4, 0)

	summary := acc.Label
	if acc.MaskedPhone != "" {
		summary = acc.Label + " · " + acc.MaskedPhone
	}
	ui.Text(c.Key("edit_summary"), summary).Font(loginFont).FontSize(12.5).TextColor(textCol).Margin(0, 0, 10, 0)

	if !deletion {
		draft := v.State.Form.RenameDraft
		shell := ui.Row(c.Key("rename_group")).
			Label("账号名称").
			Width(fieldWidth).Height(fieldHeight).
			Radius(fieldHeight/2).
			Background(colInputBg).
			Border(1.5, borderCol).
			AlignItems(ui.Center).
			Margin(0, 0, 12, 0)
		if v.State.Form.Focused == state.FocusRename {
			shell.Border(1.5, colInputFocus)
		}
		shell.Children(func() {
			ui.Box(c).Width(22)
			labelIn := ui.TextInput(c.Key("account_label"), &draft).
				Label("账号名称").
				Placeholder("账号名称").
				PlaceholderColor(colMutedText).
				MaxLength(32).
				Font(loginFont).FontSize(15).FontWeight(600).
				TextColor(textCol).
				Background(colTransparent).
				Border(0, colTransparent).
				Padding(0, 0).
				Grow(1).
				Height(fieldHeight - 6).
				Disabled(!canAct).
				DimDisabled(false).
				OnSubmit(func() {
					if !v.composing {
						v.Dispatch(state.Event{Kind: state.EvConfirmRename})
					}
				})
			if draft != v.State.Form.RenameDraft {
				v.Dispatch(state.Event{Kind: state.EvSetRenameDraft, Text: draft})
			}
			if v.State.RestoreFocus == state.FocusRename {
				labelIn.Focus()
				if v.State.RestoreSelection {
					labelIn.SetTextSelection(v.State.Form.RenameSel.Start, v.State.Form.RenameSel.End)
				}
				v.Dispatch(state.Event{Kind: state.EvConsumeRestore})
			}
			if labelIn.Composing() {
				v.composing = true
			}
			if labelIn.Focused() {
				s, e := labelIn.TextSelection()
				v.State.Form.Focused = state.FocusRename
				v.State.Form.RenameSel = state.Selection{Start: s, End: e}
			}
			ui.Box(c).Width(22)
		})
	}

	confirmKey, confirmLabel := "confirm_rename", "保存名称"
	if deletion {
		confirmKey, confirmLabel = "confirm_delete", "确认删除"
	}
	ui.Row(c.Key("confirm_row")).FillWidth().Height(secondaryHeight).Children(func() {
		confirmBtn := v.secondaryButton(c, confirmKey, confirmLabel, !canAct, highContrast)
		if confirmBtn.Clicked() {
			if deletion {
				v.Dispatch(state.Event{Kind: state.EvConfirmDelete})
			} else {
				v.Dispatch(state.Event{Kind: state.EvConfirmRename})
			}
		}
	})
	ui.Box(c).Height(6)
	ui.Row(c).FillWidth().Justify(ui.Center).Children(func() {
		cancelBtn := v.linkButton(c, "cancel_edit", "取消", 0, 32, false)
		if cancelBtn.Clicked() {
			v.Dispatch(state.Event{Kind: state.EvCancelEdit})
		}
	})
}

func (v *ViewShell) cardFooter(c *ui.Context, highContrast bool) {
	canAct := v.State.CanAct()
	ui.Column(c.Key("footer")).
		Label("登录操作").
		FillWidth().
		Height(footerHeight+footerInset).
		Padding(0, 0, footerInset, 0).
		AlignItems(ui.Center).
		Children(func() {
			ui.Column(c.Key("footer_inner")).Width(fieldWidth).Height(footerHeight).Children(func() {
				// Row 1: Primary action pill (52px).
				prim := v.primaryButton(c, "primary", v.State.PrimaryLabel(), !v.State.CanPrimary(), highContrast)
				if prim.Clicked() {
					v.Dispatch(state.Event{Kind: state.EvClickPrimary})
				}

				ui.Box(c).Height(12)

				// Row 2: Secondary pills (44px), split 50/50 when both visible.
				showOther := v.State.ShowOtherAccount()
				showOfficial := v.State.ShowOfficial()
				ui.Row(c.Key("secondary_row")).FillWidth().Height(secondaryHeight).Gap(10).Children(func() {
					if showOther {
						otherBtn := v.secondaryButton(c, "other_account", "其他账号", !canAct, highContrast)
						if otherBtn.Clicked() {
							v.Dispatch(state.Event{Kind: state.EvClickOtherAccount})
						}
					}
					if showOfficial {
						offBtn := v.secondaryButton(c, "official", "使用官方启动器", !canAct, highContrast)
						if offBtn.Clicked() {
							v.Dispatch(state.Event{Kind: state.EvClickOfficial})
						}
					}
				})

				ui.Box(c).Height(10)

				// Row 3: Remember checkbox (26px, visible on phone screen only).
				ui.Row(c.Key("remember_row")).FillWidth().Height(rememberHeight).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
					if v.State.ShowRemember() {
						v.rememberCheckbox(c, !canAct, highContrast)
					}
				})
			})
		})
}

func (v *ViewShell) primaryButton(c *ui.Context, key, label string, disabled, highContrast bool) ui.Element {
	b := ui.ButtonBase(c.Key(key)).
		Label(label).
		FillWidth().
		Height(primaryHeight).
		Radius(primaryHeight / 2).
		Padding(3).
		Disabled(disabled).
		DimDisabled(false)

	ringBg, ringBorder := colPrimaryRingBg, colPrimaryRingBd
	if disabled {
		ringBg, ringBorder = colTransparent, colTransparent
	}
	if highContrast {
		ringBg, ringBorder = colWhite, colBlack
	}
	b.Background(ringBg).Border(1, ringBorder)

	b.Children(func() {
		inner := ui.Box(c).Fill().Radius((primaryHeight - 6) / 2).Justify(ui.Center).AlignItems(ui.Center)
		textCol := colPrimaryText
		switch {
		case disabled:
			inner.Background(colPrimaryDisBg).Border(1.2, colPrimaryDisBg)
			textCol = colPrimaryDisTxt
		case b.Pressed():
			inner.Background(colPrimaryPress).Border(1.2, colPrimaryBorder)
		case b.Hovered():
			inner.Background(colWhite).Border(1.2, colInputFocus)
		default:
			inner.Gradient(colPrimaryTop, colPrimaryBot, 90).Border(1.2, colPrimaryBorder)
		}
		if b.FocusVisible() {
			inner.Border(2, colPrimaryText)
		}
		if highContrast && !disabled {
			inner.Background(colBlack).Border(2, colWhite)
			textCol = colWhite
		}
		inner.Children(func() {
			ui.Text(c, label).Font(loginFont).FontSize(16).Bold().TextColor(textCol).SingleLine()
		})
	})
	return b
}

func (v *ViewShell) secondaryButton(c *ui.Context, key, label string, disabled, highContrast bool) ui.Element {
	b := ui.ButtonBase(c.Key(key)).
		Label(label).
		Grow(1).
		Height(secondaryHeight).
		Radius(secondaryHeight/2).
		Padding(4, 14).
		Justify(ui.Center).AlignItems(ui.Center).
		Border(2, colTransparent).
		Disabled(disabled).
		DimDisabled(false)
	bg := colSecondBg
	fg := colSecondText
	switch {
	case disabled:
		b.Opacity(0.55)
	case b.Pressed():
		bg = colSecondPress
		fg = colText
	case b.Hovered():
		bg = colSecondHover
		fg = colText
	}
	if b.FocusVisible() {
		b.Border(2, colPrimaryText)
	}
	if highContrast {
		bg, fg = colWhite, colBlack
		b.Border(2, colBlack)
	}
	b.Background(bg).Children(func() {
		ui.Text(c, label).Font(loginFont).FontSize(14).Bold().TextColor(fg).SingleLine()
	})
	return b
}

func (v *ViewShell) linkButton(c *ui.Context, key, label string, width, height float32, disabled bool) ui.Element {
	b := ui.ButtonBase(c.Key(key)).
		Label(label).
		Height(height).
		Radius(height/2).
		Padding(2, 8).
		Justify(ui.Center).AlignItems(ui.Center).
		Border(1.5, colTransparent).
		Disabled(disabled).
		DimDisabled(false)
	if width > 0 {
		b.Width(width).Shrink(0)
	}
	fg := colLinkText
	switch {
	case disabled:
		fg = colLinkDisText
	case b.Hovered():
		fg = colBlack
		b.Background(colLinkHoverBg)
	}
	if b.FocusVisible() {
		b.Border(1.5, colPrimaryText)
	}
	b.Children(func() {
		ui.Text(c, label).Font(loginFont).FontSize(13).Bold().TextColor(fg).SingleLine()
	})
	return b
}

func (v *ViewShell) rememberCheckbox(c *ui.Context, disabled, highContrast bool) {
	checked := v.State.Form.Remember
	cb := ui.CheckboxBase(c.Key("remember"), &checked).
		Label("在此设备保存账号").
		Height(rememberHeight).
		AlignItems(ui.Center).
		Gap(8).
		Disabled(disabled).
		DimDisabled(false).
		OnChange(func() {
			v.Dispatch(state.Event{Kind: state.EvSetRemember, Bool: checked})
		})
	if disabled {
		cb.Opacity(0.50)
	}
	ringBorder := colCheckRing
	ringWidth := float32(1.6)
	if !disabled && cb.Hovered() {
		ringBorder = colInputFocus
	}
	if cb.FocusVisible() {
		ringBorder = colPrimaryText
		ringWidth = 2.2
	}
	labelCol := colCheckLabel
	if highContrast {
		ringBorder = colBlack
		labelCol = colBlack
	}
	cb.Children(func() {
		dot := ui.Box(c).Size(18, 18).Radius(9).Justify(ui.Center).AlignItems(ui.Center)
		if checked {
			dot.Background(colText).Border(ringWidth, colText).Children(func() {
				ui.Icon(c, svgCheck).Size(12, 12).TextColor(colWhite)
			})
		} else {
			dot.Background(colWhite).Border(ringWidth, ringBorder)
		}
		ui.Text(c, "在此设备保存账号").Font(loginFont).FontSize(12).FontWeight(600).TextColor(labelCol).SingleLine()
	})
}
