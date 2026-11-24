// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package native

import (
	"strconv"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"golang.org/x/sys/windows"

	"github.com/nerv-base/Oddward/internal/branding"
	"github.com/nerv-base/Oddward/internal/cnlogin/loginui"
	"github.com/nerv-base/Oddward/internal/cnlogin/loginui/state"
	"github.com/nerv-base/Oddward/internal/overlaywin"
)

// controller manages the native window lifecycle, WinEvent tracking, broker
// binding handshake, controlled HWND recreation and welcome banner animation.
// Every state mutation runs on the MyGo main (UI) thread.
type controller struct {
	emit   func(loginui.Event) bool
	onStop func(int)

	shell *ViewShell
	win   *mygo.Window
	hwnd  uintptr

	// Verified game process identity retained for the session lifetime.
	initialized      bool
	gameProcess      windows.Handle
	gamePID          uint32
	gameCreationTime string
	gameCreation     uint64
	exitStopEvent    windows.Handle
	exitWaitDone     chan struct{}

	watcher     *overlaywin.Watcher
	tickStop    chan struct{}
	tickRunning bool

	// Per-target binding state (mirrors GameOverlay.cs).
	preferEmbedding     bool
	gameWindow          uintptr
	destroyedParent     uintptr
	ownerAttemptedFor   uintptr
	ownerRequested      bool
	bindingAcknowledged bool
	bindingRequest      *loginui.Event
	nextRequestID       uint64
	lastBounds          overlaywin.Rect
	lastChild           bool
	hasPlaced           bool

	// Controlled recreation & reentry guards.
	refreshing     bool
	refreshAgain   bool
	refreshQueued  bool
	closeRequested bool
	replacingView  bool
	recreating     bool
	recreateTimer  *time.Timer
	capturedForm   *state.FormState

	// Welcome banner independent animation & hold lifecycle.
	bannerMode    bool
	bannerStarted bool
	bannerStop    chan struct{}
	bannerDone    chan struct{}

	stopped  bool
	exitCode int
	stopOnce sync.Once
}

func newController(emit func(loginui.Event) bool, onStop func(int)) *controller {
	c := &controller{
		emit:            emit,
		onStop:          onStop,
		preferEmbedding: true,
	}
	c.shell = NewViewShell(c.handleEffect)
	c.shell.OnInvalidate = func() {
		if c.win != nil {
			c.win.Invalidate()
		}
	}
	return c
}

func (c *controller) handleEffect(fx state.Effect) {
	if c.stopped {
		return
	}
	switch fx.Kind {
	case state.EffectEmitEvent:
		if !c.emit(fx.Event) {
			c.Stop(1)
		}
	case state.EffectRequestClose:
		c.requestUserClose()
	case state.EffectEnterBanner:
		c.enterBannerMode()
	}
}

func (c *controller) requestUserClose() {
	if c.stopped {
		return
	}
	c.closeRequested = true
	if c.win != nil {
		c.win.Close()
		return
	}
	if !c.bannerMode {
		_ = c.emit(loginui.Event{Kind: "cancel"})
	}
	c.Stop(0)
}

func (c *controller) enterBannerMode() {
	if c.bannerMode {
		if c.win != nil {
			c.win.Invalidate()
		}
		return
	}
	c.bannerMode = true
	// If a banner starts on a window that already exists, apply non-activating
	// click-through styles immediately.
	if c.win != nil && c.hwnd != 0 {
		c.applyBannerStyles()
	}
}

func (c *controller) applyBannerStyles() {
	if c.win == nil || c.hwnd == 0 {
		return
	}
	c.win.SetIgnoreMouseEvents(true)
	_, _ = overlaywin.MakeBannerStyle(c.hwnd)
	if !c.shell.BannerAlphaSet {
		if !overlaywin.AnimationsEnabled() {
			c.shell.BannerAlpha = 1
		} else {
			c.shell.BannerAlpha = 0
		}
		c.shell.BannerAlphaSet = true
	}
}

