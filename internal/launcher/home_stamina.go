// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

const (
	homeStaminaWidth  float32 = 224
	homeStaminaHeight float32 = 66
	homeStaminaTop    float32 = 76
)

// homeStamina is the Starward-style daily-note chip on the home screen: one
// compact card, fed by the same role-home snapshot as the check-in page. It
// only draws when a linked role with a reported cap is available; it never
// invents a value or a reserve pool.
func (s *Shell) homeStamina(c *ui.Context) {
	m := s.Model.CheckIn
	if !m.Linked || m.Role.StaminaMax <= 0 {
		return
	}
	f, ok := state.ForecastStamina(m.Role, s.now)
	if !ok {
		return
	}
	value := fmt.Sprintf("本性像素 %d/%d", f.Current, f.Max)
	status := "已满"
	if !f.Full {
		status = "约 " + staminaWait(f.NextIn) + "后 +1"
		if f.FullIn > f.NextIn {
			status += " · " + staminaWait(f.FullIn) + "后回满"
		}
	}
	label := value + "，" + status
	card := ui.Box(c.Key("home-stamina")).Label(label).Absolute().Right(dockRight).Top(homeStaminaTop).
		Size(homeStaminaWidth, homeStaminaHeight).Radius(12).Padding(12).Material(frostedSurface{Tint: glassInk}).PassThrough()
	card.Children(func() {
		ui.Column(c).Fill().Gap(2).Children(func() {
			ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(6).Children(func() {
				ui.Box(c).Size(4, 22).Radius(2).Background(s.tone())
				ui.Text(c, "本性像素").FontSize(12).TextColor(muted).SingleLine()
				ui.Box(c).Grow(1)
				ui.Text(c, fmt.Sprintf("%d/%d", f.Current, f.Max)).FontSize(18).FontWeight(600).TextColor(paper).SingleLine()
			})
			ui.Text(c, status).FontSize(11).TextColor(muted).SingleLine().Shrink(1).Tooltip(status)
		})
	})
}

// staminaWait renders a short countdown for the home chip. Seconds are kept
// for the final minute so a "6 分" card does not sit unchanged near the point.
func staminaWait(d time.Duration) string {
	if d <= 0 {
		return "即将"
	}
	d = d.Truncate(time.Second)
	h := int(d / time.Hour)
	m := int(d/time.Minute) % 60
	sec := int(d/time.Second) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%d 小时 %d 分", h, m)
	case m > 0 && sec > 0:
		return fmt.Sprintf("%d 分 %d 秒", m, sec)
	case m > 0:
		return fmt.Sprintf("%d 分", m)
	default:
		return fmt.Sprintf("%d 秒", sec)
	}
}
