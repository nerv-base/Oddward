// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/branding"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

const (
	railWidth float32 = 64
	tileSize  float32 = 44
	tileGap   float32 = 8
	tileTop   float32 = 92
)

// The rail is part of the bezel: solid, flush-left and full height. Page
// tiles are icons only; native labels, tooltips, current-page semantics and
// focus carry the meaning without permanent captions.
func (s *Shell) navigation(c *ui.Context) {
	items := []struct {
		key, label, icon string
		page             state.Page
	}{
		{"home", "游戏主页", "\uE80F", state.Home},
		{"wish-analysis", "抽卡分析", "\uE9D9", state.WishAnalysis},
		{"check-in", "每日签到", "\uE787", state.CheckIn},
		{"game-data", "游戏数据", "\uE8A5", state.GameData},
	}
	settingsSelected := s.Model.Page == state.LauncherSettings
	ui.Box(c.Key("rail")).Absolute().Left(0).Top(0).Bottom(0).Width(railWidth).Background(bezel).Children(func() {
		s.railMark(c)
		ui.Column(c).Absolute().Left((railWidth - tileSize) / 2).Top(tileTop).Gap(tileGap).Children(func() {
			for _, item := range items {
				item := item
				selected := s.Model.Page == item.page
				b := ui.ButtonBase(c.Key(item.key)).Label(item.label).Tooltip(item.label).
					Size(tileSize, tileSize).Radius(12).Justify(ui.Center).TextColor(muted)
				fill := ui.Transparent
				color := muted
				if selected {
					fill = ui.RGBA(255, 255, 255, .10)
					color = paper
					b.TextColor(paper).Current(ui.CurrentPage).Bind(&s.navigationFocus)
				}
				if b.Hovered() {
					fill = ui.RGBA(255, 255, 255, .08)
					color = paper
					b.TextColor(paper)
				}
				if b.Pressed() {
					fill = ui.RGBA(255, 255, 255, .14)
				}
				s.controlFill(b, fill)
				b.Children(func() {
					if icon := launcherIcons[item.icon]; icon != nil {
						ui.Icon(c, icon).FontSize(17).TextColor(color)
					}
				}).OnClick(func() {
					s.Dispatch(state.Event{Kind: state.Navigate, Page: item.page})
				})
			}
		})
		settings := ui.ButtonBase(c.Key("settings")).Label("启动器设置").Tooltip("启动器设置").Absolute().Left((railWidth-tileSize)/2).Bottom(16).
			Size(tileSize, tileSize).Radius(12).Justify(ui.Center).TextColor(muted)
		fill := ui.Transparent
		if settingsSelected {
			fill = ui.RGBA(255, 255, 255, .10)
			settings.TextColor(paper).Current(ui.CurrentPage).Bind(&s.navigationFocus)
		}
		if settings.Hovered() {
			fill = ui.RGBA(255, 255, 255, .08)
			settings.TextColor(paper)
		}
		if settings.Pressed() {
			fill = ui.RGBA(255, 255, 255, .14)
		}
		s.controlFill(settings, fill)
		settings.Children(func() {
			glyph(c, "\uE713", 18)
		}).OnClick(func() {
			s.Dispatch(state.Event{Kind: state.Navigate, Page: state.LauncherSettings})
		})
	})
}

var brandSymbol = ui.MustParseSVG(branding.SymbolSVG)

// railMark shares the application identity with the native icon and login UI.
func (s *Shell) railMark(c *ui.Context) {
	ui.Box(c.Key("rail-mark")).Absolute().Left(0).Top(22).Width(railWidth).Height(48).PassThrough().DragWindow().Children(func() {
		ui.Icon(c, brandSymbol).Absolute().Left((railWidth-brandMarkSize)/2).Top(0).
			Size(brandMarkSize, brandMarkSize).TextColor(paper).Label("Oddward 标志").PassThrough()
	}).Draw(func(p *ui.Painter, r ui.Rect) {
		cx := r.X + r.W/2
		p.Line(cx-10, r.Y+44, cx+10, r.Y+44, 1, ui.RGBA(255, 255, 255, .14))
	})
}
