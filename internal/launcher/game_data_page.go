// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// gameDataPageData is a UI-thread projection of the same role summary the
// check-in page reads. It adds no request of its own: opening this page uses
// the check-in status path, and every action only dispatches an event.
type gameDataPageData struct {
	AccountID   string
	RoleID      string
	Linked      bool
	NeedsRelink bool
	Loaded      bool
	LoadFailure state.CheckInFailure

	StatusPhase   state.CheckInStatusPhase
	StatusFailure state.CheckInFailure
	Role          state.CheckInRole
	CanLaunch     bool

	OnLoad      func()
	OnStatus    func()
	OnRelink    func()
	OnStartGame func()
}

func (s *Shell) gameDataPage(c *ui.Context, data gameDataPageData) {
	tone := s.tone()
	prefs := c.Preferences()
	opaque := prefs.HighContrast || prefs.ReduceTransparency
	surface := canvas(c, "gamedata-surface")
	surface.Material(frostedSurface{Tint: ui.RGBA(10, 11, 14, .55)})
	surface.Children(func() {
		if !opaque {
			ui.Box(c).Absolute().Right(wishBloomRight).Bottom(wishBloomBottom).Size(wishBloomSize, wishBloomSize).PassThrough().RadialGradient(ui.RadialGradient{Radius: wishBloomSize / 2, Stops: []ui.GradientStop{
				{Color: tone.Alpha(wishHighlightOpacity), At: 0}, {Color: ui.Transparent, At: 1},
			}})
		}
		ui.Column(c.Key("gamedata-header")).Absolute().Left(checkinInset).Right(checkinInset).Top(checkinHeaderTop).Gap(checkinSpaceMD).Children(func() {
			ui.Text(c, "游戏数据").FontSize(checkinTextTitle).FontWeight(700)
			s.gameDataSourceLine(c, data)
		})
		ui.Scroll(c.Key("gamedata-body")).Absolute().Left(checkinInset).Right(checkinInset).Top(checkinBodyTop).Bottom(checkinBottomInset).Scrollbars(true).Children(func() {
			ui.Column(c.Key("gamedata-body-column")).FillWidth().Gap(checkinBodyGap).Children(func() {
				if !data.Loaded {
					s.gameDataLoadCard(c, data)
					return
				}
				s.gameDataAccountCard(c, data)
				if !data.Linked || data.NeedsRelink {
					return
				}
				s.gameDataRoleCard(c, data, tone)
				if details := data.Role.Details; details != nil {
					s.gameDataCharactersCard(c, details.Characters, data.Role.CharCount, tone)
					s.gameDataAreasCard(c, details.Areas, tone)
					s.gameDataCollectionsCard(c, details.RealEstate, details.Vehicle, tone)
				}
			})
		})
	})
}

func (s *Shell) gameDataSourceLine(c *ui.Context, data gameDataPageData) {
	line := "未关联塔吉多"
	switch {
	case data.Linked && data.NeedsRelink:
		line = "塔吉多关联已失效"
	case data.Linked:
		line = "塔吉多"
		if data.Role.Name != "" {
			line += " · " + data.Role.Name
		} else if data.RoleID != "" {
			line += " · " + data.RoleID
		}
	case data.AccountID == "":
		line = "还没有已保存的游戏账号"
	}
	if data.Loaded && data.Linked && !data.NeedsRelink {
		fetched := s.checkInFetchedLine(checkInPageData{Role: data.Role, StatusPhase: data.StatusPhase})
		line += " · " + fetched
	}
	ui.Row(c.Key("gamedata-source")).Gap(checkinSpaceSM).AlignItems(ui.Center).Children(func() {
		dot := muted
		if data.Linked && !data.NeedsRelink {
			dot = s.tone()
		}
		ui.Box(c).Size(checkinSourceDotSize, checkinSourceDotSize).Radius(checkinSourceDotSize / 2).Background(dot)
		ui.Text(c, line).FontSize(checkinTextBody).TextColor(muted).SingleLine().Shrink(1).Tooltip(line)
	})
}

func (s *Shell) gameDataLoadCard(c *ui.Context, data gameDataPageData) {
	card := checkInCard(c, "gamedata-load")
	card.Children(func() {
		title, body := "正在读取游戏数据", "读取完成后会显示角色、探索、房产和载具信息。"
		if data.LoadFailure != state.CheckInFailureNone {
			title, body = "无法读取游戏数据", checkInFailureText(data.LoadFailure)
		}
		ui.Text(c, title).FontSize(checkinTextHelp).FontWeight(600)
		ui.Text(c, body).FontSize(checkinTextBody).TextColor(muted).LineHeight(wishBodyLineHeight)
		if data.LoadFailure != state.CheckInFailureNone {
			s.quietButton(c, "gamedata-load-retry", "重试读取", false).AlignSelf(ui.Start).OnClick(func() { gameDataInvoke(data.OnLoad) })
		}
	})
}

