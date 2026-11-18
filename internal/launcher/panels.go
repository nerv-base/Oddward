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
	title, kicker := "设置", "SYS / SETTINGS"
	switch s.Model.Overlay {
	case state.Scenarios:
		title, kicker = "状态预览", "SYS / SCENARIOS"
	case state.About:
		title, kicker = "关于 Oddward", "SYS / ABOUT"
	case state.ConfirmQuit:
		title, kicker = "离开前暂停一下？", "SYS / EXIT"
	case state.NewsDetails:
		title, kicker = "官方公告", "OFFICIAL / DISPATCH"
	case state.LocateGame:
		title, kicker = "定位游戏", "SYS / LOCATE"
	case state.LaunchNotice:
		title, kicker = "首次启动确认", "SYS / NOTICE"
	case state.UpdateDetails:
		title, kicker = "Oddward 更新", "SYS / UPDATE"
	case state.DownloadDetails:
		title, kicker = "游戏下载与更新", "GAME / DOWNLOAD"
	case state.NetworkSettings:
		title, kicker = "网络", "SYS / NETWORK"
	}
	ui.Overlay(c, func() {
		back := ui.Row(c.Key("modal-backdrop")).Absolute().Left(0).Top(0).Right(0).Bottom(0).Justify(ui.End).Padding(sheetInset, sheetInset, sheetInset, sheetInset).Background(ui.RGBA(6, 7, 10, .52)).Modal()
		back.Children(func() {
			// A floating rounded glass sheet, the same material as the dock.
			panel := ui.Column(c.Key("panel")).Width(408).FillHeight().Padding(22, 22, 18, 26).Gap(14).Radius(sheetRadius).Clip().
				Border(1, ui.RGBA(255, 255, 255, .10)).Shadow(0, 18, 48, 0, ui.RGBA(0, 0, 0, .45)).Role(ui.RoleDialog).Label(title)
			panel.Material(frostedSurface{Tint: ui.RGBA(18, 19, 24, .84)})
			if !s.Model.ReducedMotion && !s.Model.SystemReducedMotion {
				entered := ui.Local(panel, "entered", func() bool { return false })
				pose := ui.Pose{}
				if !*entered {
					pose, *entered = ui.Pose{X: 12}, true
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
					ui.Column(c).Grow(1).Gap(6).Children(func() {
						ui.Row(c).Gap(7).Children(func() {
							ui.Box(c).Size(6, 6).Radius(3).Background(s.tone())
							mono(c, kicker, 10).TextColor(muted)
						})
						ui.Text(c, title).FontSize(23).FontWeight(700)
					})
					iconButton(c, "dismiss", "关闭面板", "\uE8BB", 32, func() { s.Dispatch(state.Event{Kind: state.Dismiss}) })
				})
				ui.Row(c).FillWidth().Height(1).Children(func() {
					ui.Box(c).Grow(1).Height(1).Background(hairline)
				})
				switch s.Model.Overlay {
				case state.Settings:
					s.settingsPanel(c)
				case state.Scenarios:
					s.scenarioPanel(c)
				case state.About:
					s.aboutPanel(c)
				case state.ConfirmQuit:
					s.quitPanel(c)
				case state.NewsDetails:
					s.newsDetailsPanel(c)
				case state.LocateGame:
					s.managedLocationPanel(c)
				case state.LaunchNotice:
					s.managedNoticePanel(c)
				case state.UpdateDetails:
					s.updatePanel(c)
				case state.DownloadDetails:
					s.downloadPanel(c)
				case state.NetworkSettings:
					s.networkPanel(c)
				}
				if s.Model.Overlay != state.NewsDetails && s.Model.Overlay != state.LocateGame && s.Model.Overlay != state.UpdateDetails && s.Model.Overlay != state.DownloadDetails {
					ui.Box(c).Grow(1)
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

func (s *Shell) settingsPanel(c *ui.Context) {
	s.versionSetting(c)
	if s.Model.NormalMode {
		s.quietButton(c, "download-settings-entry", "下载设置", false).FillWidth().Height(downloadButtonHeight).OnClick(s.openDownloadSettings)
	}
	ui.Box(c).FillWidth().Height(1).Background(hairline)
	s.setting(c, "motion-setting", "动态背景", "静音循环播放本地视频。暂停后显示静态背景。", s.Model.Motion, s.Model.Media == state.MediaUnavailable, state.ToggleMotion)
	s.setting(c, "reduced-motion", "减少动态效果", "暂停背景与界面动画，同时遵循系统动态效果偏好。", s.Model.ReducedMotion, false, state.ToggleReducedMotion)
	status, source := "本地视频 · 静音循环", "素材仅只读加载。偏好只在本次会话生效。"
	switch s.Model.Media {
	case state.MediaLoading:
		status = "本地视频 · 正在加载"
	case state.MediaPaused:
		status = "本地画面 · 已暂停"
	case state.MediaUnavailable:
		status = "原生静态背景 · 未载入视频"
	}
	if s.Model.MediaPreview {
		status = "MOCK · 媒体状态预览"
		source = "仅预览显示状态，不证明本地解码或兼容性。偏好只在本次会话生效。"
	}
	ui.Column(c).Gap(8).Children(func() {
		ui.Text(c, "背景来源").FontSize(13).TextColor(muted)
		ui.Text(c, status).FontSize(14)
		ui.Text(c, source).FontSize(12).TextColor(muted).LineHeight(1.6)
	})
	ui.Box(c).FillWidth().Height(1).Background(hairline)
	b := ui.ButtonBase(c.Key("scenarios")).Label("状态预览").FillWidth().Height(56).Radius(14).Padding(0, 16).Border(1, hairline).Background(ui.RGBA(255, 255, 255, .05))
	if b.Hovered() || b.FocusVisible() {
		b.Background(ui.RGBA(255, 255, 255, .10)).Border(1, s.tone().Alpha(.55))
	}
	b.Children(func() {
		ui.Column(c).Gap(3).Grow(1).Children(func() {
			ui.Text(c, "状态预览").FontSize(14)
			ui.Text(c, "开发工具 · 仅模拟交互流程").FontSize(11).TextColor(muted)
		})
		glyph(c, "\uE76C", 13)
	}).OnClick(func() { s.Dispatch(state.Event{Kind: state.OpenOverlay, Overlay: state.Scenarios}) })
	ui.Row(c).FillWidth().Gap(8).Children(func() {
		if s.Model.NormalMode {
			s.quietButton(c, "game-settings", "游戏目录", false).Grow(1.6).Height(36).OnClick(func() {
				s.ManagedPathInput = s.Model.Managed.Root
				s.Dispatch(state.Event{Kind: state.OpenOverlay, Overlay: state.LocateGame})
			})
		}
		s.quietButton(c, "network", "网络", false).Grow(1).Height(36).OnClick(s.openNetworkSettings)
		s.quietButton(c, "about", "关于", false).Grow(1).Height(36).OnClick(func() {
			s.Dispatch(state.Event{Kind: state.OpenOverlay, Overlay: state.About})
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
	mono(c, "OFFICIAL · 官网公告", 10).TextColor(s.tone())
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
	ui.Box(c).FillWidth().Height(1).Background(hairline)
	ui.Text(c, state.NewsOfficialNote).FontSize(12).TextColor(muted).LineHeight(1.6)
	s.quietButton(c, "return-from-news", "返回主页", false).FillWidth().Height(40).OnClick(func() {
		s.Dispatch(state.Event{Kind: state.Dismiss})
	})
}

func (s *Shell) aboutPanel(c *ui.Context) {
	ui.Text(c, "Oddward").Font("Bahnschrift").FontSize(34).FontWeight(600).Margin(12, 0, 0, 0)
	ui.Text(c, "一个轻量的单游戏启动器。").FontSize(16)
	ui.Text(c, "公告使用官方公开数据；抽卡分析使用本地档案，可导入和导出 JSON。启动器内置国服原生登录流程和登录窗口，首次启动需确认风险；签到仍为演示。").FontSize(14).LineHeight(1.8).TextColor(muted)
	ui.Box(c).FillWidth().Height(1).Background(hairline).Margin(12, 0)
	ui.Text(c, "抽卡页物品图标运行时取自塔吉多官方 CDN，版权归原权利人；Oddward 不分发这些图片").FontSize(12).LineHeight(1.8).TextColor(muted)
	ui.Text(c, "MyGo Native UI · Go · Direct3D").Font("Segoe UI").FontSize(13).TextColor(muted)
	ui.Text(c, "原始代码 GPL-3.0-or-later\nMyGo · MIT License\n游戏、活动标语与影像版权归各自权利人所有。").FontSize(12).LineHeight(1.8).TextColor(muted)
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
