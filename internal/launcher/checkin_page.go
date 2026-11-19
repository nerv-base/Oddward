// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// checkInPageData is a UI-thread projection of the check-in state. It carries
// no credential and no callback result: every action only dispatches an event.
type checkInPageData struct {
	AccountID   string
	RoleID      string
	Linked      bool
	NeedsRelink bool
	Loading     bool
	Loaded      bool
	LoadFailure state.CheckInFailure

	Auto    [state.CheckInKinds]bool
	Busy    [state.CheckInKinds]bool
	Phase   [state.CheckInKinds]state.CheckInPhase
	Result  [state.CheckInKinds]state.CheckInResult
	Detail  [state.CheckInKinds]string
	Failure [state.CheckInKinds]state.CheckInFailure
	At      [state.CheckInKinds]time.Time
	// Confirm arms the one manual retry of a result whose answer never came.
	Confirm [state.CheckInKinds]bool

	StatusPhase   state.CheckInStatusPhase
	StatusFailure state.CheckInFailure
	Role          state.CheckInRole

	Now       time.Time
	OnSign    func(state.CheckInKind)
	OnConfirm func(state.CheckInKind, bool)
	OnRefresh func()
	OnStatus  func()
	OnRelink  func()
}

func checkInKindTitle(k state.CheckInKind) (title, caption string) {
	if k == state.CheckCommunity {
		return "社区签到", "塔吉多社区每日签到"
	}
	return "游戏签到", "《异环》每日游戏签到"
}

// checkInQuietButton keeps the shared quiet control but drops its glass fill in
// high contrast and reduced transparency, where a translucent hover layer would
// sit on top of the key art.
func (s *Shell) checkInQuietButton(c *ui.Context, key, label string, disabled bool) ui.Element {
	b := s.quietButton(c, key, label, disabled)
	if prefs := c.Preferences(); prefs.HighContrast || prefs.ReduceTransparency {
		b.Background(s.theme.Surface).Border(checkinHairlineWidth, muted)
	}
	return b
}

func (s *Shell) checkInPage(c *ui.Context, data checkInPageData) {
	tone := s.tone()
	prefs := c.Preferences()
	opaque := prefs.HighContrast || prefs.ReduceTransparency
	surface := canvas(c, "feature-surface")
	// One frosted sheet over the key art, matching the wish page: the page must
	// read the same whether the current poster is bright or dark.
	surface.Material(frostedSurface{Tint: ui.RGBA(10, 11, 14, .55)})
	surface.Children(func() {
		if !opaque {
			ui.Box(c).Absolute().Right(wishBloomRight).Bottom(wishBloomBottom).Size(wishBloomSize, wishBloomSize).PassThrough().RadialGradient(ui.RadialGradient{Radius: wishBloomSize / 2, Stops: []ui.GradientStop{
				{Color: tone.Alpha(wishHighlightOpacity), At: 0}, {Color: ui.Transparent, At: 1},
			}})
		}
		ui.Column(c.Key("checkin-header")).Absolute().Left(checkinInset).Right(checkinInset).Top(checkinHeaderTop).Gap(checkinSpaceMD).Children(func() {
			ui.Text(c, "每日签到").FontSize(checkinTextTitle).FontWeight(700)
			s.checkInSourceLine(c, data)
		})
		ui.Scroll(c.Key("checkin-body")).Absolute().Left(checkinInset).Right(checkinInset).Top(checkinBodyTop).Bottom(checkinBottomInset).Scrollbars(true).Children(func() {
			ui.Column(c.Key("checkin-body-column")).FillWidth().Gap(checkinBodyGap).Children(func() {
				if !data.Loaded {
					s.checkInLoadingCard(c, data)
					return
				}
				s.checkInAccountCard(c, data)
				if !data.Linked {
					return
				}
				ui.Row(c.Key("checkin-tasks")).FillWidth().Gap(checkinCardGap).AlignItems(ui.Stretch).Children(func() {
					for k := 0; k < state.CheckInKinds; k++ {
						s.checkInTaskCard(c, data, state.CheckInKind(k))
					}
				})
				s.checkInStatusCard(c, data)
			})
		})
	})
}

// checkInCard is the shared glass card: one hairline, one radius, one fill.
func checkInCard(c *ui.Context, key string) ui.Element {
	return ui.Column(c.Key(key)).FillWidth().Radius(checkinCardRadius).Clip().
		Border(checkinHairlineWidth, hairline).
		Material(frostedSurface{Tint: ui.RGBA(12, 14, 18, checkinStatusOpacity)}).
		Padding(checkinCardPadding).Gap(checkinSpaceSM)
}