func (s *Shell) gameDataAccountCard(c *ui.Context, data gameDataPageData) {
	tone := s.tone()
	card := checkInCard(c, "gamedata-account")
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
			if data.Linked && !data.NeedsRelink {
				s.quietButton(c, "gamedata-refresh", "刷新", data.StatusPhase == state.CheckStatusLoading).OnClick(func() { gameDataInvoke(data.OnStatus) })
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
				ui.Text(c, "这条关联已失效，本地记录仍然保留；重新关联后才能读取游戏数据。").
					FontSize(checkinTextBody).TextColor(muted).LineHeight(wishBodyLineHeight)
				s.quietButton(c, "gamedata-relink", "重新关联", false).AlignSelf(ui.Start).OnClick(func() { gameDataInvoke(data.OnRelink) })
			}
		case data.AccountID == "":
			ui.Text(c, "还没有已保存的游戏账号").FontSize(checkinTextHelp).FontWeight(600)
			ui.Text(c, "启动游戏并在登录窗口勾选「在此设备保存账号」，回来就能关联塔吉多。").
				FontSize(checkinTextBody).TextColor(muted).LineHeight(wishBodyLineHeight)
			if data.CanLaunch {
				s.accentButton(c, "gamedata-start-game", "启动游戏并保存账号").AlignSelf(ui.Start).OnClick(func() { gameDataInvoke(data.OnStartGame) })
			}
		default:
			ui.Text(c, "关联塔吉多后读取游戏数据").FontSize(checkinTextHelp).FontWeight(600)
			ui.Text(c, "关联会复用已保存的游戏账号直接完成，无需短信；也可以改用短信登录。关联会顶掉手机上已登录的塔吉多。").
				FontSize(checkinTextBody).TextColor(muted).LineHeight(wishBodyLineHeight)
			s.accentButton(c, "gamedata-relink", "去关联塔吉多").AlignSelf(ui.Start).OnClick(func() { gameDataInvoke(data.OnRelink) })
		}
	})
}

func (s *Shell) gameDataRoleCard(c *ui.Context, data gameDataPageData, tone ui.Color) {
	card := checkInCard(c, "gamedata-role").Gap(checkinStatusGap)
	card.Label("角色概览")
	card.Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(checkinSpaceSM).Children(func() {
			ui.Text(c, "角色概览").FontSize(checkinTextHelp).FontWeight(600)
			ui.Box(c).Grow(1)
			ui.Text(c, s.checkInFetchedLine(checkInPageData{Role: data.Role, StatusPhase: data.StatusPhase})).
				FontSize(checkinTextCaption).TextColor(muted).SingleLine()
		})
		haveRole := data.Role.RoleID != "" || data.Role.Name != ""
		switch data.StatusPhase {
		case state.CheckStatusFailed:
			reason := checkInFailureText(data.StatusFailure)
			if reason == "" {
				reason = "请稍后再试"
			}
			line := "读取游戏数据失败 · " + reason
			if haveRole {
				line = "刷新失败 · " + reason + "；下面仍是上次获取的数据。"
			}
			ui.Text(c, line).FontSize(checkinTextBody).TextColor(s.wishWarningColor()).LineHeight(wishBodyLineHeight)
			if !haveRole {
				s.quietButton(c, "gamedata-status-retry", "重试", false).AlignSelf(ui.Start).OnClick(func() { gameDataInvoke(data.OnStatus) })
				return
			}
		case state.CheckStatusLoading:
			if !haveRole {
				ui.Text(c, "正在读取游戏数据…").FontSize(checkinTextBody).TextColor(muted)
				return
			}
		case state.CheckStatusReady:
			if !haveRole {
				return
			}
		default:
			if !haveRole {
				ui.Text(c, "尚未读取游戏数据。点「刷新」从塔吉多获取。").FontSize(checkinTextBody).TextColor(muted)
				s.quietButton(c, "gamedata-status-refresh", "刷新", false).AlignSelf(ui.Start).OnClick(func() { gameDataInvoke(data.OnStatus) })
				return
			}
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
		secondary := fmt.Sprintf("角色等级 %d · 鉴别等级 %d · 登录 %d 天", role.Level, role.WorldLevel, role.LoginDays)
		if role.CharCount > 0 {
			secondary += fmt.Sprintf(" · 角色 %d", role.CharCount)
		}
		if role.Server != "" {
			secondary += " · " + role.Server
		}
		if role.AchievementTotal > 0 {
			secondary += fmt.Sprintf(" · 成就 %d/%d", role.Achievement, role.AchievementTotal)
		}
		ui.Text(c, secondary).FontSize(checkinTextBody).TextColor(muted).SingleLine().Shrink(1).Tooltip(secondary)
		ui.Text(c, "数值来自塔吉多；未提供的字段显示为空，不做推算。").FontSize(checkinTextCaption).TextColor(muted).LineHeight(wishBodyLineHeight)
	})
}