// Apply processes one validated loginui.Model frame on the UI thread.
func (c *controller) Apply(m loginui.Model) {
	if c.stopped {
		return
	}
	if !c.initialized {
		c.initialized = true
		c.gamePID = m.GameProcessID
		c.gameCreationTime = m.GameCreationTime
		if c.gamePID != 0 {
			created, err := overlaywin.ParseCreationTime(c.gameCreationTime)
			if err != nil {
				c.Stop(1)
				return
			}
			h, err := overlaywin.OpenVerified(overlaywin.Identity{PID: c.gamePID, CreationTime: created})
			if err != nil {
				c.Stop(1)
				return
			}
			c.gameProcess = h
			c.gameCreation = created
			c.shell.OverlayMode = true
			c.startProcessWaiter()
			if err := c.startWatcher(); err != nil {
				c.Stop(1)
				return
			}
		}
	} else if m.GameProcessID != c.gamePID || m.GameCreationTime != c.gameCreationTime {
		c.Stop(1)
		return
	}

	// A fresh backend snapshot clears any in-flight action captured during recreation.
	if c.capturedForm != nil {
		cleared := c.capturedForm.ClearPending()
		c.capturedForm = &cleared
	}

	c.shell.ApplyModel(m)

	if c.gamePID == 0 {
		// Standalone mode (no game process attached).
		if c.win == nil {
			if err := c.createStandaloneWindow(); err != nil {
				c.Stop(1)
				return
			}
		}
		if c.win != nil {
			c.win.Invalidate()
			if !c.win.IsVisible() {
				if c.bannerMode {
					c.win.ShowInactive()
					c.beginBannerAnimation()
				} else {
					c.win.Show()
				}
			}
		}
		return
	}

	c.refreshSafely()
	if !c.stopped {
		c.startFallbackTicker()
	}
}

// WindowBound handles a broker binding acknowledgment on the UI thread.
func (c *controller) WindowBound(completed loginui.Event) {
	if c.stopped || c.bindingRequest == nil || *c.bindingRequest != completed {
		return
	}
	c.bindingAcknowledged = true
	c.refreshSafely()
}

func (c *controller) startProcessWaiter() {
	ev, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		return
	}
	c.exitStopEvent = ev
	c.exitWaitDone = make(chan struct{})
	proc := c.gameProcess
	go func() {
		defer close(c.exitWaitDone)
		handles := []windows.Handle{proc, ev}
		res, err := windows.WaitForMultipleObjects(handles, false, windows.INFINITE)
		if err != nil || res == windows.WAIT_OBJECT_0 {
			go mygo.RunOnMain(func() {
				if err != nil {
					c.Stop(1)
					return
				}
				c.Stop(0)
			})
		}
	}()
}

func (c *controller) startWatcher() error {
	w, err := overlaywin.NewWatcher(overlaywin.Identity{PID: c.gamePID, CreationTime: c.gameCreation})
	if err != nil {
		return err
	}
	w.SetOnEvent(c.scheduleRefresh)
	if err := w.Install(); err != nil {
		return err
	}
	c.watcher = w
	return nil
}

// startFallbackTicker runs a low-frequency reconciliation loop (150ms) for
// late game-window appearance, missed WinEvents and fallback process checks.
// Steady-state coordinate changes are handled immediately by WinEvent hooks.
func (c *controller) startFallbackTicker() {
	if c.tickRunning || c.stopped {
		return
	}
	c.tickRunning = true
	c.tickStop = make(chan struct{})
	stopCh := c.tickStop
	go func() {
		t := time.NewTicker(150 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-t.C:
				mygo.RunOnMain(c.refreshSafely)
			}
		}
	}()
}

func (c *controller) scheduleRefresh() {
	if c.stopped || c.refreshQueued {
		return
	}
	c.refreshQueued = true
	go mygo.RunOnMain(func() {
		c.refreshQueued = false
		c.refreshSafely()
	})
}

func (c *controller) refreshSafely() {
	if c.stopped || c.recreating {
		return
	}
	if c.refreshing {
		c.refreshAgain = true
		return
	}
	c.refreshing = true
	c.refreshAgain = false
	c.refresh()
	c.refreshing = false
	if c.refreshAgain && !c.stopped && !c.refreshQueued {
		c.refreshAgain = false
		c.scheduleRefresh()
	}
}

