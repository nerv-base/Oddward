// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// homeDock is the whole download story on the home page: one capsule that
// starts, updates or pauses the game, and — only while a preload exists — one
// compact chip to its left. There is no permanent panel and no second button
// that repeats what the capsule already says.
func (s *Shell) homeDock(c *ui.Context, primary state.Presentation) {
	m := s.Model
	p := state.PresentDownload(m)
	preload := state.PresentPreload(m)
	dock := ui.Column(c.Key("home-dock")).Absolute().Right(dockRight).Bottom(homeBottom).
		Width(homeDockWidth).AlignItems(ui.End)
	if preload.Visible {
		dock.Width(homeDockWidth + homePreloadChipWidth + homePreloadChipGap)
	}
	hover := dock.Hovered()
	canPeek := m.Overlay == state.NoOverlay && !primary.LocateExisting &&
		(primary.Task || m.Download.Phase != state.DownloadIdle && m.Download.Phase != state.DownloadDone || m.Download.Preload != state.PreloadStateUnavailable)
	if m.HomeAccounts.Open && m.Overlay == state.NoOverlay && c.Shortcut(0, ui.KeyEscape) {
		s.Dispatch(state.Event{Kind: state.HomeAccountsClose})
	}
	dock.Children(func() {
		if canPeek && hover {
			s.homeTransferSummary(c, p)
			ui.Box(c).Height(homeDownloadGap)
		}
		if m.HomeAccounts.Count > 0 && !primary.LocateExisting {
			if m.HomeAccounts.Open && !(canPeek && hover) {
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
		ui.Row(c.Key("home-dock-actions")).Height(homeDockHeight).AlignItems(ui.Center).
			Gap(homePreloadChipGap).Children(func() {
			if preload.Visible {
				s.homePreloadChip(c, preload)
			}
			ui.Row(c.Key("home-dock-row")).Height(homeDockHeight).Radius(homeDockHeight / 2).Clip().
				Material(frostedSurface{Tint: homeDockTint}).Children(func() {
				s.homeDockPrimary(c, primary, p)
				s.homeDockIcon(c, "home-dock-game-settings", "游戏设置", "\uE713", func() {
					s.Dispatch(state.Event{Kind: state.OpenOverlay, Overlay: state.GameSettings})
				})
			})
		})
	})
	if primary.LocateExisting {
		s.locateExistingAction(c)
	}
}

// homePreloadChip is the only preload surface on the home page. It carries its
// own percentage so a running preload never moves into the launch capsule.
func (s *Shell) homePreloadChip(c *ui.Context, p state.PreloadPresentation) {
	b := ui.ButtonBase(c.Key("home-preload")).Label(p.Label).Tooltip(p.Detail).
		Size(homePreloadChipWidth, homeDockHeight).Padding(0, 10).Gap(6).
		AlignItems(ui.Center).Justify(ui.Center).Radius(homeDockHeight/2).
		Border(1, hairline).Background(homeDockTint).TextColor(paper).DimDisabled(true).
		Disabled(!p.Enabled)
	s.homeDockFeedback(b)
	b.Children(func() {
		glyph(c, "\uE896", 13)
		ui.Text(c, p.Label).FontSize(downloadCaptionSize).FontWeight(600).SingleLine().PassThrough()
		if p.ShowBar {
			mono(c, fmt.Sprintf("%d%%", min(max(p.Percent, 0), 100)), 12).TextColor(s.tone())
		}
	})
	if p.Enabled && p.Action != 0 {
		b.OnClick(func() { s.Dispatch(state.Event{Kind: p.Action}) })
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
			case "下载游戏", "更新游戏", "重试下载":
				glyph(c, "\uE896", 14)
			case "启动游戏", "重试启动":
				glyph(c, "\uE768", 14)
			}
			ui.Text(c, p.Primary).FontSize(downloadBodySize).FontWeight(600).SingleLine().PassThrough()
		})
	}
	b.OnClick(func() { s.Dispatch(state.Event{Kind: state.Primary}) })
}

// homeTransferSummary answers the hover question "what is it doing right now".
// Destructive actions stay here; everything else is already on the capsule.
func (s *Shell) homeTransferSummary(c *ui.Context, p state.DownloadPresentation) {
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
		if p.CanCancel {
			ui.Row(c).FillWidth().Justify(ui.End).Children(func() {
				s.quietButton(c, "home-download-cancel", "取消任务", false).Height(downloadButtonHeight).
					Padding(0, 8).Radius(homeDockPopupRadius).OnClick(s.confirmDownloadCancel)
			})
		}
	})
}
