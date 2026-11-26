// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package overlaywin

import (
	"fmt"
	"sync"
	"sync/atomic"

	"golang.org/x/sys/windows"
)

const (
	eventSystemMoveSizeStart  = 0x000A
	eventSystemMoveSizeEnd    = 0x000B
	eventSystemMinimizeStart  = 0x0016
	eventSystemMinimizeEnd    = 0x0017
	eventObjectDestroy        = 0x8001
	eventObjectShow           = 0x8002
	eventObjectHide           = 0x8003
	eventObjectLocationChange = 0x800B

	wineventOutOfContext = 0x0000
)

// Watcher tracks one verified game process and its main window. OUTOFCONTEXT
// WinEvent hooks react to move, resize, show, hide, minimize and destroy
// immediately without DLL injection; callers may pair hooks with a low-rate
// health check for process lifetime and late window creation.
type Watcher struct {
	identity Identity

	hooks     []uintptr
	installed atomic.Bool
	onEvent   atomic.Pointer[func()]
}

// NewWatcher creates a watcher for a verified identity.
func NewWatcher(id Identity) (*Watcher, error) {
	if err := id.Verify(); err != nil {
		return nil, err
	}
	return &Watcher{identity: id}, nil
}

// SetOnEvent registers a callback invoked on the installing UI thread whenever
// a matching WinEvent arrives for the watched process. Pass nil to clear.
func (w *Watcher) SetOnEvent(fn func()) {
	if fn == nil {
		w.onEvent.Store(nil)
		return
	}
	w.onEvent.Store(&fn)
}

// Install installs the WinEvent hooks. It must be called on the thread that
// runs the message loop (the MyGo UI thread); callbacks are delivered there.
func (w *Watcher) Install() error {
	if w.installed.Load() {
		return nil
	}
	hookOnce.Do(func() {
		hookCallback = windows.NewCallback(winEventProc)
	})
	ranges := [][2]uint32{
		{eventObjectLocationChange, eventObjectLocationChange},
		{eventSystemMoveSizeStart, eventSystemMoveSizeEnd},
		{eventSystemMinimizeStart, eventSystemMinimizeEnd},
		{eventObjectDestroy, eventObjectHide},
	}
	for _, r := range ranges {
		h, _, err := procSetWinEventHook.Call(uintptr(r[0]), uintptr(r[1]), 0, hookCallback, uintptr(w.identity.PID), 0, wineventOutOfContext)
		if h == 0 {
			w.Uninstall()
			return fmt.Errorf("overlay: SetWinEventHook(%#x): %w", r[0], err)
		}
		w.hooks = append(w.hooks, h)
		watcherMu.Lock()
		watchersByHook[h] = w
		watcherMu.Unlock()
	}
	w.installed.Store(true)
	return nil
}

// Uninstall removes the hooks. It must be called on the installing thread.
func (w *Watcher) Uninstall() {
	for _, h := range w.hooks {
		watcherMu.Lock()
		delete(watchersByHook, h)
		watcherMu.Unlock()
		procUnhookWinEvent.Call(h)
	}
	w.hooks = nil
	w.installed.Store(false)
}

var (
	hookOnce            sync.Once
	hookCallback        uintptr
	watcherMu           sync.RWMutex
	watchersByHook      = map[uintptr]*Watcher{}
	procSetWinEventHook = user32.NewProc("SetWinEventHook")
	procUnhookWinEvent  = user32.NewProc("UnhookWinEvent")
)

func winEventProc(hook uintptr, event uint32, hwnd uintptr, idObject int32, idChild int32, _ uint32, _ uint32) uintptr {
	if idObject != 0 || idChild != 0 {
		return 0
	}
	watcherMu.RLock()
	w := watchersByHook[hook]
	watcherMu.RUnlock()
	if w == nil {
		return 0
	}
	// Ignore accessibility descendants and windows from other processes.
	if hwnd != 0 && ProcessOfWindow(hwnd) != w.identity.PID {
		return 0
	}
	if fn := w.onEvent.Load(); fn != nil && *fn != nil {
		(*fn)()
	}
	return 0
}