// checkInSourceLine is the one line under the title: who is signed in, which
// day the answer belongs to, and the last read failure if there was one.
func (s *Shell) checkInSourceLine(c *ui.Context, data checkInPageData) {
	tone := s.tone()
	now := data.Now
	if now.IsZero() {
		now = s.now
	}
	line := "尚未关联塔吉多"
	switch {
	case data.Linked:
		line = ""
	case data.AccountID != "":
		line = "未关联塔吉多"
	}
	if data.Loaded {
		if line != "" {
			line += " · "
		}
		line += "今日 " + now.In(time.Local).Format("2006-01-02")
	}
	ui.Row(c.Key("checkin-source")).Gap(checkinSpaceSM).AlignItems(ui.Center).Children(func() {
		dot := muted
		if data.Linked {
			dot = tone
		}
		ui.Box(c).Size(checkinSourceDotSize, checkinSourceDotSize).Radius(checkinSourceDotSize / 2).Background(dot)
		ui.Text(c, line).FontSize(checkinTextBody).TextColor(muted).SingleLine().Shrink(1).Tooltip(line)
		if data.LoadFailure != state.CheckInFailureNone {
			note := "上次读取失败：" + checkInFailureText(data.LoadFailure)
			ui.Text(c, "· "+note).FontSize(checkinTextCaption).TextColor(muted).SingleLine().Shrink(1).Tooltip(note)
		}
	})
}

func (s *Shell) checkInLoadingCard(c *ui.Context, data checkInPageData) {
	card := checkInCard(c, "checkin-loading")
	card.Children(func() {
		title, body := "正在读取塔吉多状态", "读取完成后会显示账号、今日签到结果和游戏信息。"
		if data.LoadFailure != state.CheckInFailureNone {
			title, body = "无法读取签到状态", checkInFailureText(data.LoadFailure)
		}
		ui.Text(c, title).FontSize(checkinTextHelp).FontWeight(600)
		ui.Text(c, body).FontSize(checkinTextBody).TextColor(muted).LineHeight(wishBodyLineHeight)
		if data.LoadFailure != state.CheckInFailureNone {
			s.quietButton(c, "checkin-retry", "重试读取", false).AlignSelf(ui.Start).OnClick(func() { wishInvoke(data.OnRefresh) })
		}
	})
}

func (s *Shell) checkInAccountCard(c *ui.Context, data checkInPageData) {
	tone := s.tone()
	card := checkInCard(c, "checkin-account")
	card.Label("游戏账号与塔吉多关联")
	card.Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(checkinSpaceSM).Children(func() {
			dot := muted
			label := "未关联塔吉多"
			switch {
			case data.Linked && data.NeedsRelink:
				label = "塔吉多关联已失效"
			case data.Linked:
				dot = tone
				label = "已关联塔吉多"
			}
			ui.Box(c).Size(checkinSourceDotSize, checkinSourceDotSize).Radius(checkinSourceDotSize / 2).Background(dot)
			ui.Text(c, label).FontSize(checkinTextBody).TextColor(paper)
			ui.Box(c).Grow(1)
			if data.Linked {
				s.quietButton(c, "checkin-refresh", "刷新状态", data.Loading).OnClick(func() { wishInvoke(data.OnRefresh) })
			}
		})
		switch {
		case data.Linked:
			role := data.RoleID
			if data.Role.Name != "" {
				role = data.Role.Name + " · " + data.RoleID
			}
			ui.Text(c, "角色 "+role).FontSize(checkinTextHelp).FontWeight(600).SingleLine().Shrink(1)
			if data.NeedsRelink {
				ui.Text(c, "这条关联已失效，本地记录仍然保留；重新关联后才能签到。").
					FontSize(checkinTextBody).TextColor(muted).LineHeight(wishBodyLineHeight)
			}
		case data.AccountID == "":
			ui.Text(c, "还没有已保存的游戏账号").FontSize(checkinTextHelp).FontWeight(600)
			ui.Text(c, "启动游戏并在登录窗口勾选「在此设备保存账号」，回来就能关联塔吉多。").
				FontSize(checkinTextBody).TextColor(muted).LineHeight(wishBodyLineHeight)
			if s.Model.NormalMode {
				s.accentButton(c, "checkin-start-game", "启动游戏并保存账号").AlignSelf(ui.Start).OnClick(func() {
					s.Dispatch(state.Event{Kind: state.Navigate, Page: state.Home})
					s.Dispatch(state.Event{Kind: state.Primary})
				})
			}
		default:
			ui.Text(c, "关联塔吉多后即可签到").FontSize(checkinTextHelp).FontWeight(600)
			ui.Text(c, "关联会复用已保存的游戏账号，走塔吉多官方短信登录，不需要再登录游戏；手机号未注册过时会自动注册。").FontSize(checkinTextBody).TextColor(muted).LineHeight(wishBodyLineHeight)
			s.accentButton(c, "checkin-relink", "去关联塔吉多").AlignSelf(ui.Start).OnClick(func() { wishInvoke(data.OnRelink) })
		}
		if data.Linked && data.NeedsRelink {
			s.quietButton(c, "checkin-relink", "重新关联", data.Loading).AlignSelf(ui.Start).OnClick(func() { wishInvoke(data.OnRelink) })
		}
	})
}

