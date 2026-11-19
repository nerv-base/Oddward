// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

const (
	homeStaminaWidth    float32 = 312
	homeStaminaHeight   float32 = 220
	homeStaminaTop      float32 = 64
	homeStaminaInset    float32 = 14
	homeStaminaInner    float32 = homeStaminaWidth - 2*homeStaminaInset
	homeStaminaIconSize float32 = 44
)

// homeStamina is the Starward-style daily-note entry on the home screen: a
// compact resource icon whose popover carries the role identity, the main
// stamina reading with a recovery estimate, a progress meter, the city-stamina
// snapshot and the day's activity figures. It only draws when a linked role
// with a reported cap is available; it never invents a value or a reserve pool.
func (s *Shell) homeStamina(c *ui.Context) {
	m := s.Model.CheckIn
	if !m.Linked || m.NeedsRelink || m.Role.StaminaMax <= 0 {
		s.staminaOpen = false
		return
	}
	f, ok := state.ForecastStamina(m.Role, s.now)
	if !ok {
		s.staminaOpen = false
		return
	}
	status := "已满"
	if !f.Full {
		status = "约 " + staminaWait(f.NextIn) + "后 +1"
	}
	anchor := ui.ButtonBase(c.Key("home-stamina-toggle")).Label("查看体力与活跃度").Tooltip("查看体力与活跃度").
		Absolute().Right(dockRight).Top(homeStaminaTop).Size(homeStaminaIconSize, homeStaminaIconSize).
		Radius(homeStaminaIconSize/2).Justify(ui.Center).AlignItems(ui.Center).
		Material(frostedSurface{Tint: glassInk}).Border(1, hairline)
	if s.staminaOpen {
		anchor.Border(1, s.tone())
	}
	if anchor.Hovered() || anchor.FocusVisible() {
		anchor.Background(hairline)
	}
	anchor.Children(func() {
		if s.StaminaIcon != nil {
			ui.Image(c, s.StaminaIcon).Size(28, 28).Fit(ui.Contain).PassThrough()
		} else {
			glyph(c, "\uE9D8", 26)
		}
	}).OnClick(func() {
		s.staminaOpen = !s.staminaOpen
	})
	if !s.staminaOpen {
		return
	}
	ui.PopoverBase(c, anchor, &s.staminaOpen, func(panel ui.Element) {
		panel.AttachTo(anchor, ui.AnchorBottomEnd, ui.AnchorTopEnd).Margin(8, 0, 0, 0).
			Size(homeStaminaWidth, homeStaminaHeight).Padding(homeStaminaInset).Radius(14).
			Material(frostedSurface{Tint: glassInk}).Border(1, hairline).
			Label(fmt.Sprintf("体力和活跃度，本性像素 %d/%d，%s", f.Current, f.Max, status)).
			Transition(ui.ElementTransition{
				Duration:     160 * time.Millisecond,
				ExitDuration: 120 * time.Millisecond,
				Enter:        &ui.Motion{X: 10, Opacity: 0},
				Exit:         &ui.Motion{X: 10, Opacity: 0},
			})
		panel.Children(func() {
			ui.Column(c).Fill().Gap(7).Children(func() {
				s.homeStaminaHeader(c, m.Role)
				s.homeStaminaMain(c, f, status)
				ui.Box(c).FillWidth().Height(5).Radius(3).Background(hairline).Children(func() {
					ratio := float32(f.Current) / float32(f.Max)
					if ratio < 0 {
						ratio = 0
					}
					if ratio > 1 {
						ratio = 1
					}
					ui.Box(c).Width(ratio * homeStaminaInner).Height(5).Radius(3).Background(s.tone())
				})
				ui.Box(c).FillWidth().Height(1).Background(hairline)
				s.homeStaminaCity(c, m.Role)
				s.homeStaminaStats(c, m.Role)
			})
		})
	})
}

func (s *Shell) homeStaminaHeader(c *ui.Context, role state.CheckInRole) {
	name := role.Name
	if name == "" {
		name = "角色"
	}
	initial := "?"
	if runes := []rune(role.Name); len(runes) > 0 {
		initial = string(runes[0])
	}
	ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(9).Children(func() {
		if s.RoleAvatar != nil {
			ui.Image(c, s.RoleAvatar).Size(40, 40).Radius(20).Fit(ui.Cover).PassThrough().
				Label("角色头像")
		} else {
			ui.Box(c).Size(40, 40).Radius(20).Background(s.tone()).Justify(ui.Center).AlignItems(ui.Center).Children(func() {
				ui.Text(c, initial).FontSize(16).FontWeight(600).TextColor(glassInk).SingleLine()
			})
		}
		ui.Column(c).Grow(1).Gap(1).Children(func() {
			ui.Text(c, name).FontSize(14).FontWeight(600).TextColor(paper).SingleLine().Shrink(1)
			meta := "已保存的游戏账号"
			if role.Level > 0 && role.WorldLevel > 0 {
				meta = fmt.Sprintf("Lv.%d · 鉴别等级 %d", role.Level, role.WorldLevel)
			} else if role.Level > 0 {
				meta = fmt.Sprintf("Lv.%d", role.Level)
			}
			ui.Text(c, meta).FontSize(10).TextColor(muted).SingleLine().Shrink(1)
		})
		s.homeStaminaRefresh(c)
	})
}

