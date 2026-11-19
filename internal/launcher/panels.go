// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func (s *Shell) overlay(c *ui.Context) {
	s.wishLinkOverlay(c)
	if s.Model.Overlay == state.NoOverlay {
		return
	}
	if s.Model.Overlay == state.OverlayInstallPath {
		s.installPathDialog(c)
		return
	}
	var title string
	width := sheetWidth
	align, leftInset, enter := ui.End, float32(sheetInset), ui.Pose{X: sheetEnterDistance}
	gameSettings := s.Model.GameSettingsVisible()
	cancelDownload := s.Model.Overlay == state.ConfirmDownloadCancel
	centered := gameSettings || s.Model.Overlay == state.LocateGame || cancelDownload
	switch s.Model.Overlay {
	case state.Scenarios, state.UpdateDetails:
		align, leftInset, enter = ui.Start, railWidth+sheetInset, ui.Pose{X: -sheetEnterDistance}
	}
	switch s.Model.Overlay {
	case state.Scenarios:
		title = "状态预览"
	case state.ConfirmQuit:
		title = "退出启动器？"
	case state.ConfirmDownloadCancel:
		title = "取消当前任务？"
	case state.NewsDetails:
		title = "官方公告"
	case state.GameSettings, state.LocateGame:
		if gameSettings {
			title = "游戏设置"
		} else {
			title = "定位游戏"
		}
	case state.LaunchNotice:
		title = "首次启动确认"
	case state.UpdateDetails:
		title = "Oddward 更新"
	}
	shade, tint := ui.RGBA(6, 7, 10, .52), ui.RGBA(18, 19, 24, .84)
	if centered {
		align, leftInset, enter = ui.Center, sheetInset, ui.Pose{Y: sheetEnterDistance}
		shade, tint = ui.RGBA(6, 7, 10, .42), ui.RGBA(18, 19, 24, .72)
	}
	ui.Overlay(c, func() {
		back := ui.Row(c.Key("modal-backdrop")).Absolute().Left(0).Top(0).Right(0).Bottom(0).Justify(align).Padding(sheetInset, sheetInset, sheetInset, leftInset).Background(shade).Modal()
		if centered {
			back.AlignItems(ui.Center)
		}
		back.Children(func() {
			panel := ui.Column(c.Key("panel")).Padding(22, 22, 18, 26).Gap(sheetGap).Radius(sheetRadius).Clip().
				Border(1, ui.RGBA(255, 255, 255, .10)).Shadow(0, 18, 48, 0, ui.RGBA(0, 0, 0, .45)).Role(ui.RoleDialog).Label(title).
				Material(frostedSurface{Tint: tint})
			switch {
			case gameSettings:
				w, h := c.Size()
				panel.Size(w*gameSettingsWidthRatio, h*gameSettingsHeightRatio)
			case centered:
				w, h := c.Size()
				panel.Size(min(locateGameDialogWidth, w-2*sheetInset), min(locateGameDialogHeight, h-2*sheetInset))
			default:
				panel.Width(width).FillHeight()
			}
			if !s.Model.ReducedMotion && !s.Model.SystemReducedMotion {
				entered := ui.Local(panel, "entered", func() bool { return false })
				pose := ui.Pose{}
				if !*entered {
					pose, *entered = enter, true
					c.Invalidate()
				}
				panel.AnimatePose(pose, ui.Animation{Duration: 220 * time.Millisecond})
			}
			panel.Children(func() {
				// A soft tone glow in the sheet's corner ties it to the art.
				ui.Box(c).Absolute().Left(-140).Top(-160).Size(360, 320).PassThrough().RadialGradient(ui.RadialGradient{Radius: 180, Stops: []ui.GradientStop{
					{Color: s.tone().Alpha(.14), At: 0}, {Color: ui.Transparent, At: 1},
				}})
				ui.Row(c).Height(52).Children(func() {
					ui.Text(c, title).Grow(1).FontSize(23).FontWeight(700)
					iconButton(c, "dismiss", "关闭面板", "\uE8BB", 32, func() { s.Dispatch(state.Event{Kind: state.Dismiss}) })
				})
				ui.Row(c).FillWidth().Height(1).Children(func() {
					ui.Box(c).Grow(1).Height(1).Background(hairline)
				})
				switch s.Model.Overlay {
				case state.GameSettings:
					s.gameSettingsPanel(c)
				case state.Scenarios:
					s.scenarioPanel(c)
					ui.Box(c).Grow(1)
				case state.ConfirmQuit:
					s.quitPanel(c)
					ui.Box(c).Grow(1)
				case state.ConfirmDownloadCancel:
					s.downloadCancelPanel(c)
				case state.NewsDetails:
					s.newsDetailsPanel(c)
				case state.LocateGame:
					if gameSettings {
						s.gameSettingsPanel(c)
					} else {
						s.managedLocationPanel(c)
					}
				case state.LaunchNotice:
					s.managedNoticePanel(c)
					ui.Box(c).Grow(1)
				case state.UpdateDetails:
					s.updatePanel(c)
				}
				ui.Row(c).Gap(8).Children(func() {
					mono(c, "ESC", 10).Padding(2, 7).Radius(5).Background(ui.RGBA(255, 255, 255, .06)).Border(1, hairline).TextColor(muted)
					ui.Text(c, "关闭当前面板").FontSize(12).TextColor(muted)
				})
			})
			// Modal scopes suppress the window-level F8 binding.
			closePreview := s.Model.Overlay == state.Scenarios && back.OverlayShortcut(0, ui.KeyF8) && !c.ShortcutRepeated()
			if back.OverlayShortcut(0, ui.KeyEscape) || closePreview || panel.PressedOutside() {
				s.Dispatch(state.Event{Kind: state.Dismiss})
			}
		})
	})
}

