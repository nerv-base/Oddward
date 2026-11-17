// SPDX-License-Identifier: GPL-3.0-or-later
package launcher

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/nerv-base/Oddward/internal/launcher/state"
	"time"
)

const (
	wishLinkWidth = 520
	wishLinkInset = 28
	wishLinkGap   = 16
	// wishRoleRowHeight is the height of one selectable role row; the list is
	// only as tall as its content so a short list does not leave a hole.
	wishRoleRowHeight = 48
	wishRoleListMax   = 220
)

func wishRoleListHeight(roles int) float32 {
	if roles == 0 {
		return 32
	}
	h := float32(roles) * wishRoleRowHeight
	return min(h, wishRoleListMax)
}

func (s *Shell) wishLinkOverlay(c *ui.Context) {
	l := s.Model.Wish.Link
	if l.Phase != state.WishLinking {
		return
	}
	ui.Overlay(c, func() {
		back := ui.Box(c.Key("wish-link-backdrop")).Absolute().Left(0).Top(0).Right(0).Bottom(0).Background(ui.RGBA(6, 7, 10, .72)).Center().Modal()
		if p := c.Preferences(); p.HighContrast || p.ReduceTransparency {
			back.Background(ink)
		}
		back.Children(func() {
			ui.Column(c.Key("wish-link-dialog")).Width(wishLinkWidth).Padding(wishLinkInset).Gap(wishLinkGap).Radius(24).Background(ui.Hex("#181A20")).Border(1, hairline).TextColor(paper).Role(ui.RoleDialog).Label("关联塔吉多").Children(func() {
				ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
					ui.Column(c).Grow(1).Gap(6).Children(func() {
						mono(c, fmt.Sprintf("TAJIDUO / 0%d", int(l.Step)+1), 10).TextColor(s.tone())
						ui.Text(c, "关联塔吉多").FontSize(24).FontWeight(700)
					})
					s.quietButton(c, "wish-link-cancel", "取消", false).Role(ui.RoleButton).OnClick(func() { s.Dispatch(state.Event{Kind: state.WishLinkCancel}) })
				})
				switch l.Step {
				case state.WishLinkNotice:
					ui.Text(c, "关联须知").FontSize(17).FontWeight(600)
					ui.Text(c, "通过短信登录塔吉多会顶掉手机上原有的登录。手机号尚未注册时，平台会自动创建账号。").FontSize(14).LineHeight(1.6)
					ui.Text(c, "Oddward 是非官方工具。仅保存加密的续期凭据，不保存短信验证码；使用前请在官方客户端阅读《用户协议》和《隐私政策》。").FontSize(13).TextColor(muted).LineHeight(1.6)
					ui.Text(c, "记录仅在你点击更新时获取；解除关联后，本地档案仍会保留。").FontSize(13).TextColor(muted).LineHeight(1.6)
					ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
						caption := ui.Text(c, "我已阅读并同意").FontSize(14).Grow(1)
						s.preferenceSwitch(c, "wish-link-agree", caption, l.Agreed, false, state.WishLinkAgree)
					})
					s.accentButton(c, "wish-link-continue", "继续").Role(ui.RoleButton).Disabled(!l.Agreed).OnClick(func() { s.Dispatch(state.Event{Kind: state.WishLinkContinue}) })
				case state.WishLinkPhone:
					ui.Text(c, "使用已保存游戏账号对应的手机号").FontSize(14).TextColor(muted)
					ui.TextInput(c.Key("wish-link-phone"), &s.wishLink.phone).Label("塔吉多手机号").Role(ui.RoleTextField).Placeholder("手机号").FillWidth().Height(44).FontSize(15).Background(ink).TextColor(paper).Disabled(l.Busy)
					s.wishLink.phone = digitsOnly(s.wishLink.phone, 11)
					left := state.WishLinkCooldown(l.SMSUntil, s.now)
					label := "发送验证码"
					if left > 0 {
						label = fmt.Sprintf("%d 秒后重发", left)
					}
					ui.Row(c).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
						ui.TextInput(c.Key("wish-link-code"), &s.wishLink.code).Label("塔吉多验证码").Role(ui.RoleTextField).Placeholder("短信验证码").Grow(1).Height(44).FontSize(15).Background(ink).TextColor(paper).Disabled(l.Busy)
						s.quietButton(c, "wish-link-sms", label, l.Busy || left > 0 || !mainlandPhone(s.wishLink.phone)).Role(ui.RoleButton).OnClick(func() { s.Dispatch(state.Event{Kind: state.WishLinkSendSMS, Now: s.now}) })
					})
					s.wishLink.code = digitsOnly(s.wishLink.code, 6)
					if l.CodeSent {
						ui.Text(c, "验证码已发送").FontSize(12).TextColor(muted)
					}
					s.accentButton(c, "wish-link-login", "登录并读取角色").Role(ui.RoleButton).Disabled(l.Busy || !mainlandPhone(s.wishLink.phone) || len(s.wishLink.code) < 4).OnClick(func() { s.Dispatch(state.Event{Kind: state.WishLinkSubmit}) })
					if left > 0 {
						c.After(time.Second)
					}
				case state.WishLinkRole:
					ui.Text(c, "选择游戏角色").FontSize(17).FontWeight(600)
					ui.Text(c, "已绑定角色可以直接选择；其他角色仅在点击「绑定」后提交到塔吉多。").FontSize(13).TextColor(muted).LineHeight(1.6)
					ui.Scroll(c.Key("wish-link-roles")).FillWidth().Height(wishRoleListHeight(len(s.wishLink.roles))).Scrollbars(true).Children(func() {
						if len(s.wishLink.roles) == 0 {
							ui.Text(c, "暂无可选角色，请先在官方客户端创建或绑定角色。").FontSize(13).TextColor(muted)
						}
						for _, r := range s.wishLink.roles {
							ui.Row(c.Key("wish-role-" + r.ID)).FillWidth().Gap(12).AlignItems(ui.Center).Children(func() {
								ui.Column(c).Grow(1).Gap(4).Children(func() { ui.Text(c, r.Name).FontSize(15); mono(c, r.ID, 11).TextColor(muted) })
								label, kind, accessible := "绑定", state.WishLinkBind, "绑定 "+r.Name
								if r.Bound {
									label, kind, accessible = "选择已绑定角色", state.WishLinkSelectBound, "选择已绑定角色 "+r.Name
								}
								s.quietButton(c, "wish-role-pick-"+r.ID, label, l.Busy).Label(accessible).Role(ui.RoleButton).OnClick(func() { s.Dispatch(state.Event{Kind: kind, WishRoleID: r.ID}) })
							})
						}
					})
				}
				if l.Busy {
					ui.Text(c, "正在处理…").FontSize(13).TextColor(muted)
				}
				if l.Failure != state.WishLinkFailureNone {
					ui.Text(c, wishLinkFailureText(l.Failure)).FontSize(13).TextColor(paper).LineHeight(1.5)
				}
			})
		})
		if back.OverlayShortcut(0, ui.KeyEscape) {
			s.Dispatch(state.Event{Kind: state.WishLinkCancel})
		}
	})
}
