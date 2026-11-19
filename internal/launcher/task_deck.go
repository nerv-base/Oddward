// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// transfer returns the figures the active backend reported. Normal mode reads
// the managed component transfer, which reports bytes but no rate.
func (s *Shell) transfer() state.Transfer {
	if s.Model.NormalMode {
		return state.Transfer{Downloaded: s.Model.Managed.Downloaded, Total: s.Model.Managed.Total}
	}
	return s.Model.Transfer
}

// taskDeck stacks a glass card on the launch dock while a task runs, so the
// progress bar, percentage, rate, time left and size get their own room and
// the pill keeps one clean caption. It never estimates what was not reported.
func (s *Shell) taskDeck(c *ui.Context, p state.Presentation) {
	if !p.Progress {
		return
	}
	tone := s.tone()
	value := float32(min(max(s.Model.Progress, 0), 1))
	x := s.transfer()
	paused := s.Model.Flow == state.Paused
	title := "下载进度"
	switch {
	case s.Model.NormalMode:
		title = "组件准备"
	case s.Model.Flow == state.Verifying:
		title = "校验进度"
	}
	fill := tone
	if paused {
		fill = tone.Mix(ui.Hex("#8A8E97"), .55)
	}
	height := actionHeight
	if s.Model.NormalMode {
		height = homeDockHeight
	}
	deck := ui.Column(c.Key("task-deck")).Absolute().Right(dockRight).Bottom(homeBottom+height+deckGap).Size(dockWidth, deckHeight).
		Padding(14, 18, 14, 18).Gap(10).Radius(18).Clip().Border(1, ui.RGBA(255, 255, 255, .10)).Material(frostedSurface{Tint: glassInk})
	if !s.Model.ReducedMotion && !s.Model.SystemReducedMotion {
		value = deck.AnimateTo("deck-value", value, ui.Animation{Duration: 240 * time.Millisecond})
	}
	deck.Children(func() {
		ui.Row(c).FillWidth().Height(26).Gap(8).Children(func() {
			ui.Box(c).Size(6, 6).Radius(3).Background(fill)
			ui.Text(c, title).FontSize(12).FontWeight(600).TextColor(muted).Grow(1)
			mono(c, fmt.Sprintf("%.0f%%", s.Model.Progress*100), 22).FontWeight(700).TextColor(paper)
		})
		// A semantic sibling avoids ButtonBase flattening its accessibility.
		ui.Progress(c.Key("task-progress"), s.Model.Progress).Label("任务进度").FillWidth().Height(4).MinHeight(4).
			Background(ui.Transparent).PassThrough().Draw(func(pt *ui.Painter, r ui.Rect) {
			pt.Fill(r, ui.RGBA(255, 255, 255, .12), 2)
			if value > 0 {
				pt.Fill(ui.Rect{X: r.X, Y: r.Y, W: max(r.W*value, r.H), H: r.H}, fill, 2)
			}
		})
		ui.Row(c.Key("task-figures")).FillWidth().Height(16).Gap(12).Children(func() {
			figures := transferFigures(s.Model.Flow, x)
			if len(figures) == 0 {
				ui.Text(c, p.Detail).FontSize(11).TextColor(muted).SingleLine().Grow(1)
				return
			}
			for i, f := range figures {
				if i > 0 {
					ui.Box(c).Grow(1)
				}
				mono(c, f, 11).TextColor(muted)
			}
		})
	})
}

// transferFigures formats only reported values: the live rate and time left
// need a measured rate, the size needs a reported total.
func transferFigures(flow state.Flow, x state.Transfer) []string {
	var out []string
	if x.Total <= 0 {
		return out
	}
	switch {
	case flow == state.Paused:
		out = append(out, "已暂停")
	case x.Rate > 0:
		out = append(out, formatRate(x.Rate))
		if flow == state.Downloading {
			out = append(out, "剩余 "+formatDuration((x.Total-x.Downloaded)/x.Rate))
		}
	}
	return append(out, formatSize(x.Downloaded, x.Total))
}

func formatRate(rate int64) string {
	if rate >= 1<<30 {
		return fmt.Sprintf("%.1f GB/s", float64(rate)/(1<<30))
	}
	if rate >= 1<<20 {
		return fmt.Sprintf("%.1f MB/s", float64(rate)/(1<<20))
	}
	return fmt.Sprintf("%.0f KB/s", float64(rate)/(1<<10))
}

func formatDuration(seconds int64) string {
	h, m, s := seconds/3600, seconds/60%60, seconds%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%02d:%02d", m, s)
}

func formatSize(done, total int64) string {
	if total >= 1<<30 {
		return fmt.Sprintf("%.1f / %.1f GB", float64(done)/(1<<30), float64(total)/(1<<30))
	}
	return fmt.Sprintf("%.0f / %.0f MB", float64(done)/(1<<20), float64(total)/(1<<20))
}