// checkInPhaseText is the chip on a task card: short, never a guess.
func (s *Shell) checkInPhaseText(data checkInPageData, k state.CheckInKind) (string, ui.Color) {
	warn := s.wishWarningColor()
	switch data.Phase[k] {
	case state.CheckInSigning:
		return "提交中", paper
	case state.CheckInDone:
		return "已完成", paper
	case state.CheckInFailed:
		return "失败", warn
	case state.CheckInUnknown:
		return "待核实", warn
	case state.CheckInUnlinked:
		return "未关联", muted
	}
	if data.NeedsRelink {
		return "需重新关联", warn
	}
	return "未签到", muted
}

func (s *Shell) checkInChip(c *ui.Context, text string, color ui.Color) {
	ui.Row(c).Height(checkinChipHeight).Padding(0, checkinChipPadding).Radius(checkinChipRadius).
		Border(checkinHairlineWidth, color.Alpha(.45)).AlignItems(ui.Center).
		Children(func() { ui.Text(c, text).FontSize(checkinTextCaption).TextColor(color).SingleLine() })
}

func (s *Shell) checkInDetailLine(data checkInPageData, k state.CheckInKind) (string, ui.Color, bool) {
	warn := s.wishWarningColor()
	switch data.Phase[k] {
	case state.CheckInSigning:
		return "正在提交签到，请稍候…", paper, false
	case state.CheckInDone:
		detail := "今日签到已完成"
		if data.Detail[k] != "" {
			detail += " · " + data.Detail[k]
		}
		if !data.At[k].IsZero() {
			detail += " · " + data.At[k].In(time.Local).Format("15:04")
		}
		return detail, muted, false
	case state.CheckInFailed:
		// The backend detail is a complete sentence; the failure text is the
		// fallback for a record that only kept the kind.
		detail := data.Detail[k]
		if detail == "" {
			detail = checkInFailureText(data.Failure[k])
		}
		if detail == "" {
			detail = "请稍后再试"
		}
		return detail, warn, true
	case state.CheckInUnknown:
		if data.Confirm[k] {
			return "上次结果未确认，再次提交可能重复记入；服务器确认前请先刷新状态。", warn, true
		}
		return "结果待核实：请求可能已经送达服务器。请稍后刷新状态确认，Oddward 不会自动重试。", warn, true
	case state.CheckInUnlinked:
		return "关联塔吉多后才能签到。", muted, false
	}
	if data.Detail[k] != "" {
		// A ledger answer from earlier today, before the sign button was used.
		return "本地记录：" + data.Detail[k], muted, false
	}
	return "今日尚未签到。", muted, false
}

func (s *Shell) checkInActionLabel(data checkInPageData, k state.CheckInKind) (string, bool) {
	switch {
	case !data.Linked || data.NeedsRelink:
		return "签到", true
	case data.Busy[k]:
		return "签到中…", true
	case data.Phase[k] == state.CheckInSigning:
		return "签到中…", true
	case data.Phase[k] == state.CheckInDone:
		return "今日已签到", true
	case data.Phase[k] == state.CheckInUnknown && data.Confirm[k]:
		return "确认提交", false
	case data.Phase[k] == state.CheckInUnknown:
		// The first click only opens the confirmation; nothing is sent yet.
		return "重新提交", false
	}
	return "签到", false
}