func (s *Shell) gameDataCharactersCard(c *ui.Context, characters []state.CheckInCharacter, total int, tone ui.Color) {
	if len(characters) == 0 {
		return
	}
	card := checkInCard(c, "gamedata-characters")
	card.Label("角色收集")
	card.Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(checkinSpaceSM).Children(func() {
			ui.Text(c, "角色收集").FontSize(checkinTextHelp).FontWeight(600)
			ui.Box(c).Grow(1)
			label := fmt.Sprintf("已读取 %d 名", len(characters))
			if total > len(characters) {
				label += fmt.Sprintf(" · 服务器报告共 %d 名", total)
			}
			ui.Text(c, label).FontSize(checkinTextCaption).TextColor(muted).SingleLine()
		})
		s.gameDataGrid(c, len(characters), 4, func(i int) {
			s.gameDataCharacterCard(c, characters[i], tone)
		})
	})
}

func (s *Shell) gameDataCharacterCard(c *ui.Context, ch state.CheckInCharacter, tone ui.Color) {
	card := ui.Column(c).Grow(1).Shrink(1).Padding(12).Radius(10).
		Border(checkinHairlineWidth, hairline).
		Material(frostedSurface{Tint: ui.RGBA(12, 14, 18, checkinStatusOpacity)}).
		Gap(4)
	card.Label("角色 " + ch.Name)
	card.Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(checkinSpaceSM).Children(func() {
			ui.Text(c, ch.Name).FontSize(checkinTextBody).FontWeight(600).SingleLine().Shrink(1)
			ui.Box(c).Grow(1)
			if quality := gameDataQualityLabel(ch.Quality); quality != "" {
				ui.Text(c, quality).Font(monoFont).FontSize(checkinTextCaption).TextColor(tone)
			}
		})
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(checkinSpaceXS).Children(func() {
			if element := gameDataElementLabel(ch.ElementType); element != "" {
				s.gameDataChip(c, element, tone)
			}
			if ch.Level > 0 {
				ui.Text(c, fmt.Sprintf("Lv.%d", ch.Level)).FontSize(checkinTextCaption).TextColor(muted)
			}
			if ch.Awaken > 0 {
				ui.Text(c, fmt.Sprintf("觉醒 %d", ch.Awaken)).FontSize(checkinTextCaption).TextColor(muted)
			}
		})
		if ch.MixLevel > 0 || ch.Likeability > 0 {
			var parts []string
			if ch.MixLevel > 0 {
				parts = append(parts, fmt.Sprintf("共鸣 %d", ch.MixLevel))
			}
			if ch.Likeability > 0 {
				parts = append(parts, fmt.Sprintf("好感 %d", ch.Likeability))
			}
			ui.Text(c, strings.Join(parts, " · ")).FontSize(checkinTextCaption).TextColor(muted).SingleLine()
		}
	})
}

func (s *Shell) gameDataChip(c *ui.Context, text string, tone ui.Color) {
	ui.Row(c).Height(checkinChipHeight).Padding(0, checkinChipPadding).Radius(checkinChipRadius).
		Border(checkinHairlineWidth, tone.Alpha(.45)).AlignItems(ui.Center).
		Children(func() { ui.Text(c, text).FontSize(checkinTextCaption).TextColor(tone).SingleLine() })
}

