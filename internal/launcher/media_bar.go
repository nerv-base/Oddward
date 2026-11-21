// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

const consoleHeight float32 = 60

func (s *Shell) mediaBarShown() bool {
	return s.Model.Page != state.LauncherSettings && s.Model.Overlay == state.NoOverlay && (s.Model.Bar == state.MediaBarVisible || s.Model.Bar == state.MediaBarLeaving)
}

// Pointer and focus are observations, not playback commands. Hidden controls
// never remain hit-testable, unlike Starward's opacity-only background strip.
func (s *Shell) updateMediaBar(c *ui.Context, root ui.Element) {
	if s.Model.Page == state.LauncherSettings {
		if s.Model.Overlay == state.NoOverlay && c.Shortcut(0, ui.KeyEscape) {
			s.Dispatch(state.Event{Kind: state.Dismiss})
			if s.Model.Bar != state.MediaBarVisible {
				s.navigationFocus.Focus()
			}
		}
		return
	}
	x, y, inside := root.PointerPosition()
	bounds := root.Bounds()
	near := inside && bounds.H > 0 && y >= bounds.H-consoleHeight
	if near && s.locateShown {
		// The first-run link hangs into this strip. Do not open the lane under
		// a control the user is already reaching for.
		link := locateActionBounds(bounds.W, bounds.H)
		near = x < link.X || x >= link.X+link.W || y < link.Y || y >= link.Y+link.H
	}
	s.Dispatch(state.Event{Kind: state.MediaBarActivity, Enabled: near, Focused: s.mediaFocus.FocusWithin(c), Now: c.Now()})
	if s.Model.Overlay == state.NoOverlay {
		if c.Shortcut(0, ui.KeyF6) {
			s.Dispatch(state.Event{Kind: state.RevealMediaBar})
			s.mediaFocus.Focus()
		} else if s.mediaBarShown() && c.Shortcut(0, ui.KeyEscape) {
			s.dismissMediaBar()
		}
	}
	if s.Model.Bar == state.MediaBarLeaving {
		c.After(s.Model.BarDeadline.Sub(c.Now()))
	}
}

func (s *Shell) dismissMediaBar() {
	s.Dispatch(state.Event{Kind: state.HideMediaBar})
	s.mediaFocus.CancelFocus()
	s.navigationFocus.Focus()
}

func (s *Shell) mediaBar(c *ui.Context) {
	if s.Model.Overlay != state.NoOverlay {
		return
	}
	if !s.mediaBarShown() {
		return
	}
	// Starward's full-width edge fade supports a small, bottom-centered icon
	// group, not a frosted panel. Keep our keyboard/focus and 600ms leave grace.
	bar := ui.Box(c.Key("media-bar")).Absolute().Left(0).Right(0).Bottom(0).Height(consoleHeight).
		Role(ui.RoleToolbar).Label("背景播放控制").
		Focusable().TabStop(false).Bind(&s.mediaFocus)
	prefs := c.Preferences()
	if prefs.ReduceTransparency || prefs.HighContrast {
		bar.Background(c.Theme().Surface)
	} else {
		// Slightly earlier darkening than the upstream linear ramp keeps the
		// 16-DIP paper-colored icons readable even on a white wallpaper.
		bar.LinearGradient(ui.LinearGradient{Angle: 180, Stops: []ui.GradientStop{
			{Color: ui.Transparent, At: 0},
			{Color: ui.RGBA(0, 0, 0, .48), At: .55},
			{Color: ui.RGBA(0, 0, 0, .60), At: 1},
		}})
	}
	if !s.Model.ReducedMotion && !s.Model.SystemReducedMotion {
		entered := ui.Local(bar, "entered", func() bool { return false })
		if !*entered {
			bar.AnimatePose(ui.Pose{Y: 6, Fade: 1}, ui.Animation{Duration: 180 * time.Millisecond})
			*entered = true
			c.Invalidate()
		} else {
			bar.AnimatePose(ui.Pose{}, ui.Animation{Duration: 180 * time.Millisecond})
		}
	}
	bar.Children(func() {
		ui.Row(c.Key("media-icons")).Absolute().Left(0).Right(0).Bottom(4).Height(32).Gap(4).Justify(ui.Center).Children(func() {
			s.mediaControl(c)
			iconButton(c, "background-settings", "背景设置", "\uE713", 32, func() { s.Dispatch(state.Event{Kind: state.Navigate, Page: state.LauncherSettings, FromMedia: true}) }).Radius(16).TextColor(paper)
			iconButton(c, "hide-media-bar", "收起控制台", "\uE8BB", 32, s.dismissMediaBar).Radius(16).TextColor(paper)
		})
	})
}

func (s *Shell) mediaControl(c *ui.Context) {
	label, icon := "暂停动态背景", "\uE769"
	if !s.Model.AnimateBackground() {
		label, icon = "播放动态背景", "\uE768"
	}
	status := "暂停视频并显示静态背景"
	if !s.Model.AnimateBackground() {
		status = "播放动态背景 · 静音循环"
	}
	if s.Model.Media == state.MediaLoading {
		status = "正在加载视频，暂显静态背景"
	}
	if s.Model.Media == state.MediaUnavailable {
		status = "视频不可用，显示静态背景"
	}
	if s.Model.ReducedMotion || s.Model.SystemReducedMotion {
		status = "减少动态效果已开启，可在设置中调整"
	}
	b := ui.ButtonBase(c.Key("motion")).Label(label).Tooltip(status).Size(32, 32).Radius(16).Justify(ui.Center).TextColor(paper).
		Disabled(s.Model.Media == state.MediaUnavailable || s.Model.Media == state.MediaLoading || s.Model.ReducedMotion || s.Model.SystemReducedMotion)
	fill := ui.Transparent
	if b.Hovered() {
		fill = ui.RGBA(235, 244, 255, .13)
	}
	if b.Pressed() {
		fill = ui.RGBA(235, 244, 255, .23)
	}
	s.controlFill(b, fill)
	b.Children(func() { glyph(c, icon, 16) }).OnClick(func() { s.Dispatch(state.Event{Kind: state.ToggleMotion}) })
}
