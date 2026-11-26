// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package broker

import (
	"context"
	"errors"

	"github.com/nerv-base/Oddward/internal/process"
)

const (
	overlayChild       = uintptr(0x40000000)
	overlayPopup       = uintptr(0x80000000)
	overlayVisible     = uintptr(0x10000000)
	overlayCaption     = uintptr(0x00C00000)
	overlaySysMenu     = uintptr(0x00080000)
	overlayThickFrame  = uintptr(0x00040000)
	overlayMinimizeBox = uintptr(0x00020000)
	overlayMaximizeBox = uintptr(0x00010000)
	// Top-level decorations must be stripped with WS_POPUP when converting a
	// frameless native window (such as MyGo) to WS_CHILD, otherwise user32
	// paints a classic child title bar over its client area.
	overlayChildClear = overlayPopup | overlayCaption | overlaySysMenu | overlayThickFrame | overlayMinimizeBox | overlayMaximizeBox
)

var errOverlayRestore = errors.New("cnlogin/broker: overlay recovery failed")

// EmbedOverlay prefers a hidden child of the session game when both actual
// window DPI contexts match; otherwise it retains BindOverlay's owner behavior.
func (c *Client) EmbedOverlay(ctx context.Context, ui process.Identity, window, gameWindow uintptr) error {
	p := overlayPayload{UI: ui, Window: uint64(window), GameWindow: uint64(gameWindow), PreferChild: true}
	if !p.valid() {
		return errProtocol
	}
	return c.call(ctx, "bindoverlay", p)
}

// The additional operations are restricted to the already verified UI window.
type overlayChildSystem interface {
	overlaySystem
	sameDPI(uintptr, uintptr) bool
	setStyle(uintptr, uintptr) error
	setParent(uintptr, uintptr) error
	frameChanged(uintptr) error
}

// embedWindow is called with retained process handles and verified snapshots.
// It never reparents across differing or unknown window DPI contexts.
func embedWindow(ctx context.Context, window, gameWindow uintptr, before, target overlayWindow, sys overlayChildSystem, alive func() bool) (result error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	if before.visible || before.topmost || before.style&overlayVisible != 0 || !sys.sameDPI(before.dpi, target.dpi) {
		return errRemote
	}
	// A reused HWND, a different thread, or an intervening foreign owner/parent
	// must never receive a mutation, including a best-effort rollback.
	ownedWindow := func() (overlayWindow, error) {
		if !alive() {
			return overlayWindow{}, errRemote
		}
		w, err := sys.window(window)
		if err != nil || w.pid != before.pid || w.tid != before.tid || w.visible || w.topmost || w.style&overlayVisible != 0 || (w.owner != 0 && w.owner != before.owner) {
			return overlayWindow{}, errRemote
		}
		relation := w.owner
		if w.style&overlayChild != 0 {
			relation = w.parent
		}
		if relation != 0 && relation != before.owner && relation != gameWindow {
			return overlayWindow{}, errRemote
		}
		return w, nil
	}
	check := func() (overlayWindow, error) {
		if err := ctx.Err(); err != nil {
			return overlayWindow{}, err
		}
		w, err := ownedWindow()
		if err != nil {
			return overlayWindow{}, err
		}
		g, err := sys.window(gameWindow)
		if err != nil || g.pid != target.pid || g.tid != target.tid || !g.topLevel || g.visible != target.visible || g.tool || !sys.sameDPI(w.dpi, before.dpi) || !sys.sameDPI(g.dpi, target.dpi) || !sys.sameDPI(w.dpi, g.dpi) {
			return overlayWindow{}, errRemote
		}
		return w, nil
	}
	childBound := func(w overlayWindow) bool {
		return w.style&overlayChild != 0 && w.style&overlayChildClear == 0 && w.parent == gameWindow && (w.owner == 0 || w.owner == before.owner) && !w.topLevel
	}
	current, err := check()
	if err != nil {
		return err
	}
	if !before.topLevel {
		if childBound(current) {
			return nil
		}
		return errRemote
	}
	if !current.topLevel || current.owner != before.owner || current.style != before.style {
		return errRemote
	}
	// Restore only this same hidden UI. Failed recovery remains a failure; it
	// never turns into an owner fallback or an ACK for a partially changed HWND.
	restore := func() error {
		w, err := ownedWindow()
		if err != nil {
			return errOverlayRestore
		}
		if w.style&overlayChild != 0 && w.parent != 0 {
			if sys.setParent(window, 0) != nil {
				return errOverlayRestore
			}
		}
		if _, err := ownedWindow(); err != nil || sys.setStyle(window, before.style) != nil {
			return errOverlayRestore
		}
		if before.owner != 0 {
			owner, err := sys.window(before.owner)
			if err != nil || !owner.topLevel || !((owner.pid == before.pid && !owner.visible) || (owner.pid == target.pid && !owner.tool)) {
				return errOverlayRestore
			}
		}
		if _, err := ownedWindow(); err != nil || sys.setOwner(window, before.owner) != nil {
			return errOverlayRestore
		}
		if _, err := ownedWindow(); err != nil || sys.frameChanged(window) != nil {
			return errOverlayRestore
		}
		w, err = ownedWindow()
		if err != nil || !w.topLevel || w.style != before.style || w.owner != before.owner || w.parent != 0 {
			return errOverlayRestore
		}
		return nil
	}
	defer func() {
		if result != nil && restore() != nil {
			result = errOverlayRestore
		}
	}()
	if err := sys.setStyle(window, before.style&^overlayChildClear|overlayChild); err != nil {
		return errRemote
	}
	current, err = check()
	if err != nil || current.style&overlayChild == 0 || current.style&overlayChildClear != 0 {
		if err != nil {
			return err
		}
		return errRemote
	}
	if err := sys.setParent(window, gameWindow); err != nil {
		return errRemote
	}
	if _, err := check(); err != nil {
		return err
	}
	if err := sys.frameChanged(window); err != nil {
		return errRemote
	}
	current, err = check()
	if err != nil {
		return err
	}
	if !childBound(current) {
		return errRemote
	}
	return nil
}