func (s *Shell) checkInTaskCard(c *ui.Context, data checkInPageData, k state.CheckInKind) {
	title, caption := checkInKindTitle(k)
	key := "checkin-game"
	if k == state.CheckCommunity {
		key = "checkin-community"
	}
	card := checkInCard(c, key)
	card.Label(title).Height(checkinTaskHeight)
	card.Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
			ui.Column(c).Grow(1).Shrink(1).Gap(checkinSpaceXXS).Children(func() {
				ui.Text(c, title).FontSize(checkinTextHelp).FontWeight(600)
				ui.Text(c, caption).FontSize(checkinTextCaption).TextColor(muted)
			})
			text, color := s.checkInPhaseText(data, k)
			s.checkInChip(c, text, color)
		})
		detail, color, _ := s.checkInDetailLine(data, k)
		ui.Text(c, detail).FontSize(checkinTextBody).TextColor(color).LineHeight(wishBodyLineHeight)
		ui.Box(c).Grow(1)
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(checkinSpaceSM).Children(func() {
			label, disabled := s.checkInActionLabel(data, k)
			button := s.checkInQuietButton(c, key+"-sign", label, disabled)
			switch {
			case data.Phase[k] == state.CheckInUnknown && data.Confirm[k]:
				// The retry is offered only after an explicit confirmation, and
				// the user can still call it off without sending anything.
				button.OnClick(func() { checkInInvokeSign(data.OnSign, k) })
				cancel := s.checkInQuietButton(c, key+"-cancel", "取消", false)
				cancel.OnClick(func() { checkInInvokeConfirm(data.OnConfirm, k, false) })
			case data.Phase[k] == state.CheckInUnknown:
				button.OnClick(func() { checkInInvokeConfirm(data.OnConfirm, k, true) })
			default:
				button.OnClick(func() { checkInInvokeSign(data.OnSign, k) })
			}
			ui.Box(c).Grow(1)
			// The caption is the switch's accessible name, so it names the task
			// instead of a bare "自动".
			caption := ui.Text(c, "启动游戏时自动").FontSize(checkinTextBody).TextColor(muted).Label(title + " · 启动游戏时自动")
			s.preferenceSwitchFor(c, "checkin-auto-"+kindKey(k), caption, data.Auto[k], !data.Linked || data.NeedsRelink, func(on bool) state.Event {
				return state.Event{Kind: state.CheckInAutoToggle, Check: state.CheckInPayload{Kind: k, Enabled: on}}
			})
		})
	})
}

func checkInInvokeSign(fn func(state.CheckInKind), k state.CheckInKind) {
	if fn != nil {
		fn(k)
	}
}

func checkInInvokeConfirm(fn func(state.CheckInKind, bool), k state.CheckInKind, armed bool) {
	if fn != nil {
		fn(k, armed)
	}
}

func kindKey(k state.CheckInKind) string {
	if k == state.CheckCommunity {
		return "community"
	}
	return "game"
}

func (s *Shell) checkInStatusCard(c *ui.Context, data checkInPageData) {
	tone := s.tone()
	card := checkInCard(c, "checkin-status").Gap(checkinStatusGap)
	card.Label("游戏信息")
	card.Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(checkinSpaceSM).Children(func() {
			ui.Text(c, "游戏信息").FontSize(checkinTextHelp).FontWeight(600)
			ui.Box(c).Grow(1)
			ui.Text(c, s.checkInFetchedLine(data)).FontSize(checkinTextCaption).TextColor(muted).SingleLine()
			s.quietButton(c, "checkin-status-refresh", "刷新", data.StatusPhase == state.CheckStatusLoading || !data.Linked).OnClick(func() { wishInvoke(data.OnStatus) })
		})
		haveRole := data.Role.RoleID != "" || data.Role.Name != ""
		switch data.StatusPhase {
		case state.CheckStatusFailed:
			reason := checkInFailureText(data.StatusFailure)
			if reason == "" {
				reason = "请稍后再试"
			}
			line := "读取游戏信息失败 · " + reason
			if haveRole {
				line = "刷新失败 · " + reason + "；下面仍是上次获取的数值。"
			}
			ui.Text(c, line).FontSize(checkinTextBody).TextColor(s.wishWarningColor()).LineHeight(wishBodyLineHeight)
			if !haveRole {
				return
			}
		case state.CheckStatusReady:
			if !haveRole {
				return
			}
		default:
			ui.Text(c, "尚未读取游戏信息。点「刷新」从塔吉多获取当前数值。").FontSize(checkinTextBody).TextColor(muted)
			return
		}
		role := data.Role
		width, _ := c.Size()
		statWidth := (width - railWidth - bezelWidth - 2*checkinInset - 2*checkinCardPadding - 3*checkinStatGap) / 4
		if statWidth < 80 {
			statWidth = 80
		}
		ui.Row(c).FillWidth().Gap(checkinStatGap).AlignItems(ui.Start).Children(func() {
			s.checkInStat(c, "本性像素", role.Stamina, role.StaminaMax, statWidth, tone)
			s.checkInStat(c, "都市活力", role.CityStamina, role.CityStaminaMax, statWidth, tone)
			s.checkInMeterStat(c, "今日活跃度", role.DayValue, 100, "", statWidth, tone)
			s.checkInMeterStat(c, "周本剩余", role.WeekRemain, 3, "次", statWidth, tone)
		})
		ui.Box(c).FillWidth().Height(checkinHairlineWidth).Background(hairline)
		secondary := fmt.Sprintf("角色等级 %d · 世界等级 %d · 登录 %d 天 · 角色 %d", role.Level, role.WorldLevel, role.LoginDays, role.CharCount)
		if role.Server != "" {
			secondary += " · " + role.Server
		}
		if role.AchievementTotal > 0 {
			secondary += fmt.Sprintf(" · 成就 %d/%d", role.Achievement, role.AchievementTotal)
		}
		ui.Text(c, secondary).FontSize(checkinTextBody).TextColor(muted).SingleLine().Shrink(1).Tooltip(secondary)
		ui.Text(c, "数值来自塔吉多；未提供的体力恢复、溢出与派遣数据不做推算。").FontSize(checkinTextCaption).TextColor(muted).LineHeight(wishBodyLineHeight)
	})
}