func (s *Shell) setting(c *ui.Context, key, label, description string, on, disabled bool, event state.EventKind) {
	ui.Column(c).Gap(8).Children(func() {
		ui.Row(c).FillWidth().Height(36).Children(func() {
			caption := ui.Text(c, label).FontSize(15).FontWeight(500).Grow(1)
			// LabelledBy makes the caption activate and focus the native switch.
			// Bind a local projection, never mutate the reducer's model directly.
			s.preferenceSwitch(c, key, caption, on, disabled, event)
		})
		ui.Text(c, description).FontSize(13).TextColor(muted).LineHeight(1.6)
		ui.Box(c).FillWidth().Height(1).Background(hairline).Margin(8, 0, 0, 0)
	})
}

func (s *Shell) launcherSettingsPage(c *ui.Context) {
	canvas(c, "launcher-settings").Material(frostedSurface{Tint: ui.RGBA(13, 15, 22, .76)}).
		Role(ui.RoleGroup).Label("启动器设置内容").Children(func() {
		ui.Row(c.Key("launcher-settings-layout")).Absolute().Top(launcherSettingsTop).Bottom(launcherSettingsInset).
			Left(launcherSettingsInset).Right(launcherSettingsInset).AlignItems(ui.Start).Gap(launcherSettingsColumnGap).Children(func() {
			s.launcherSettingsSections(c)
		})
	})
}

func (s *Shell) launcherSettingsSections(c *ui.Context) {
	section := s.Model.SettingsSection
	ui.Column(c.Key("settings-nav")).Width(launcherSettingsNavWidth).Shrink(0).Gap(launcherSettingsGap).Children(func() {
		ui.Text(c, "启动器设置").FontSize(launcherSettingsTitleSize).FontWeight(700)
		ui.Column(c.Key("settings-tabs")).FillWidth().Gap(4).Role(ui.RoleTabList).Label("设置分类").Orientation(ui.Vertical).FocusGroup(ui.Vertical).Children(func() {
			for _, tab := range []struct {
				label, icon string
				section     state.LauncherSettingsSection
			}{
				{"常规", "\uE713", state.SettingsGeneral},
				{"外观", "\uE790", state.SettingsAppearance},
				{"下载与网络", "\uE774", state.SettingsNetwork},
				{"文件", "\uE8B7", state.SettingsFiles},
				{"关于", "\uE946", state.SettingsAbout},
			} {
				item := tab.section
				selected := section == item
				b := ui.ButtonBase(c.Key(tab.label)).Label(tab.label).Role(ui.RoleTab).Selected(selected).
					FillWidth().Height(launcherSettingsTabHeight).Padding(0, 12).Gap(10).Radius(8).Justify(ui.Start).TextColor(muted)
				if selected {
					b.Background(s.tone().Alpha(.18)).TextColor(paper)
				} else if b.Hovered() || b.FocusVisible() {
					s.controlFill(b, ui.RGBA(255, 255, 255, .08))
					b.TextColor(paper)
				}
				b.Children(func() {
					glyph(c, tab.icon, 16)
					ui.Text(c, tab.label).FontSize(14)
				}).OnClick(func() { s.selectSettingsSection(item) })
			}
		})
	})
	ui.Scroll(c.Key("settings-content")).Width(launcherSettingsBodyWidth).MinWidth(0).FillHeight().Scrollbars(true).Children(func() {
		ui.Column(c.Key("settings-section")).FillWidth().Gap(launcherSettingsGap).Children(func() {
			switch section {
			case state.SettingsAppearance:
				s.appearanceSection(c)
			case state.SettingsNetwork:
				s.downloadSection(c)
			case state.SettingsFiles:
				s.filesSection(c)
			case state.SettingsAbout:
				s.aboutPanel(c)
			default:
				s.generalSection(c)
			}
		})
	})
}

