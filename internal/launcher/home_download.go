// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"fmt"
	"time"

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
		AlignItems(ui.End)
	canPeek := m.Overlay == state.NoOverlay && !primary.LocateExisting &&
		(primary.Task || m.Download.Phase != state.DownloadIdle && m.Download.Phase != state.DownloadDone || m.Download.Preload != state.PreloadStateUnavailable)
	if m.HomeAccounts.Open && m.Overlay == state.NoOverlay && c.Shortcut(0, ui.KeyEscape) {
		s.Dispatch(state.Event{Kind: state.HomeAccountsClose})
	}
	_, py, inside := dock.PointerPosition()
	bounds := dock.Bounds()
	onActions := inside && bounds.H > 0 && py >= bounds.H-homeDockHeight
	if !canPeek || m.HomeAccounts.Open || !inside {
		s.transferPeek = false
	} else if onActions {
		s.transferPeek = true
	}
	peeking := canPeek && s.transferPeek
	dock.Children(func() {
		if peeking {
			s.homeTransferSummary(c, p)
			ui.Box(c).Height(homeDownloadGap)
		} else if m.HomeAccounts.Count > 0 && !primary.LocateExisting {
			if m.HomeAccounts.Open {
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
	progress := (p.Task || s.Model.Download.Phase == state.DownloadRebuilding || s.Model.Download.Phase == state.DownloadCommitting) && d.ShowProgress
	eta := ""
	if progress {
		fraction := min(max(float64(d.Downloaded)/float64(d.Total), 0), 1)
		pct := fmt.Sprintf("%.0f%%", fraction*100)
		description := p.Status + " · " + pct
		if d.ShowETA {
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
			case "下载游戏", "更新游戏", "重试下载", "重试更新":
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
// It only displays backend-measured progress, throughput and remaining time,
// and keeps destructive cancellation accessible without dominating the card.
func (s *Shell) homeTransferSummary(c *ui.Context, p state.DownloadPresentation) {
	d := s.Model.Download
	tone := s.tone()
	isWarning := p.SpaceShort || p.Error != "" ||
		d.Phase == state.DownloadError || d.Phase == state.DownloadRecoveryRequired || d.Phase == state.DownloadStale ||
		d.Note == "disk-full" || d.Note == "disk-short" || d.Note == "official-running" || d.Note == "game-running" || d.Note == "login-running" || d.Note == "locked"
	dotColor := tone
	switch {
	case isWarning:
		dotColor = warning
	case d.Phase == state.DownloadSuspended:
		dotColor = muted
	}
	card := ui.Column(c.Key("home-transfer-summary")).Role(ui.RoleGroup).Label("下载详情").
		Width(homeTransferWidth).
		Padding(homeTransferPaddingY, homeDockPopupInset, homeTransferPaddingY, homeDockPopupInset).
		Gap(homeTransferGap).Radius(homeDockPopupRadius).
		Border(1, homeTransferBorder).Material(frostedSurface{Tint: homeTransferTint}).
		Shadow(0, 8, 24, 0, ui.RGBA(0, 0, 0, .28))
	card.Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).Children(func() {
			ui.Box(c).Size(homeTransferDotSize, homeTransferDotSize).Radius(homeTransferDotSize / 2).
				Background(dotColor).PassThrough()
			ui.Text(c, p.StageText).Grow(1).MinWidth(0).FontSize(homeTransferTitleSize).FontWeight(600).
				TextColor(paper).SingleLine()
			if p.CanCancel {
				s.homeTransferCancelButton(c)
			}
		})
		if p.ShowRate || p.ShowETA {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
				if p.ShowRate {
					mono(c, formatRate(p.Rate), homeTransferSpeedSize).FontWeight(600).TextColor(paper)
				}
				ui.Box(c).Grow(1)
				if p.ShowETA {
					ui.Row(c).AlignItems(ui.Center).Gap(3).TextColor(muted).Children(func() {
						glyph(c, "\uE895", 11)
						mono(c, "剩 "+formatDuration(p.ETA), homeTransferMetaSize)
					})
				}
			})
		}
		if p.ShowProgress {
			fraction := min(max(float64(p.Downloaded)/float64(p.Total), 0), 1)
			barValue := float32(fraction)
			if s.motion() {
				barValue = card.AnimateTo("transfer-progress", barValue, ui.Animation{Duration: 180 * time.Millisecond})
			}
			barFill := tone
			if d.Phase == state.DownloadSuspended {
				barFill = muted.Alpha(.72)
			}
			ui.Progress(c.Key("home-transfer-progress"), fraction).Label("当前阶段进度").
				FillWidth().Height(homeTransferBarHeight).MinHeight(homeTransferBarHeight).
				Background(ui.Transparent).PassThrough().Draw(func(pt *ui.Painter, r ui.Rect) {
				pt.Fill(r, hairline, homeTransferBarHeight/2)
				if barValue > 0 {
					pt.Fill(ui.Rect{X: r.X, Y: r.Y, W: max(r.W*barValue, r.H), H: r.H}, barFill, homeTransferBarHeight/2)
				}
			})
			bytes := formatSize(p.Downloaded, p.Total)
			if d.Kind == state.DownloadInstall && d.Stage == state.StageNetwork {
				bytes = installSize(p.Downloaded) + " / " + installSize(p.Total)
			}
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).Children(func() {
				mono(c, bytes, homeTransferMetaSize).TextColor(paper)
				ui.Box(c).Grow(1)
				if (d.Stage == state.StageVerify || d.Stage == state.StageApply || d.Stage == state.StageCommit) && p.FilesTotal > 0 {
					mono(c, fmt.Sprintf("%d / %d 文件", p.FilesDone, p.FilesTotal), homeTransferMetaSize).TextColor(muted)
				}
			})
		}
		if p.NextAction != "" {
			if isWarning {
				ui.Box(c).FillWidth().Padding(5, 8).Radius(7).
					Background(homeTransferNoteFill).Border(1, warning.Alpha(.28)).Children(func() {
					ui.Text(c, p.NextAction).FontSize(homeTransferMetaSize).MaxLines(2).TextColor(warning)
				})
			} else {
				ui.Text(c, p.NextAction).FontSize(homeTransferMetaSize).MaxLines(2).TextColor(muted)
			}
		}
	})
}

func (s *Shell) homeTransferCancelButton(c *ui.Context) {
	b := ui.ButtonBase(c.Key("home-download-cancel")).Label("取消任务").Tooltip("取消当前任务").
		Height(homeTransferCancelHeight).Padding(0, 7).Radius(homeTransferCancelRadius).
		Gap(3).AlignItems(ui.Center).Justify(ui.Center).TextColor(muted)
	fill := homeTransferCancelFill
	switch {
	case b.Pressed():
		fill = homeTransferCancelPress
		b.TextColor(homeTransferCancelText)
	case b.Hovered() || b.FocusVisible():
		fill = homeTransferCancelHover
		b.TextColor(homeTransferCancelText)
	}
	s.controlFill(b, fill)
	if b.FocusVisible() {
		b.Border(1, s.tone())
	}
	b.Children(func() {
		glyph(c, "\uE8BB", 9)
		ui.Text(c, "取消任务").FontSize(homeTransferMetaSize).SingleLine().PassThrough()
	}).OnClick(s.confirmDownloadCancel)
}