func (s *Shell) checkInFetchedLine(data checkInPageData) string {
	if data.Role.FetchedAt.IsZero() {
		return "尚未获取"
	}
	stamp := data.Role.FetchedAt.In(time.Local).Format("01-02 15:04")
	switch data.StatusPhase {
	case state.CheckStatusReady:
		return "获取于 " + stamp
	case state.CheckStatusFailed:
		if data.Role.RoleID == "" && data.Role.Name == "" {
			return "尚未获取"
		}
		return "显示上次获取 " + stamp
	}
	return "尚未获取"
}

// checkInStat draws one value with a proportion meter. The number is always
// the server's; when the current value exceeds the cap the meter is clamped,
// while the text keeps showing what the server reported.
func (s *Shell) checkInStat(c *ui.Context, label string, value, max int, width float32, tone ui.Color) {
	text := "—"
	ratio := float64(0)
	if max > 0 {
		text = fmt.Sprintf("%d / %d", value, max)
		ratio = float64(value) / float64(max)
		if ratio > 1 {
			ratio = 1
		}
		if ratio < 0 {
			ratio = 0
		}
	}
	ui.Column(c).Width(width).Gap(checkinSpaceXXS).Children(func() {
		ui.Text(c, label).FontSize(checkinTextCaption).TextColor(muted).SingleLine()
		ui.Text(c, text).Font(monoFont).FontSize(checkinTextValue).TextColor(paper).SingleLine()
		ui.Box(c).FillWidth().Height(checkinMeterHeight).Radius(checkinMeterRadius).Background(hairline).Children(func() {
			ui.Box(c).Width(float32(ratio) * width).Height(checkinMeterHeight).Radius(checkinMeterRadius).Background(tone)
		})
	})
}

// checkInMeterStat is checkInStat for a figure with a fixed ceiling that is
// not an in-game resource (activity out of 100, weekly clears out of 3).
func (s *Shell) checkInMeterStat(c *ui.Context, label string, value, max int, suffix string, width float32, tone ui.Color) {
	text := fmt.Sprintf("%d / %d%s", value, max, suffix)
	ratio := float64(value) / float64(max)
	if ratio > 1 {
		ratio = 1
	}
	if ratio < 0 {
		ratio = 0
	}
	ui.Column(c).Width(width).Gap(checkinSpaceXXS).Children(func() {
		ui.Text(c, label).FontSize(checkinTextCaption).TextColor(muted).SingleLine()
		ui.Text(c, text).Font(monoFont).FontSize(checkinTextValue).TextColor(paper).SingleLine()
		ui.Box(c).FillWidth().Height(checkinMeterHeight).Radius(checkinMeterRadius).Background(hairline).Children(func() {
			ui.Box(c).Width(float32(ratio) * width).Height(checkinMeterHeight).Radius(checkinMeterRadius).Background(tone)
		})
	})
}
