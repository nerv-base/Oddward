// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"time"

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
// tiles keep native labels, tooltips, current-page semantics and focus; a
// tone pill at the rail edge travels to the current page.
func (s *Shell) navigation(c *ui.Context) {
	items := []struct {
		key, label, icon string
		page             state.Page
	}{
		{"home", "游戏主页", "\uE80F", state.Home},
		{"wish-analysis", "抽卡分析", "\uE9D9", state.WishAnalysis},
		{"check-in", "自动签到", "\uE787", state.CheckIn},
	}
	current := 0
	for i, item := range items {
		if s.Model.Page == item.page {
			current = i
		}
	}
	tone := s.tone()
	ui.Box(c.Key("rail")).Absolute().Left(0).Top(0).Bottom(0).Width(railWidth).Background(bezel).Children(func() {
		s.railMark(c)
		y := tileTop + float32(current)*(tileSize+tileGap) + 12
		bar := ui.Box(c.Key("rail-indicator")).Absolute().Left(1).Width(3).Height(tileSize - 24).Radius(1.5).Background(tone).PassThrough()
		if s.motion() {
			y = bar.AnimateTo("y", y, ui.Animation{Duration: 260 * time.Millisecond})
		}
		bar.Top(y)
		ui.Column(c).Absolute().Left((railWidth - tileSize) / 2).Top(tileTop).Gap(tileGap).Children(func() {
			for _, item := range items {
				item := item
				selected := s.Model.Page == item.page
				b := ui.ButtonBase(c.Key(item.key)).Label(item.label).Tooltip(item.label).
					Size(tileSize, tileSize).Radius(12).Justify(ui.Center).TextColor(muted)
				fill := ui.Transparent
				if selected {
					fill = ui.RGBA(255, 255, 255, .10)
					b.TextColor(paper).Current(ui.CurrentPage).Bind(&s.navigationFocus)
				}
				if b.Hovered() {
					fill = ui.RGBA(255, 255, 255, .08)
					b.TextColor(paper)
				}
				if b.Pressed() {
					fill = ui.RGBA(255, 255, 255, .14)
				}
				s.controlFill(b, fill)
				b.Children(func() { glyph(c, item.icon, 20) }).OnClick(func() {
					s.Dispatch(state.Event{Kind: state.Navigate, Page: item.page})
				})
			}
		})
		settings := ui.ButtonBase(c.Key("settings")).Label("设置").Tooltip("设置").Absolute().Left((railWidth-tileSize)/2).Bottom(16).
			Size(tileSize, tileSize).Radius(12).Justify(ui.Center).TextColor(muted)
		fill := ui.Transparent
		if settings.Hovered() {
			fill = ui.RGBA(255, 255, 255, .08)
			settings.TextColor(paper)
		}
		if settings.Pressed() {
			fill = ui.RGBA(255, 255, 255, .14)
		}
		s.controlFill(settings, fill)
		settings.Children(func() { glyph(c, "\uE713", 20) }).OnClick(func() {
			s.Dispatch(state.Event{Kind: state.OpenOverlay, Overlay: state.Settings})
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
