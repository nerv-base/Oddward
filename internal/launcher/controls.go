// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// Neutral glass surfaces and one art-derived interaction tone. Input, focus and
// accessibility remain MyGo's; strong fill is reserved for the primary action.
func (s *Shell) controlFill(b ui.Element, color ui.Color) {
	if !s.Model.ReducedMotion && !s.Model.SystemReducedMotion {
		color = b.AnimateColor("control-fill", color, ui.Animation{Duration: 140 * time.Millisecond})
	}
	b.Background(color)
}

// quietButton is a soft glass key: rounded, hairline-edged, tone on hover.
func (s *Shell) quietButton(c *ui.Context, key, label string, disabled bool) ui.Element {
	b := ui.ButtonBase(c.Key(key)).Label(label).Height(42).Radius(12).Padding(0, 18).Justify(ui.Center).Border(1, hairline).TextColor(paper).Disabled(disabled).DimDisabled(false)
	fill := ui.RGBA(255, 255, 255, .05)
	if disabled {
		b.TextColor(ui.Hex("#6E727B"))
		fill = ui.RGBA(255, 255, 255, .02)
	} else if b.Pressed() {
		fill = ui.RGBA(255, 255, 255, .16)
	} else if b.Hovered() || b.FocusVisible() {
		fill = ui.RGBA(255, 255, 255, .10)
		b.Border(1, s.tone().Alpha(.55))
	}
	s.controlFill(b, fill)
	b.Children(func() { ui.Text(c, label).FontSize(14) })
	return b
}

// accentButton is the one tone-filled pill of a page or panel: its main,
// safe action. Everything else stays quiet glass.
func (s *Shell) accentButton(c *ui.Context, key, label string) ui.Element {
	tone := s.tone()
	b := ui.ButtonBase(c.Key(key)).Label(label).Height(44).Radius(22).Padding(0, 22).Justify(ui.Center).TextColor(ink)
	fill := tone
	if b.Pressed() {
		fill = tone.Mix(ink, .10)
	} else if b.Hovered() || b.FocusVisible() {
		fill = tone.Mix(ui.Hex("#FFFFFF"), .14)
		b.Shadow(0, 6, 20, 0, tone.Alpha(.35))
	}
	s.controlFill(b, fill)
	b.Children(func() { ui.Text(c, label).FontSize(14).FontWeight(700) })
	return b
}
func (s *Shell) preferenceSwitch(c *ui.Context, key string, caption ui.Element, on, disabled bool, event state.EventKind) {
	// Bind only a local value. The callback emits an event, never edits Model.
	b := ui.SwitchBase(c.Key(key), &on).LabelledBy(caption).Size(48, 28).Radius(14).Disabled(disabled)
	x, fill, knob := float32(3), ui.RGBA(255, 255, 255, .10), muted
	if on {
		x, fill, knob = 23, s.tone(), ink
	}
	if b.Hovered() && !disabled {
		if on {
			fill = s.tone()
		} else {
			fill = ui.RGBA(255, 255, 255, .16)
		}
	}
	if b.Pressed() && !disabled {
		if on {
			fill = s.tone().Mix(ui.Hex("#FFFFFF"), .2)
		} else {
			fill = ui.RGBA(255, 255, 255, .22)
		}
	}
	s.controlFill(b, fill)
	if !s.Model.ReducedMotion && !s.Model.SystemReducedMotion {
		x = b.AnimateTo("knob", x, ui.Animation{Duration: 140 * time.Millisecond})
	}
	b.Border(1, hairline).Children(func() {
		ui.Box(c).Absolute().Left(x).Top(3).Size(22, 22).Radius(11).Background(knob)
	}).OnChange(func() { s.Dispatch(state.Event{Kind: event, Enabled: on}) })
}