func (s *Shell) gameDataAreasCard(c *ui.Context, areas []state.CheckInArea, tone ui.Color) {
	if len(areas) == 0 {
		return
	}
	card := checkInCard(c, "gamedata-areas")
	card.Label("区域探索")
	card.Children(func() {
		ui.Text(c, "区域探索").FontSize(checkinTextHelp).FontWeight(600)
		width, _ := c.Size()
		areaWidth := (width - railWidth - bezelWidth - 2*checkinInset - 2*checkinCardPadding - checkinStatGap) / 2
		if areaWidth < 120 {
			areaWidth = 120
		}
		for start := 0; start < len(areas); start += 2 {
			start := start
			ui.Row(c).FillWidth().Gap(checkinStatGap).AlignItems(ui.Start).Children(func() {
				for i := 0; i < 2; i++ {
					if start+i < len(areas) {
						area := areas[start+i]
						s.checkInStat(c, area.Name, area.Progress, area.Total, areaWidth, tone)
					} else {
						ui.Box(c).Width(areaWidth)
					}
				}
			})
		}
		ui.Text(c, "进度来自塔吉多；尚未解锁的区域可能没有进度。").
			FontSize(checkinTextCaption).TextColor(muted).LineHeight(wishBodyLineHeight)
	})
}

func (s *Shell) gameDataCollectionsCard(c *ui.Context, realEstate, vehicle state.CheckInCollection, tone ui.Color) {
	if realEstate.Total == 0 && vehicle.Total == 0 {
		return
	}
	card := checkInCard(c, "gamedata-collections")
	card.Label("房产与载具")
	card.Children(func() {
		ui.Text(c, "房产与载具").FontSize(checkinTextHelp).FontWeight(600)
		width, _ := c.Size()
		colWidth := (width - railWidth - bezelWidth - 2*checkinInset - 2*checkinCardPadding - checkinStatGap) / 2
		if colWidth < 120 {
			colWidth = 120
		}
		ui.Row(c).FillWidth().Gap(checkinStatGap).AlignItems(ui.Start).Children(func() {
			gameDataCollection(c, "房产", realEstate, colWidth, tone)
			gameDataCollection(c, "载具", vehicle, colWidth, tone)
		})
	})
}

func gameDataCollection(c *ui.Context, label string, collection state.CheckInCollection, width float32, tone ui.Color) {
	ui.Column(c).Width(width).Gap(checkinSpaceXS).Children(func() {
		ui.Text(c, label).FontSize(checkinTextCaption).TextColor(muted).SingleLine()
		text := "—"
		if collection.Total > 0 {
			text = fmt.Sprintf("%d / %d", collection.Own, collection.Total)
		}
		ui.Text(c, text).Font(monoFont).FontSize(checkinTextValue).TextColor(paper).SingleLine()
		if collection.ShowName != "" {
			ui.Text(c, "当前展示 "+collection.ShowName).FontSize(checkinTextCaption).TextColor(muted).SingleLine().Shrink(1)
		}
		if collection.Total > 0 {
			ratio := float64(collection.Own) / float64(collection.Total)
			if ratio > 1 {
				ratio = 1
			}
			if ratio < 0 {
				ratio = 0
			}
			ui.Box(c).FillWidth().Height(checkinMeterHeight).Radius(checkinMeterRadius).Background(hairline).Children(func() {
				ui.Box(c).Width(float32(ratio) * width).Height(checkinMeterHeight).Radius(checkinMeterRadius).Background(tone)
			})
		}
	})
}

func gameDataQualityLabel(code string) string {
	switch strings.ToUpper(code) {
	case "ITEM_QUALITY_ORANGE":
		return "S"
	case "ITEM_QUALITY_PURPLE":
		return "A"
	case "ITEM_QUALITY_BLUE":
		return "B"
	case "ITEM_QUALITY_GREEN":
		return "C"
	case "ITEM_QUALITY_WHITE":
		return "N"
	}
	return ""
}

func gameDataElementLabel(code string) string {
	switch strings.ToUpper(code) {
	case "CHARACTER_ELEMENT_TYPE_PSYCHE":
		return "魂"
	case "CHARACTER_ELEMENT_TYPE_COSMOS":
		return "光"
	case "CHARACTER_ELEMENT_TYPE_NATURE":
		return "灵"
	case "CHARACTER_ELEMENT_TYPE_INCANTATION":
		return "咒"
	case "CHARACTER_ELEMENT_TYPE_CHAOS":
		return "暗"
	case "CHARACTER_ELEMENT_TYPE_LAKSHANA":
		return "相"
	}
	return ""
}

func (s *Shell) gameDataGrid(c *ui.Context, count, perRow int, item func(int)) {
	for start := 0; start < count; start += perRow {
		start := start
		ui.Row(c).FillWidth().Gap(checkinCardGap).AlignItems(ui.Stretch).Children(func() {
			for i := 0; i < perRow; i++ {
				if start+i < count {
					item(start + i)
				} else {
					ui.Box(c).Grow(1)
				}
			}
		})
	}
}

func gameDataInvoke(fn func()) {
	if fn != nil {
		fn()
	}
}
