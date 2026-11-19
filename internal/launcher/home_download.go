// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func (s *Shell) homeDock(c *ui.Context, primary state.Presentation) {
	m := s.Model
	p := state.PresentDownload(m)
	dock := ui.Column(c.Key("home-dock")).Absolute().Right(dockRight).Bottom(homeBottom).
		Width(homeDockWidth).AlignItems(ui.End)
	hover := dock.Hovered()
	pinned := ui.Local(dock, "pinned", func() bool { return false })
	dismissed := ui.Local(dock, "dismissed", func() bool { return false })
	opener := ui.Local(dock, "details-opener", func() ui.Handle { return ui.Handle{} })
	firstAction := ui.Local(dock, "details-first-action", func() ui.Handle { return ui.Handle{} })
	if !hover {
		*dismissed = false
	}
	canPeek := m.Overlay == state.NoOverlay && !m.HomeDownloadsOpen && !primary.LocateExisting &&
		(primary.Task || m.Download.Phase != state.DownloadIdle && m.Download.Phase != state.DownloadDone || m.Download.Preload != state.PreloadStateUnavailable)
	if !canPeek {
		*pinned = false
	}
	showDetails := canPeek && (*pinned || hover && !*dismissed)
	if m.HomeAccounts.Open && m.Overlay == state.NoOverlay && c.Shortcut(0, ui.KeyEscape) {
		s.Dispatch(state.Event{Kind: state.HomeAccountsClose})
	} else if showDetails && c.Shortcut(0, ui.KeyEscape) {
		if *pinned {
			opener.Focus()
		}
		*pinned, *dismissed, showDetails = false, true, false
	} else if m.HomeDownloadsOpen && m.Overlay == state.NoOverlay && c.Shortcut(0, ui.KeyEscape) {
		s.Dispatch(state.Event{Kind: state.CloseHomeDownloads})
		*dismissed = true
	}
	dock.Children(func() {
		if showDetails {
			s.homeTransferSummary(c, p, firstAction)
			ui.Box(c).Height(homeDownloadGap)
		} else if p.OfferVersion != "" && primary.Primary == "更新游戏" && !m.HomeDownloadsOpen {
			ui.Text(c, "可用正式版本 "+p.OfferVersion).FontSize(downloadCaptionSize).SingleLine().Padding(4, 10).
				Margin(0, 0, homeDownloadGap, 0).Radius(homeDockPopupRadius).Material(frostedSurface{Tint: homeDockTint})
		}
		if m.HomeAccounts.Count > 0 && !primary.LocateExisting {
			if m.HomeAccounts.Open && !showDetails {
				s.homeAccountsPopup(c)
				ui.Box(c).Height(homeDownloadGap)
			}
			current := m.HomeAccounts.Accounts[0]
			for i := 0; i < int(m.HomeAccounts.Count); i++ {
				if m.HomeAccounts.Accounts[i].ID == m.HomeAccounts.Current {
					current = m.HomeAccounts.Accounts[i]
				}
			}
			s.homeAccountRow(c, current)
			ui.Box(c).Height(homeDownloadGap + 4)
		}
		ui.Row(c.Key("home-dock-row")).Height(homeDockHeight).Radius(homeDockHeight / 2).Clip().
			Material(frostedSurface{Tint: homeDockTint}).Children(func() {
			if !primary.LocateExisting {
				label, icon := "游戏下载与更新", "\uE896"
				if canPeek {
					label, icon = "查看下载详情", "\uE70E"
					if *pinned {
						label, icon = "收起下载详情", "\uE70D"
					}
				} else if m.HomeDownloadsOpen {
					label, icon = "收起游戏下载与更新", "\uE70D"
				}
				s.homeDockIcon(c, "home-dock-details", label, icon, func() {
					if canPeek {
						*pinned, *dismissed = !*pinned, *pinned
						if *pinned {
							firstAction.Focus()
						}
						return
					}
					event := state.OpenHomeDownloads
					if m.HomeDownloadsOpen {
						event = state.CloseHomeDownloads
						*dismissed = true
					}
					s.Dispatch(state.Event{Kind: event})
				}).Expanded(showDetails || m.HomeDownloadsOpen).Bind(opener)
			}
			s.homeDockPrimary(c, primary, p)
			s.homeDockIcon(c, "home-dock-game-settings", "游戏设置", "\uE713", func() {
				s.Dispatch(state.Event{Kind: state.OpenOverlay, Overlay: state.GameSettings})
			})
		})
	})
	if primary.LocateExisting && !m.HomeDownloadsOpen {
		s.locateExistingAction(c)
	}
}

func (s *Shell) homeDockIcon(c *ui.Context, key, label, icon string, act func()) ui.Element {
	b := ui.ButtonBase(c.Key(key)).Label(label).Tooltip(label).Size(homeDockHeight, homeDockHeight).
		AlignItems(ui.Center).Justify(ui.Center).Radius(homeDockHeight / 2)
	s.homeDockFeedback(b)
	return b.Children(func() { glyph(c, icon, gameSettingsIconSize) }).OnClick(act)
}

func (s *Shell) homeDockFeedback(b ui.Element) {
	if b.Pressed() || b.Hovered() {
		b.Background(hairline)
	}
	if b.FocusVisible() {
		b.Border(1, s.tone())
	}
}