func (c *controller) refresh() {
	if c.stopped || c.gameProcess == 0 {
		return
	}
	alive, err := overlaywin.ProcessAlive(c.gameProcess)
	if err != nil {
		c.Stop(1)
		return
	}
	if !alive {
		c.Stop(0)
		return
	}
	// While a broker transaction is in flight, preserve this HWND and request
	// until the exact ACK arrives even if the visible game target changes.
	if c.ownerRequested && !c.bindingAcknowledged {
		c.hideWindow()
		return
	}
	if c.destroyedParent != 0 && !overlaywin.IsWindow(c.destroyedParent) {
		c.destroyedParent = 0
	}
	if !overlaywin.CandidateOf(c.gameWindow, c.gamePID) || c.gameWindow == c.destroyedParent {
		next := overlaywin.FindMainWindow(c.gamePID)
		if next == c.destroyedParent {
			next = 0
		}
		c.gameWindow = next
	}
	if c.gameWindow == 0 || overlaywin.Minimized(c.gameWindow) {
		c.hideWindow()
		return
	}
	bounds, err := overlaywin.ClientRectOf(c.gameWindow)
	if err != nil || bounds.Empty() {
		c.hideWindow()
		return
	}
	if c.win == nil {
		if err := c.createOverlayWindow(c.gameWindow, bounds); err != nil {
			if !overlaywin.CandidateOf(c.gameWindow, c.gamePID) {
				c.gameWindow = 0
				return
			}
			c.Stop(1)
			return
		}
	}
	// Never migrate an existing WS_CHILD from another live parent. Replace
	// only our own window while preserving in-memory form state. If our child
	// has already been detached/destroyed (parent == 0 or !IsWindow), wait for
	// onWindowClosed rather than racing back onto the dying parent HWND.
	if !overlaywin.IsWindow(c.hwnd) {
		c.hideWindow()
		return
	}
	if overlaywin.HasChildStyle(c.hwnd) {
		parent := overlaywin.Parent(c.hwnd)
		if parent == 0 {
			c.destroyedParent = c.gameWindow
			c.gameWindow = 0
			c.hideWindow()
			return
		}
		if parent != c.gameWindow {
			c.hideWindow()
			c.recreateView()
			return
		}
	}
	if c.ownerAttemptedFor != c.gameWindow {
		c.hideWindow()
		c.ownerAttemptedFor = c.gameWindow
		c.ownerRequested = false
		c.bindingRequest = nil
		c.bindingAcknowledged = false
		c.hasPlaced = false
	}
	// Both login windows and welcome banners prefer native child embedding
	// (embed_overlay), matching WPF's GameOverlay behavior so the surface
	// composites reliably above fullscreen, borderless or topmost game windows
	// while the broker falls back to owner mode on DPI context mismatch.
	// Welcome banners retain WS_EX_LAYERED | WS_EX_TRANSPARENT | WS_EX_TOOLWINDOW
	// | WS_EX_NOACTIVATE so clicks pass straight through the WS_CHILD subtree.
	if c.preferEmbedding && !c.ownerRequested {
		c.requestBinding("embed_overlay")
		return
	}
	isChild := overlaywin.ChildOf(c.hwnd, c.gameWindow)
	isOwned := overlaywin.Owner(c.hwnd) == c.gameWindow
	if !isChild && !isOwned {
		c.hideWindow()
		if !c.ownerRequested {
			c.requestBinding("bind_overlay")
		}
		return
	}
	targetRect := bounds
	if isChild {
		targetRect = overlaywin.Rect{X: 0, Y: 0, W: bounds.W, H: bounds.H}
	}
	if !c.hasPlaced || c.lastBounds != bounds || c.lastChild != isChild {
		flags := uintptr(0)
		if isChild && (!c.hasPlaced || !c.lastChild) {
			flags = overlaywin.SWPFrameChanged
		}
		if err := overlaywin.SetPos(c.hwnd, targetRect, flags); err != nil {
			if !overlaywin.IsWindow(c.hwnd) || !overlaywin.CandidateOf(c.gameWindow, c.gamePID) {
				c.hideWindow()
				return
			}
			c.Stop(1)
			return
		}
		c.lastBounds = bounds
		c.lastChild = isChild
		c.hasPlaced = true
	}
	if !c.stopped && !c.win.IsVisible() {
		c.win.ShowInactive()
		if c.bannerMode {
			c.beginBannerAnimation()
		}
	}
}

func (c *controller) requestBinding(kind string) {
	c.hideWindow()
	c.ownerRequested = true
	c.bindingAcknowledged = false
	c.nextRequestID++
	ev := loginui.Event{
		Kind:       kind,
		Window:     strconv.FormatUint(uint64(c.hwnd), 10),
		GameWindow: strconv.FormatUint(uint64(c.gameWindow), 10),
		RequestID:  strconv.FormatUint(c.nextRequestID, 10),
	}
	c.bindingRequest = &ev
	if !c.emit(ev) {
		c.Stop(1)
	}
}

func (c *controller) createStandaloneWindow() error {
	w := mygo.NewWindow(mygo.WindowOptions{
		Title:             "Oddward 国服登录",
		Width:             int(cardWidth),
		Height:            int(cardHeight),
		Frameless:         true,
		Transparent:       true,
		DisableResize:     true,
		DisableMaximize:   true,
		DisableMinimize:   true,
		DisableFullScreen: true,
		DisableShadow:     true,
		SkipTaskbar:       c.bannerMode,
		IgnoreMouseEvents: c.bannerMode,
		Hidden:            true,
		Content:           ui.View(c.shell.View),
	})
	if w == nil {
		return errWindowCreate
	}
	c.attachWindow(w)
	return nil
}