// homeStaminaRefresh is the card's manual read trigger. It only dispatches the
// same status event the check-in page uses; the backend still runs off-thread.
func (s *Shell) homeStaminaRefresh(c *ui.Context) {
	loading := s.Model.CheckIn.StatusPhase == state.CheckStatusLoading
	b := iconButton(c, "home-stamina-refresh", "刷新体力", "\uE72C", 24, func() {
		s.Dispatch(state.Event{Kind: state.CheckInStatusRequested})
	})
	b.Disabled(loading)
	if loading {
		b.Tooltip("正在刷新体力")
	}
}

// setRoleAvatar decodes the portrait bytes delivered with a status snapshot.
// It runs on the UI thread, once per read, never per frame.
func (s *Shell) setRoleAvatar(avatar *state.CheckInAvatar) {
	if avatar == nil || len(avatar.PNG) == 0 {
		s.RoleAvatar = nil
		return
	}
	s.RoleAvatar = decodeBundledIcon(avatar.PNG)
}

func (s *Shell) homeStaminaMain(c *ui.Context, f state.StaminaForecast, status string) {
	ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
		if s.StaminaIcon != nil {
			ui.Image(c, s.StaminaIcon).Size(30, 30).Fit(ui.Contain).PassThrough()
		} else {
			ui.Icon(c, launcherIcons["\uE9D8"]).FontSize(28).TextColor(s.tone())
		}
		ui.Column(c).Gap(1).Children(func() {
			ui.Text(c, "本性像素").FontSize(11).TextColor(muted).SingleLine()
			ui.Row(c).AlignItems(ui.Center).Gap(3).Children(func() {
				ui.Text(c, fmt.Sprintf("%d", f.Current)).FontSize(24).FontWeight(600).TextColor(paper).SingleLine()
				ui.Text(c, fmt.Sprintf("/ %d", f.Max)).FontSize(12).TextColor(muted).SingleLine()
			})
		})
		ui.Box(c).Grow(1)
		ui.Column(c).AlignItems(ui.End).Gap(1).Children(func() {
			ui.Text(c, status).FontSize(11).TextColor(s.tone()).SingleLine().Tooltip(status)
			if !f.Full {
				ui.Text(c, "回满 "+s.now.Add(f.FullIn).Local().Format("15:04")).
					FontSize(10).TextColor(muted).SingleLine()
			}
		})
	})
}

func (s *Shell) homeStaminaCity(c *ui.Context, role state.CheckInRole) {
	city := "—"
	if role.CityStaminaMax > 0 {
		city = fmt.Sprintf("%d / %d", role.CityStamina, role.CityStaminaMax)
	}
	ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
		if s.CityStaminaIcon != nil {
			ui.Image(c, s.CityStaminaIcon).Size(22, 22).Fit(ui.Contain).PassThrough()
		} else {
			ui.Icon(c, launcherIcons["\uE9D7"]).FontSize(22).TextColor(muted)
		}
		ui.Column(c).Grow(1).Gap(1).Children(func() {
			ui.Text(c, "都市活力").FontSize(10).TextColor(muted).SingleLine()
			ui.Text(c, city).FontSize(12).TextColor(paper).SingleLine()
		})
		ui.Text(c, "每周一 05:00 +5").FontSize(10).TextColor(muted).SingleLine()
	})
}

func (s *Shell) homeStaminaStats(c *ui.Context, role state.CheckInRole) {
	stats := []struct {
		label string
		value string
		tip   string
	}{
		{"今日活跃度", fmt.Sprintf("%d / 100", role.DayValue), ""},
		{"周本剩余", fmt.Sprintf("%d / 3", role.WeekRemain), ""},
	}
	ui.Row(c).FillWidth().Gap(8).Children(func() {
		for _, stat := range stats {
			ui.Column(c).Grow(1).Shrink(1).Gap(1).Children(func() {
				ui.Text(c, stat.label).FontSize(9).TextColor(muted).SingleLine()
				value := ui.Text(c, stat.value).FontSize(11).TextColor(paper).SingleLine()
				if stat.tip != "" {
					value.Tooltip(stat.tip)
				}
			})
		}
	})
}

// staminaWait renders a short countdown for the home card. Seconds are kept
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
