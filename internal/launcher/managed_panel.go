// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func (s *Shell) gameBasicInformation(c *ui.Context) {
	p := state.PresentDownload(s.Model)
	ui.Text(c, "游戏").FontSize(gameSettingsHeadingSize).FontWeight(600)
	ui.Row(c.Key("current-game-path")).FillWidth().Gap(downloadGap).Children(func() {
		root := s.Model.Managed.Root
		path := root
		if path == "" {
			path = "未选择目录"
		}
		ui.Row(c.Key("game-path-value")).Grow(1).MinWidth(0).Height(downloadButtonHeight).Padding(0, downloadGap).AlignItems(ui.Center).
			Radius(gameSettingsTabRadius).Border(1, hairline).Tooltip(root).Children(func() {
			ui.Text(c, path).Grow(1).MinWidth(0).FontSize(downloadBodySize).SingleLine()
		})
		s.quietButton(c, "locate-game", "定位游戏", false).Shrink(0).Height(downloadButtonHeight).OnClick(func() {
			s.ManagedPathInput = root
			s.Dispatch(state.Event{Kind: state.OpenOverlay, Overlay: state.LocateGame})
		})
	})
	ui.Text(c, "定位已有安装时只读检查游戏文件。").FontSize(downloadCaptionSize).TextColor(muted).LineHeight(1.5)
	if root := s.Model.Managed.Root; root != "" {
		ui.Text(c, "版本").FontSize(gameSettingsHeadingSize).FontWeight(600).Margin(8, 0, 0, 0)
		current := p.Current
		if current == "" {
			current = "尚未确认"
		}
		s.settingLine(c, "当前版本", current)
		if p.OfferVersion != "" {
			s.settingLine(c, "可更新到", p.OfferVersion)
		}
		ui.Row(c.Key("game-panel-actions")).FillWidth().Gap(downloadGap).Children(func() {
			open := s.quietButton(c, "game-open-folder", "打开安装文件夹", s.OpenFolder == nil)
			open.Shrink(0).Height(downloadButtonHeight).OnClick(func() {
				if s.OpenFolder != nil {
					s.OpenFolder(root)
				}
			})
			if p.CanVerify {
				s.quietButton(c, "game-verify", "重新校验", false).Shrink(0).Height(downloadButtonHeight).OnClick(func() {
					s.Dispatch(state.Event{Kind: state.InstallVerifyRequest})
				})
			}
			if s.Model.Download.Preload != state.PreloadStateUnavailable || s.Model.Download.Phase != state.DownloadIdle {
				s.quietButton(c, "game-preload", "预下载管理", false).Shrink(0).Height(downloadButtonHeight).OnClick(func() {
					s.Dispatch(state.Event{Kind: state.Dismiss})
					s.Dispatch(state.Event{Kind: state.OpenHomeDownloads})
				})
			}
		})
	}
}

// settingLine is one read-only label and value pair in the game panel.
func (s *Shell) settingLine(c *ui.Context, label, value string) {
	ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
		ui.Text(c, label).FontSize(downloadCaptionSize).TextColor(muted).Shrink(0)
		ui.Text(c, value).FontSize(downloadCaptionSize).SingleLine().Grow(1).TextAlign(ui.End)
	})
}

