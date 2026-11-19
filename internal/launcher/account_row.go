// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"github.com/egoist/mygo/ui"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// homeAccountRow sits just above the download capsule. It only lists accounts
// the login window already saved; adding one still means starting the game.
func (s *Shell) homeAccountRow(c *ui.Context, current state.SavedAccount) ui.Element {
	b := ui.ButtonBase(c.Key("home-account-row")).Label("当前账号 "+current.Label).
		Tooltip("切换下次使用的账号").Height(28).Padding(0, 12).Radius(14).
		Material(frostedSurface{Tint: homeDockTint}).Children(func() {
		ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).PassThrough().Children(func() {
			glyph(c, "\uE77B", 12)
			ui.Text(c, current.Label).FontSize(downloadCaptionSize).SingleLine()
			glyph(c, "\uE70D", 10)
		})
	}).OnClick(func() {
		s.Dispatch(state.Event{Kind: state.HomeAccountsToggle})
	})
	if b.Hovered() || b.Pressed() {
		b.Background(hairline)
	}
	if b.FocusVisible() {
		b.Border(1, s.tone())
	}
	return b
}

// homeAccountsPopup lists every remembered account. The check marks the one the
// next login will propose; switching only writes the choice, never a session.
func (s *Shell) homeAccountsPopup(c *ui.Context) ui.Element {
	m := s.Model
	rows := int(m.HomeAccounts.Count)
	visible := rows
	if visible > 6 {
		visible = 6
	}
	box := ui.Column(c.Key("home-account-popup")).Width(homeDockWidth).Padding(8).Gap(2).
		Radius(homeDockPopupRadius).Background(homeDockTint).Border(1, hairline).
		Role(ui.RoleGroup).Label("已保存的账号")
	box.Children(func() {
		ui.Text(c, "下次使用的账号").FontSize(downloadCaptionSize).TextColor(muted).
			Padding(2, 6).SingleLine()
		ui.Scroll(c.Key("home-account-list")).Height(float32(visible * 30)).Width(homeDockWidth - 16).
			Scrollbars(rows > 6).Children(func() {
			for i := 0; i < rows; i++ {
				index, account := i, m.HomeAccounts.Accounts[i]
				rowKey := "home-account-" + account.ID
				row := ui.ButtonBase(c.Key(rowKey)).Label(account.Label).Height(30).
					Padding(0, 6).Radius(6).AlignItems(ui.Center)
				row.Children(func() {
					ui.Row(c).FillWidth().Gap(6).AlignItems(ui.Center).PassThrough().Children(func() {
						ui.Text(c, account.Label).FontSize(downloadCaptionSize).SingleLine().Grow(1).TextAlign(ui.Start)
						if account.ID == m.HomeAccounts.Current {
							glyph(c, "\uE73E", 12)
						}
					})
				})
				if row.Hovered() || row.FocusVisible() {
					row.Background(hairline)
				}
				row.OnClick(func() {
					s.Dispatch(state.Event{Kind: state.HomeAccountUse, Index: uint8(index)})
				})
			}
		})
		if m.HomeAccounts.Error != "" {
			ui.Text(c, m.HomeAccounts.Error).FontSize(downloadCaptionSize-1).TextColor(warning).
				Padding(2, 6).Wrap()
		}
		ui.Text(c, "添加账号请在登录窗口中完成").FontSize(downloadCaptionSize-1).TextColor(muted).
			Padding(2, 6).SingleLine()
	})
	return box
}
