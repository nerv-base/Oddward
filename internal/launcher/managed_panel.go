// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"path/filepath"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func (s *Shell) managedLocationPanel(c *ui.Context) {
	p := s.Model.Managed
	locked := state.ManagedBusy(p.Phase) || state.ManagedActive(p.Phase)
	ui.Column(c.Key("managed-location")).Grow(1).MinHeight(0).FillWidth().Gap(14).Children(func() {
		ui.Text(c, "选择已有的异环游戏").FontSize(17).FontWeight(600)
		ui.Text(c, "只读检查已有安装，不移动、不修复、不重新下载游戏。").FontSize(13).TextColor(muted).LineHeight(1.5)
		ui.Text(c, "可选择游戏根目录、Client、WindowsNoEditor，或粘贴 HTGame.exe 的完整路径。").FontSize(12).TextColor(muted).LineHeight(1.5)
		ui.TextInput(c.Key("managed-game-path"), &s.ManagedPathInput).Label("游戏目录").FillWidth().Height(40).Padding(9, 10).Radius(10).Border(1, hairline).Background(ui.RGBA(255, 255, 255, .05)).TextColor(paper).FontSize(13).Placeholder("选择或粘贴已有游戏目录").ReadOnly(locked).Disabled(locked)
		if s.quietButton(c, "managed-browse", "浏览游戏目录", locked).FillWidth().Height(42).Clicked() {
			s.Dispatch(state.Event{Kind: state.ManagedBrowse})
		}
		if p.Root != "" {
			ui.Text(c, "当前目录："+p.Root).FontSize(12).TextColor(muted).LineHeight(1.5)
		}
		if p.Phase == state.ManagedLocating {
			ui.Text(c, "正在只读检查所选目录…").FontSize(13).TextColor(paper)
		}
		if p.Error != "" {
			ui.Text(c, p.Error).FontSize(12).TextColor(warning).LineHeight(1.5)
		}
		ui.Box(c).Grow(1)
		ui.Row(c).FillWidth().Gap(8).Children(func() {
			if s.quietButton(c, "managed-location-back", "返回", false).Grow(1).Height(40).Clicked() {
				s.Dispatch(state.Event{Kind: state.Dismiss})
			}
			if s.quietButton(c, "managed-adopt", "定位此目录", locked || strings.TrimSpace(s.ManagedPathInput) == "").Grow(1).Height(40).Clicked() {
				s.Dispatch(state.Event{Kind: state.ManagedSelectPath, Path: strings.TrimSpace(s.ManagedPathInput)})
			}
		})
		ui.Text(c, "结构检查不等于版本完整性或登录兼容性验证。仅保存目录记录。").FontSize(11).TextColor(muted).LineHeight(1.5)
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

func (s *Shell) managedActionNote(c *ui.Context) {
	p := s.Model.Managed
	bottom := float32(homeBottom + actionHeight + 14)
	if state.Present(s.Model).Progress {
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
		ui.Row(c).FillWidth().Gap(6).Children(func() {
			label := "尚未定位游戏"
			if p.Root != "" {
				label = "已定位 · " + filepath.Base(p.Root)
			}
			ui.Text(c, label).Tooltip(p.Root).FontSize(11).TextColor(muted).Grow(1).SingleLine()
			if s.Model.Login.Phase == state.LoginWaiting {
				if s.quietButton(c, "login-reopen", "打开登录窗口", false).Height(28).Clicked() {
					s.Dispatch(state.Event{Kind: state.LoginReopen})
				}
			}
			if p.Phase == state.ManagedPreparing || p.Phase == state.ManagedLocating {
				if s.quietButton(c, "managed-cancel", "取消准备", false).Height(28).Clicked() {
					s.Dispatch(state.Event{Kind: state.ManagedCancel})
				}
			} else if s.quietButton(c, "managed-location-menu", "游戏目录", state.ManagedActive(p.Phase)).Height(28).Clicked() {
				s.ManagedPathInput = p.Root
				s.Dispatch(state.Event{Kind: state.OpenOverlay, Overlay: state.LocateGame})
			}
		})
	})
}