func (c *controller) createOverlayWindow(targetHWND uintptr, bounds overlaywin.Rect) error {
	var w *mygo.Window
	err := overlaywin.WithThreadDPI(targetHWND, func() error {
		w = mygo.NewWindow(mygo.WindowOptions{
			Title:             "Oddward 国服登录",
			Width:             int(bounds.W),
			Height:            int(bounds.H),
			X:                 int(bounds.X),
			Y:                 int(bounds.Y),
			Frameless:         true,
			Transparent:       true,
			SkipTaskbar:       true,
			AlwaysOnTop:       false,
			DisableResize:     true,
			DisableMaximize:   true,
			DisableMinimize:   true,
			DisableFullScreen: true,
			DisableShadow:     true,
			IgnoreMouseEvents: c.bannerMode,
			Hidden:            true,
			Content:           ui.View(c.shell.View),
		})
		return nil
	})
	if err != nil {
		return err
	}
	if w == nil || w.NativeHandle() == 0 {
		return errWindowCreate
	}
	c.attachWindow(w)
	return nil
}

func (c *controller) attachWindow(w *mygo.Window) {
	c.win = w
	c.hwnd = w.NativeHandle()
	c.closeRequested = false
	c.hasPlaced = false
	c.ownerAttemptedFor = 0
	c.ownerRequested = false
	c.bindingRequest = nil
	c.bindingAcknowledged = false
	_ = w.SetIcon(branding.IconPNG)
	if c.bannerMode {
		c.applyBannerStyles()
	}
	if c.capturedForm != nil {
		c.shell.RestoreForm(*c.capturedForm)
		c.capturedForm = nil
	}
	w.OnClose(func(_ *mygo.CloseEvent) {
		c.closeRequested = true
	})
	w.OnClosed(func() {
		c.onWindowClosed(w)
	})
}

func (c *controller) onWindowClosed(w *mygo.Window) {
	if c.stopped || c.replacingView || c.win != w {
		return
	}
	c.win = nil
	c.hwnd = 0
	c.hasPlaced = false
	if c.closeRequested {
		if c.bannerMode {
			c.Stop(0)
			return
		}
		c.restoreGameFocus()
		_ = c.emit(loginui.Event{Kind: "cancel"})
		c.Stop(0)
		return
	}
	// WM_DESTROY arrived without WM_CLOSE/CloseRequested: the parent game HWND
	// was destroyed while we were a WS_CHILD. Never race to rebind back onto the
	// dying parent; preserve form state and recreate our view once the same game
	// process presents a replacement window.
	dead := c.gameWindow
	if dead == 0 {
		dead = c.ownerAttemptedFor
	}
	c.destroyedParent = dead
	c.gameWindow = 0
	c.ownerAttemptedFor = 0
	captured := c.shell.CaptureForm()
	c.capturedForm = &captured
	c.queueRecreation(50 * time.Millisecond)
}

func (c *controller) recreateView() {
	if c.stopped || c.replacingView || c.win == nil {
		return
	}
	c.replacingView = true
	captured := c.shell.CaptureForm()
	c.capturedForm = &captured
	old := c.win
	c.win = nil
	c.hwnd = 0
	c.hasPlaced = false
	c.ownerAttemptedFor = 0
	c.ownerRequested = false
	c.bindingRequest = nil
	c.bindingAcknowledged = false
	old.Close()
	c.replacingView = false
	c.queueRecreation(0)
}

func (c *controller) queueRecreation(delay time.Duration) {
	if c.stopped || c.recreating {
		return
	}
	if c.gameProcess == 0 {
		c.Stop(0)
		return
	}
	alive, err := overlaywin.ProcessAlive(c.gameProcess)
	if err != nil {
		c.Stop(1)
		return
	}
	if !alive {
		c.Stop(0)
		return
	}
	c.recreating = true
	run := func() {
		mygo.RunOnMain(func() {
			c.recreating = false
			if c.stopped {
				return
			}
			alive, err := overlaywin.ProcessAlive(c.gameProcess)
			if err != nil {
				c.Stop(1)
				return
			}
			if !alive {
				c.Stop(0)
				return
			}
			c.refreshSafely()
		})
	}
	if delay > 0 {
		if c.recreateTimer != nil {
			c.recreateTimer.Stop()
		}
		c.recreateTimer = time.AfterFunc(delay, run)
	} else {
		go run()
	}
}