func (s *Shell) gameSettingsPanel(c *ui.Context) {
	ui.Column(c.Key("game-settings-body")).Grow(1).MinHeight(0).FillWidth().Role(ui.RoleGroup).Label("游戏设置内容").Children(func() {
		if s.Model.Overlay == state.LocateGame {
			s.managedLocationPanel(c)
			return
		}
		ui.Scroll(c.Key("game-settings-content")).Grow(1).MinHeight(0).FillWidth().Scrollbars(true).Children(func() {
			ui.Column(c).FillWidth().Gap(downloadGap).Children(func() {
				s.gameBasicInformation(c)
			})
		})
	})
}

func (s *Shell) scenarioPanel(c *ui.Context) {
	ui.Column(c.Key("scenario-content")).Gap(10).Children(func() {
		ui.Text(c, "全部 MOCK · 内存演示，不联网、不修改游戏文件。").FontSize(12).TextColor(muted).LineHeight(1.5)
		ui.Row(c).FillWidth().Height(30).Children(func() {
			caption := ui.Text(c, "冻结演示时钟").FontSize(14).Grow(1)
			s.preferenceSwitch(c, "demo-clock", caption, s.Model.DemoPaused, false, state.ToggleDemoClock)
		})
		ui.Row(c).FillWidth().Gap(8).Children(func() {
			s.quietButton(c, "step-demo", "单步完成", !state.Active(s.Model.Flow)).Height(32).Grow(1).OnClick(func() {
				s.Dispatch(state.Event{Kind: state.StepDemo})
			})
			s.quietButton(c, "fail-demo", "模拟失败", !state.Active(s.Model.Flow)).Height(32).Grow(1).OnClick(func() {
				s.Dispatch(state.Event{Kind: state.FailDemo})
			})
		})
		// Two columns keep every state available even at the minimum window size.
		items := []struct {
			label string
			flow  state.Flow
		}{
			{"准备就绪", state.Ready}, {"尚未安装", state.NotInstalled}, {"有可用更新", state.UpdateAvailable},
			{"正在下载", state.Downloading}, {"下载已暂停", state.Paused}, {"正在校验", state.Verifying},
			{"正在启动", state.Launching}, {"游戏运行中", state.Running}, {"连接失败", state.Failed}, {"重新检查", state.Checking},
		}
		ui.Column(c).FillWidth().Gap(4).Children(func() {
			for i := 0; i < len(items); i += 2 {
				ui.Row(c).FillWidth().Gap(8).Children(func() {
					for _, item := range items[i : i+2] {
						row := item
						b := s.quietButton(c, "scenario-"+row.label, row.label, false).Width(182).Height(32).Padding(0, 10)
						if row.flow == s.Model.Flow {
							b.Background(s.tone().Alpha(.16)).Border(1, s.tone().Alpha(.8))
						}
						b.OnClick(func() { s.Dispatch(state.Event{Kind: state.SelectScenario, Flow: row.flow}) })
					}
				})
			}
		})
		ui.Text(c, "媒体状态预览 · MOCK").FontSize(12).TextColor(muted)
		ui.Row(c).FillWidth().Gap(8).Children(func() {
			for _, item := range []struct {
				label string
				state state.Media
			}{{"媒体加载", state.MediaLoading}, {"媒体播放", state.MediaPlaying}, {"媒体暂停", state.MediaPaused}, {"媒体不可用", state.MediaUnavailable}} {
				row := item
				b := s.quietButton(c, "media-preview-"+row.label, row.label, false).Width(87).Height(30).Padding(0, 5)
				if s.Model.MediaPreview && s.Model.Media == row.state {
					b.Border(1, s.tone())
				}
				b.OnClick(func() { s.Dispatch(state.Event{Kind: state.SelectMediaState, Media: row.state}) })
			}
		})
		s.quietButton(c, "reset-media-preview", "退出媒体预览", !s.Model.MediaPreview).FillWidth().Height(28).OnClick(func() {
			s.Dispatch(state.Event{Kind: state.ResetMediaPreview})
		})
	})
}

