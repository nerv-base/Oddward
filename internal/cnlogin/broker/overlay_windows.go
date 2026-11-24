// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package broker

import (
	"context"
	"github.com/nerv-base/Oddward/internal/process"
)

// BindOverlay makes the authenticated launcher's own ordinary child window an
// owned window of this session's game. Success requires native owner readback.
func (c *Client) BindOverlay(ctx context.Context, ui process.Identity, window uintptr, gameWindow uintptr) error {
	p := overlayPayload{UI: ui, Window: uint64(window), GameWindow: uint64(gameWindow)}
	if !p.valid() {
		return errProtocol
	}
	return c.call(ctx, "bindoverlay", p)
}

type overlayPayload struct {
	UI          process.Identity `json:"ui"`
	Window      uint64           `json:"window,string"`
	GameWindow  uint64           `json:"game_window,string"`
	PreferChild bool             `json:"prefer_child,omitempty"`
}

func (p overlayPayload) valid() bool {
	return p.UI.PID != 0 && p.UI.CreationTime != 0 && p.Window != 0 && p.GameWindow != 0 && uint64(uintptr(p.Window)) == p.Window && uint64(uintptr(p.GameWindow)) == p.GameWindow && p.Window != p.GameWindow
}

type overlayBinder func(context.Context, overlayPayload) error

type overlayProcess struct {
	identity  process.Identity
	parentPID uint32
	sid       string
	session   uint32
	ordinary  bool
	alive     func() bool
	close     func()
}
type overlayWindow struct {
	pid                              uint32
	tid                              uint32
	owner, parent, style, dpi        uintptr
	topLevel, visible, tool, topmost bool
}

// This seam contains only per-process/per-window queries and one owner write.
// There is no executable, global enumeration, input, foreground or Z-order API.
type overlaySystem interface {
	open(process.Identity) (overlayProcess, error)
	window(uintptr) (overlayWindow, error)
	setOwner(uintptr, uintptr) error
}

func bindOverlay(ctx context.Context, parent, game process.Identity, p overlayPayload, sys overlaySystem) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !p.valid() || parent.PID == 0 || parent.CreationTime == 0 || game.PID == 0 || game.CreationTime == 0 || parent == p.UI || game == p.UI {
		return errRemote
	}
	root, err := sys.open(parent)
	if err != nil {
		return errRemote
	}
	defer root.close()
	ui, err := sys.open(p.UI)
	if err != nil {
		return errRemote
	}
	defer ui.close()
	target, err := sys.open(game)
	if err != nil {
		return errRemote
	}
	defer target.close()
	if root.identity != parent || ui.identity != p.UI || target.identity != game || ui.parentPID != parent.PID || ui.identity.CreationTime <= parent.CreationTime || !root.ordinary || !ui.ordinary || root.sid == "" || ui.sid != root.sid || target.sid != root.sid || ui.session != root.session || target.session != root.session {
		return errRemote
	}
	alive := func() bool { return root.alive() && ui.alive() && target.alive() }
	var gameWindow overlayWindow
	check := func() (overlayWindow, error) {
		w, err := sys.window(uintptr(p.Window))
		ownChild := p.PreferChild && w.style&overlayChild != 0 && w.style&overlayChildClear == 0 && w.parent == uintptr(p.GameWindow)
		if err != nil || w.pid != p.UI.PID || (!w.topLevel && !ownChild) || w.topmost || (p.PreferChild && (w.visible || w.style&overlayVisible != 0)) {
			return overlayWindow{}, errRemote
		}
		g, err := sys.window(uintptr(p.GameWindow))
		if err != nil || g.pid != game.PID || !g.topLevel || !g.visible || g.tool {
			return overlayWindow{}, errRemote
		}
		if w.owner != 0 && w.owner != uintptr(p.GameWindow) {
			owner, err := sys.window(w.owner)
			if err != nil || !owner.topLevel {
				return overlayWindow{}, errRemote
			}
			hiddenUI := owner.pid == p.UI.PID && !owner.visible && w.owner != uintptr(p.Window)
			ownGame := owner.pid == game.PID && !owner.tool
			if !hiddenUI && !ownGame {
				return overlayWindow{}, errRemote
			}
		}
		gameWindow = g
		return w, nil
	}
	if !alive() {
		return errRemote
	}
	if _, err := check(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Revalidate retained processes and window ownership immediately before
	// mutation. The expected parent and game identities are fixed by this
	// authenticated host session, not selected by the caller.
	if !alive() {
		return errRemote
	}
	before, err := check()
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.PreferChild {
		if child, ok := sys.(overlayChildSystem); ok && child.sameDPI(before.dpi, gameWindow.dpi) {
			return embedWindow(ctx, uintptr(p.Window), uintptr(p.GameWindow), before, gameWindow, child, alive)
		}
		// An existing child cannot safely become an owned popup by writing its
		// owner slot. DPI uncertainty only falls back for an initial top level.
		if !before.topLevel {
			return errRemote
		}
	}
	if err := sys.setOwner(uintptr(p.Window), uintptr(p.GameWindow)); err != nil {
		return errRemote
	}
	actual, err := check()
	if err != nil || actual.owner != uintptr(p.GameWindow) || !alive() {
		return errRemote
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}