func (s *Shell) homeDockPrimary(c *ui.Context, p state.Presentation, d state.DownloadPresentation) {
	b := ui.ButtonBase(c.Key("home-dock-primary")).Label(p.Primary).Size(homeDockPrimaryWidth, homeDockHeight).
		Padding(0, 6).Gap(6).AlignItems(ui.Center).Justify(ui.Center).DimDisabled(false).Disabled(!p.Enabled).
		Radius(homeDockHeight / 2)
	if p.Detail != "" {
		b.Tooltip(p.Detail)
	}
	s.homeDockFeedback(b)
	if p.Enabled && !p.Task {
		fill := s.tone()
		if b.Pressed() {
			fill = fill.Mix(ink, .12)
		} else if b.Hovered() || b.FocusVisible() {
			fill = fill.Mix(paper, .16)
		}
		b.Background(fill).TextColor(ink)
		if b.FocusVisible() {
			b.Border(1, ink)
		}
	}
	progress := p.Task && d.ShowProgress
	network := progress && s.Model.Download.Stage == state.StageNetwork
	eta := ""
	if progress {
		fraction := min(max(float64(d.Downloaded)/float64(d.Total), 0), 1)
		pct := fmt.Sprintf("%.0f%%", fraction*100)
		description := p.Status + " · " + pct
		if network && d.ShowETA {
			eta = formatDuration(d.ETA)
			description += " · 预计剩余 " + eta
		}
		b.Description(description)
		action := ""
		switch p.Primary {
		case "暂停下载":
			action = "暂停"
		case "继续下载":
			action = "继续"
		}
		showAction := action != "" && (b.Hovered() || b.FocusVisible())
		b.Children(func() {
			ui.Row(c).Size(homeDockBadgeSize, homeDockBadgeSize).Radius(homeDockBadgeSize/2).Border(1, s.tone()).
				AlignItems(ui.Center).Justify(ui.Center).PassThrough().Children(func() {
				mono(c, pct, 11).TextColor(s.tone())
			})
			ui.Column(c).Grow(1).PassThrough().Children(func() {
				if showAction {
					ui.Text(c, action).FontSize(downloadBodySize).FontWeight(600).SingleLine()
					return
				}
				ui.Text(c, p.Status).FontSize(13).SingleLine()
				if eta != "" {
					ui.Row(c).AlignItems(ui.Center).Gap(3).TextColor(muted).PassThrough().Children(func() {
						glyph(c, "\uE895", 11)
						mono(c, eta, 11)
					})
				}
			})
			switch p.Primary {
			case "暂停下载":
				glyph(c, "\uE769", 13)
			case "继续下载":
				glyph(c, "\uE768", 13)
			}
		})
	} else if p.Primary == "游戏运行中" && !s.Model.Download.GameSince.IsZero() {
		elapsed := c.Now().Sub(s.Model.Download.GameSince)
		if elapsed < 0 {
			elapsed = 0
		}
		b.Children(func() {
			glyph(c, "\uE768", 14)
			ui.Column(c).PassThrough().Children(func() {
				ui.Text(c, p.Primary).FontSize(13).FontWeight(600).SingleLine()
				mono(c, formatDuration(int64(elapsed.Seconds())), 11).TextColor(muted)
			})
		})
	} else {
		b.Children(func() {
			switch p.Primary {
			case "下载游戏", "更新游戏":
				glyph(c, "\uE896", 14)
			case "启动游戏", "重试启动":
				glyph(c, "\uE768", 14)
			}
			ui.Text(c, p.Primary).FontSize(downloadBodySize).FontWeight(600).SingleLine().PassThrough()
		})
	}
	b.OnClick(func() { s.Dispatch(state.Event{Kind: state.Primary}) })
}

func (s *Shell) homeTransferSummary(c *ui.Context, p state.DownloadPresentation, firstAction *ui.Handle) {
	ui.Column(c.Key("home-transfer-summary")).Role(ui.RoleGroup).Label("下载详情").FillWidth().
		Padding(8, homeDockPopupInset, 4, homeDockPopupInset).Gap(4).Radius(homeDockPopupRadius).
		Material(frostedSurface{Tint: homeDockTint}).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Text(c, p.StageText).Grow(1).FontSize(downloadBodySize).FontWeight(600).SingleLine()
			if p.ShowETA {
				glyph(c, "\uE895", 12)
				mono(c, formatDuration(p.ETA), downloadCaptionSize).TextColor(muted)
			}
		})
		ui.Box(c).FillWidth().Height(1).Background(hairline).PassThrough()
		if s.Model.Download.Stage == state.StageNetwork && p.ShowProgress {
			bytes := formatSize(p.Downloaded, p.Total)
			if s.Model.Download.Kind == state.DownloadInstall {
				bytes = installSize(p.Downloaded) + " / " + installSize(p.Total)
			}
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(4).Children(func() {
				ui.Text(c, bytes).Grow(1).FontSize(downloadCaptionSize).SingleLine()
				if p.ShowRate {
					ui.Text(c, formatRate(p.Rate)).FontSize(downloadCaptionSize).SingleLine()
				}
			})
		}
		if p.NextAction != "" {
			ui.Text(c, p.NextAction).FontSize(downloadCaptionSize).MaxLines(2).TextColor(muted)
		}
		ui.Row(c).FillWidth().Justify(ui.End).Gap(4).Children(func() {
			if p.CanCancel {
				s.quietButton(c, "home-download-cancel", "取消任务", false).Height(downloadButtonHeight).
					Padding(0, 8).Radius(homeDockPopupRadius).Bind(firstAction).OnClick(s.confirmDownloadCancel)
			}
			more := s.quietButton(c, "home-download-more", "更多下载选项", false).Height(downloadButtonHeight).
				Padding(0, 8).Radius(homeDockPopupRadius).OnClick(func() { s.Dispatch(state.Event{Kind: state.OpenHomeDownloads}) })
			if !p.CanCancel {
				more.Bind(firstAction)
			}
		})
	})
}