// The detail panel shows one official announcement the shell already read
// from the public site. Nothing here opens a browser, and the note says so
// rather than pretending the text is local demo data.
func (s *Shell) newsDetailsPanel(c *ui.Context) {
	tab, item := s.Model.NewsTab, s.Model.DetailItem
	if tab > 2 {
		tab = 0
	}
	if item > 2 {
		item = 0
	}
	title, date := s.Model.DetailTitle, s.Model.DetailDate
	if title == "" {
		title = "官方公告"
	}
	ui.Text(c, title).FontSize(22).FontWeight(600).LineHeight(1.5)
	if date != "" {
		ui.Text(c, date).Font("Segoe UI").FontSize(12).TextColor(muted)
	}
	switch s.Model.Detail {
	case state.NewsPending:
		ui.Text(c, "正在读取公告正文…").FontSize(14).LineHeight(1.8).TextColor(muted)
	case state.NewsUnavailable:
		ui.Text(c, "正文获取失败").FontSize(14).TextColor(paper)
		if s.Model.DetailError != "" {
			ui.Text(c, s.Model.DetailError).FontSize(12).LineHeight(1.6).TextColor(muted)
		}
		s.quietButton(c, "detail-retry", "重新获取正文", false).FillWidth().Height(34).OnClick(func() {
			s.Dispatch(state.Event{Kind: state.DetailRequested, Index: item})
		})
	default:
		ui.Scroll(c.Key("news-body")).Grow(1).Scrollbars(true).Children(func() {
			ui.Text(c, s.Model.DetailBody).FontSize(14).LineHeight(1.8)
		})
	}
	if _, _, url, ok := s.Model.NewsRows.At(uint8(tab), uint8(item)); ok && url != "" && s.OpenURL != nil {
		s.quietButton(c, "news-open-browser", "在浏览器打开原文", false).FillWidth().Height(40).OnClick(func() { s.OpenURL(url) })
	}
	s.quietButton(c, "return-from-news", "返回主页", false).FillWidth().Height(40).OnClick(func() {
		s.Dispatch(state.Event{Kind: state.Dismiss})
	})
}

func (s *Shell) aboutPanel(c *ui.Context) {
	ui.Column(c.Key("about-identity")).FillWidth().Gap(6).Children(func() {
		ui.Icon(c, brandSymbol).Size(launcherSettingsMarkSize, launcherSettingsMarkSize).TextColor(paper).Label("Oddward 标志")
		ui.Text(c, "Oddward").Font("Bahnschrift").FontSize(28).FontWeight(600)
		ui.Text(c, "《异环》非官方启动器").FontSize(14).TextColor(muted)
	})
	s.versionSetting(c)
	ui.Box(c).FillWidth().Height(1).Background(hairline)
	ui.Text(c, "GPL-3.0-or-later").FontSize(13)
	ui.Text(c, "MyGo · MIT License").FontSize(12).TextColor(muted)
	ui.Text(c, "游戏素材及塔吉多物品图标的版权归各自权利人所有。").FontSize(12).LineHeight(1.6).TextColor(muted)
	if s.OpenURL != nil {
		links := []struct{ key, label, url string }{
			{"about-home", "项目主页", "https://github.com/nerv-base/Oddward"},
			{"about-issues", "问题反馈", "https://github.com/nerv-base/Oddward/issues"},
			{"about-releases", "更新记录", "https://github.com/nerv-base/Oddward/releases"},
		}
		ui.Row(c).Gap(8).Margin(6, 0, 0, 0).Children(func() {
			for _, link := range links {
				url := link.url
				s.quietButton(c, link.key, link.label, false).Height(32).OnClick(func() { s.OpenURL(url) })
			}
		})
	}
}

func (s *Shell) quitPanel(c *ui.Context) {
	title, detail := "当前演示流程尚未结束。", "退出将结束本次演示，进度不会保存。你的游戏文件不会被修改。"
	if s.Model.NormalMode {
		title, detail = "当前启动流程尚未结束。", "退出只取消准备或停止观察，不终止已经创建的登录组件与游戏。"
	}
	ui.Text(c, title).FontSize(17).Margin(10, 0, 0, 0)
	ui.Text(c, detail).FontSize(14).LineHeight(1.8).TextColor(muted)
	ui.Box(c).Height(12)
	s.accentButton(c, "stay", "继续留在这里").Height(46).Radius(23).FillWidth().AutoFocus().OnClick(func() { s.Dispatch(state.Event{Kind: state.Dismiss}) })
	exitLabel := "退出演示"
	if s.Model.NormalMode {
		exitLabel = "退出启动器"
	}
	s.quietButton(c, "confirm-exit", exitLabel, false).Height(44).FillWidth().OnClick(func() { s.Dispatch(state.Event{Kind: state.ConfirmExit}) })
}