func (c *controller) hideWindow() {
	if c.win != nil && c.win.IsVisible() {
		c.win.Hide()
	}
}

func (c *controller) beginBannerAnimation() {
	if c.bannerStarted || c.stopped {
		return
	}
	c.bannerStarted = true
	reduceMotion := !overlaywin.AnimationsEnabled()
	c.bannerStop = make(chan struct{})
	c.bannerDone = make(chan struct{})
	stopCh := c.bannerStop
	doneCh := c.bannerDone

	if reduceMotion {
		c.shell.BannerAlpha = 1
		c.shell.BannerAlphaSet = true
		if c.win != nil {
			c.win.Invalidate()
		}
		go func() {
			defer close(doneCh)
			timer := time.NewTimer(BannerHold)
			defer timer.Stop()
			select {
			case <-stopCh:
				return
			case <-timer.C:
				mygo.RunOnMain(c.dismissBanner)
			}
		}()
		return
	}

	start := time.Now()
	c.shell.BannerAlpha = 0
	c.shell.BannerAlphaSet = true
	if c.win != nil {
		c.win.Invalidate()
	}
	// Phase-scheduled banner animation: 16ms cadence only during the 220ms fade-in
	// and 380ms fade-out, and a single timer sleep during the 2.4s hold so idle
	// CPU and GPU redraws stay zero while the pill sits steady on screen.
	go func() {
		defer close(doneCh)
		animatePhase := func(end time.Duration) bool {
			ticker := time.NewTicker(16 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-stopCh:
					return false
				case <-ticker.C:
					elapsed := time.Since(start)
					alpha, done := BannerOpacity(elapsed, false)
					mygo.RunOnMain(func() {
						if c.stopped {
							return
						}
						c.shell.BannerAlpha = alpha
						c.shell.BannerAlphaSet = true
						if c.win != nil {
							c.win.Invalidate()
						}
					})
					if done || elapsed >= end {
						return true
					}
				}
			}
		}
		if !animatePhase(BannerFadeIn) {
			return
		}
		mygo.RunOnMain(func() {
			if c.stopped {
				return
			}
			c.shell.BannerAlpha = 1
			c.shell.BannerAlphaSet = true
			if c.win != nil {
				c.win.Invalidate()
			}
		})
		holdWait := time.Until(start.Add(BannerFadeIn + BannerHold))
		if holdWait > 0 {
			timer := time.NewTimer(holdWait)
			select {
			case <-stopCh:
				timer.Stop()
				return
			case <-timer.C:
			}
		}
		if !animatePhase(BannerTotal) {
			return
		}
		mygo.RunOnMain(c.dismissBanner)
	}()
}

func (c *controller) dismissBanner() {
	if c.stopped {
		return
	}
	c.closeRequested = true
	if c.win != nil {
		c.win.Close()
		return
	}
	c.Stop(0)
}

func (c *controller) restoreGameFocus() {
	if c.bannerMode || c.gameWindow == 0 {
		return
	}
	if overlaywin.IsWindow(c.gameWindow) && overlaywin.Visible(c.gameWindow) && !overlaywin.Minimized(c.gameWindow) {
		overlaywin.SetForeground(c.gameWindow)
	}
}

// Stop releases every native window, WinEvent hook, timer and process handle
// idempotently on the UI thread, then signals exit with code.
func (c *controller) Stop(code int) {
	c.stopOnce.Do(func() {
		c.stopped = true
		c.exitCode = code
		if c.recreateTimer != nil {
			c.recreateTimer.Stop()
			c.recreateTimer = nil
		}
		if c.bannerStop != nil {
			close(c.bannerStop)
		}
		if c.tickStop != nil {
			close(c.tickStop)
			c.tickRunning = false
		}
		if c.watcher != nil {
			c.watcher.Uninstall()
			c.watcher = nil
		}
		if c.win != nil {
			if !c.bannerMode {
				c.restoreGameFocus()
			}
			w := c.win
			c.win = nil
			c.hwnd = 0
			c.replacingView = true
			w.Close()
			c.replacingView = false
		}
		if c.exitStopEvent != 0 {
			_ = windows.SetEvent(c.exitStopEvent)
			if c.exitWaitDone != nil {
				<-c.exitWaitDone
			}
			_ = windows.CloseHandle(c.exitStopEvent)
			c.exitStopEvent = 0
		}
		if c.gameProcess != 0 {
			_ = overlaywin.CloseIdentity(c.gameProcess)
			c.gameProcess = 0
		}
		c.capturedForm = nil
		if c.onStop != nil {
			c.onStop(code)
		}
	})
}