func (s *Shell) managedLocationPanel(c *ui.Context) {
	p := s.Model.Managed
	locked := state.ManagedBusy(p.Phase) || state.ManagedActive(p.Phase)
	ui.Scroll(c.Key("managed-location")).Grow(1).MinHeight(0).FillWidth().Scrollbars(true).Children(func() {
		ui.Column(c).FillWidth().Gap(sheetGap).Children(func() {
			ui.Text(c, "选择已有的异环游戏").FontSize(17).FontWeight(600)
			ui.Text(c, "只读检查已有安装，不移动、不修复、不重新下载游戏。").FontSize(13).TextColor(muted).LineHeight(1.5)
			ui.Text(c, "可选择游戏根目录、Client、WindowsNoEditor，或粘贴 HTGame.exe 的完整路径。").FontSize(12).TextColor(muted).LineHeight(1.5)
			ui.TextInput(c.Key("managed-game-path"), &s.ManagedPathInput).Label("游戏目录").FillWidth().Height(40).Padding(9, 10).Radius(10).Border(1, hairline).Background(ui.RGBA(255, 255, 255, .05)).TextColor(paper).FontSize(13).Placeholder("选择或粘贴已有游戏目录").ReadOnly(locked).Disabled(locked)
			if s.quietButton(c, "managed-browse", "浏览游戏目录", locked).FillWidth().Clicked() {
				s.Dispatch(state.Event{Kind: state.ManagedBrowse})
			}
			if p.Root != "" {
				ui.Text(c, "当前目录："+p.Root).FontSize(12).TextColor(muted).SingleLine().Tooltip(p.Root)
			}
			if p.Phase == state.ManagedLocating {
				ui.Text(c, "正在只读检查所选目录…").FontSize(13).TextColor(paper)
			}
			ui.Text(c, "结构检查不等于版本完整性或登录兼容性验证。仅保存目录记录。").FontSize(11).TextColor(muted).LineHeight(1.5)
			if p.Error != "" {
				ui.Text(c, p.Error).FontSize(12).TextColor(warning).LineHeight(1.5)
			}
		})
	})
	ui.Row(c.Key("managed-location-actions")).FillWidth().Gap(downloadGap).Children(func() {
		if s.quietButton(c, "managed-location-back", "返回", false).Grow(1).Height(downloadButtonHeight).Clicked() {
			s.Dispatch(state.Event{Kind: state.SettingsBack})
		}
		if s.quietButton(c, "managed-adopt", "定位此目录", locked || strings.TrimSpace(s.ManagedPathInput) == "").Grow(1).Height(downloadButtonHeight).Clicked() {
			s.Dispatch(state.Event{Kind: state.ManagedSelectPath, Path: strings.TrimSpace(s.ManagedPathInput)})
		}
	})
}

func (s *Shell) managedNoticePanel(c *ui.Context) {
	ui.Column(c.Key("managed-notice")).FillWidth().Gap(15).Children(func() {
		ui.Text(c, "登录窗口随启动器提供").FontSize(17).FontWeight(600)
		ui.Text(c, "启动器已内置国服原生登录流程和登录窗口，可使用已保存的账号或在登录窗口完成验证。").FontSize(13).TextColor(muted).LineHeight(1.6)
		ui.Text(c, "并非官方授权").FontSize(15).FontWeight(600).TextColor(warning)
		ui.Text(c, "账号限制或封禁风险").FontSize(14).TextColor(warning)
		ui.Text(c, "登录协议来自对官方组件的社区兼容性研究。来源和文件校验不等于官方授权，也不保证登录兼容。请在了解风险后继续。").FontSize(13).TextColor(muted).LineHeight(1.6)
		ui.Text(c, "登录窗口独立处理账号、验证码与 token；Oddward 不明文保存密码或验证码，保存账号时凭据以系统保护加密落盘。取消不会启动或写入游戏。").FontSize(12).TextColor(muted).LineHeight(1.6)
		ui.Row(c).FillWidth().Gap(8).Children(func() {
			if s.quietButton(c, "managed-decline", "取消", false).Grow(1).Height(42).Clicked() {
				s.Dispatch(state.Event{Kind: state.Dismiss})
			}
			if s.quietButton(c, "managed-accept", "同意并启动", false).Grow(1).Height(42).Clicked() {
				s.Dispatch(state.Event{Kind: state.ManagedAcceptLaunch})
			}
		})
	})
}

func (s *Shell) managedActionNote(c *ui.Context, presentation state.Presentation) {
	p := s.Model.Managed
	bottom := homeBottom + homeDockHeight + 14
	if presentation.Progress {
		bottom += deckHeight + deckGap
	}
	ui.Column(c.Key("managed-status")).Absolute().Right(dockRight).Bottom(bottom).Width(actionWidth).Gap(6).Children(func() {
		text := p.Message
		color := muted
		if p.Phase == state.ManagedPreparing {
			text = managedBytes(p)
		}
		if p.Error != "" {
			text = p.Error
			color = warning
		}
		if text != "" {
			ui.Text(c, text).FontSize(12).TextColor(color).LineHeight(1.5)
		}
		if s.Model.Login.Phase == state.LoginWaiting {
			s.quietButton(c, "login-reopen", "打开登录窗口", false).Height(28).AlignSelf(ui.End).OnClick(func() {
				s.Dispatch(state.Event{Kind: state.LoginReopen})
			})
		}
		if p.Phase == state.ManagedPreparing || p.Phase == state.ManagedLocating {
			s.quietButton(c, "managed-cancel", "取消准备", false).Height(28).AlignSelf(ui.End).OnClick(func() {
				s.Dispatch(state.Event{Kind: state.ManagedCancel})
			})
		}
	})
}
