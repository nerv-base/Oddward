//go:build windows

package overlaywin

import (
	"fmt"
	"sync/atomic"

	"golang.org/x/sys/windows"
)

const (
	eventSystemMoveSizeStart  = 0x000A
	eventSystemMoveSizeEnd    = 0x000B
	eventObjectDestroy        = 0x8001
	eventObjectLocationChange = 0x800B

	wineventOutOfContext = 0x0000
)

// State is a snapshot of the target game window.
type State struct {
	HWND      uintptr
	Valid     bool
	Visible   bool
	Minimized bool
	Client    Rect
	DPI       uintptr
}

// Watcher tracks one verified game process and its main window. Hooks make it
// react to move/resize/destroy promptly; the caller also polls State because
// hooks are best-effort, exactly as the WPF overlay does.
type Watcher struct {
	identity Identity

	hwnd      atomic.Uintptr
	hooks     []uintptr
	callbacks atomic.Uint64
	installed atomic.Bool
}

// NewWatcher creates a watcher for a verified identity.
func NewWatcher(id Identity) (*Watcher, error) {
	if err := id.Verify(); err != nil {
		return nil, err
	}
	return &Watcher{identity: id}, nil
}

// Install installs the WinEvent hooks. It must be called on the thread that
// runs the message loop (the MyGo UI thread); callbacks are delivered there.
func (w *Watcher) Install() error {
	if w.installed.Load() {
		return nil
	}
	if hookCallback == 0 {
		hookCallback = windows.NewCallback(winEventProc)
	}
	ranges := [][2]uint32{
		{eventObjectLocationChange, eventObjectLocationChange},
		{eventSystemMoveSizeStart, eventSystemMoveSizeEnd},
		{eventObjectDestroy, eventObjectDestroy},
	}
	for _, r := range ranges {
		h, _, err := procSetWinEventHook.Call(uintptr(r[0]), uintptr(r[1]), 0, hookCallback, uintptr(w.identity.PID), 0, wineventOutOfContext)
		if h == 0 {
			w.Uninstall()
			return fmt.Errorf("overlay: SetWinEventHook(%#x): %w", r[0], err)
		}
		w.hooks = append(w.hooks, h)
	}
	currentWatcher.Store(w)
	w.installed.Store(true)
	return nil
}

// Uninstall removes the hooks. It must be called on the installing thread.
func (w *Watcher) Uninstall() {
	for _, h := range w.hooks {
		procUnhookWinEvent.Call(h)
	}
	w.hooks = nil
	w.installed.Store(false)
	currentWatcher.CompareAndSwap(w, nil)
}

// CallbackCount reports how many WinEvent callbacks this watcher observed.
func (w *Watcher) CallbackCount() uint64 { return w.callbacks.Load() }

// State reads the current target window state. It is safe from any goroutine.
func (w *Watcher) State() State {
	hwnd := w.hwnd.Load()
	if hwnd == 0 || !isWindow(hwnd) || ProcessOfWindow(hwnd) != w.identity.PID {
		hwnd = FindMainWindow(w.identity.PID)
		w.hwnd.Store(hwnd)
	}
	if hwnd == 0 || !isWindow(hwnd) {
		return State{Valid: false}
	}
	st := State{HWND: hwnd, Valid: true, DPI: DPI(hwnd)}
	if !Visible(hwnd) {
		return st
	}
	if Minimized(hwnd) {
		st.Minimized = true
		return st
	}
	client, err := ClientRectOf(hwnd)
	if err != nil || client.Empty() {
		return st
	}
	st.Visible = true
	st.Client = client
	return st
}

var (
	hookCallback        uintptr
	currentWatcher      atomic.Pointer[Watcher]
	procSetWinEventHook = user32.NewProc("SetWinEventHook")
	procUnhookWinEvent  = user32.NewProc("UnhookWinEvent")
	procIsWindow        = user32.NewProc("IsWindow")
)

func isWindow(hwnd uintptr) bool {
	v, _, _ := procIsWindow.Call(hwnd)
	return v != 0
}

func winEventProc(_ uintptr, event uint32, hwnd uintptr, idObject int32, idChild int32, _ uint32, _ uint32) uintptr {
	w := currentWatcher.Load()
	if w == nil || idObject != 0 || idChild != 0 {
		return 0
	}
	if hwnd != 0 && hwnd != w.hwnd.Load() && ProcessOfWindow(hwnd) != w.identity.PID {
		return 0
	}
	w.callbacks.Add(1)
	return 0
}
