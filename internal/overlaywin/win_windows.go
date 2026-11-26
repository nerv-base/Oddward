// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package overlaywin

import (
	"errors"
	"fmt"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	user32                       = windows.NewLazySystemDLL("user32.dll")
	kernel32                     = windows.NewLazySystemDLL("kernel32.dll")
	procGetClientRect            = user32.NewProc("GetClientRect")
	procClientToScreen           = user32.NewProc("ClientToScreen")
	procGetWindowRect            = user32.NewProc("GetWindowRect")
	procGetWindowLongPtr         = user32.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtr         = user32.NewProc("SetWindowLongPtrW")
	procGetParent                = user32.NewProc("GetParent")
	procGetWindow                = user32.NewProc("GetWindow")
	procIsWindow                 = user32.NewProc("IsWindow")
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procIsIconic                 = user32.NewProc("IsIconic")
	procSetWindowPos             = user32.NewProc("SetWindowPos")
	procSetParent                = user32.NewProc("SetParent")
	procGetWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
	procEnumWindows              = user32.NewProc("EnumWindows")
	procEnumChildWindows         = user32.NewProc("EnumChildWindows")
	procGetClassNameW            = user32.NewProc("GetClassNameW")
	procGetWindowDpiAwareness    = user32.NewProc("GetWindowDpiAwarenessContext")
	procSetThreadDpiAwareness    = user32.NewProc("SetThreadDpiAwarenessContext")
	procDpiContextsEqual         = user32.NewProc("AreDpiAwarenessContextsEqual")
	procSetForegroundWindow      = user32.NewProc("SetForegroundWindow")
	procSystemParametersInfo     = user32.NewProc("SystemParametersInfoW")
	procSetLastError             = kernel32.NewProc("SetLastError")
	dwmapi                       = windows.NewLazySystemDLL("dwmapi.dll")
	procDwmGetWindowAttribute    = dwmapi.NewProc("DwmGetWindowAttribute")
)

const (
	GWLStyle   = -16
	GWLExStyle = -20
	GWLOwner   = -8

	WSChild       = 0x40000000
	WSPopup       = 0x80000000
	WSVisible     = 0x10000000
	WSCaption     = 0x00C00000
	WSSysMenu     = 0x00080000
	WSThickFrame  = 0x00040000
	WSMinimizeBox = 0x00020000
	WSMaximizeBox = 0x00010000

	// ChildDecorationMask is the set of top-level styles stripped when turning
	// a frameless window into WS_CHILD so user32 draws no classic child frame.
	ChildDecorationMask = WSPopup | WSCaption | WSSysMenu | WSThickFrame | WSMinimizeBox | WSMaximizeBox

	WSExTopmost     = 0x00000008
	WSExTransparent = 0x00000020
	WSExToolWindow  = 0x00000080
	WSExLayered     = 0x00080000
	WSExNoActivate  = 0x08000000

	SWPNoZOrder      = 0x0004
	SWPNoActivate    = 0x0010
	SWPNoOwnerZOrder = 0x0200
	SWPFrameChanged  = 0x0020

	DWMWACloaked = 14

	spiGetClientAreaAnimation = 0x1042
)

// Rect is a rectangle in physical pixels.
type Rect struct {
	X, Y, W, H int32
}

// Empty reports whether the rectangle has no area.
func (r Rect) Empty() bool { return r.W <= 0 || r.H <= 0 }

// IsWindow reports whether hwnd refers to a live native window.
func IsWindow(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	v, _, _ := procIsWindow.Call(hwnd)
	return v != 0
}

// RectOf reads a window's screen rectangle.
func RectOf(hwnd uintptr) (Rect, error) {
	var r struct{ L, T, R, B int32 }
	if ok, _, err := procGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ok == 0 {
		return Rect{}, fmt.Errorf("overlay: GetWindowRect: %w", err)
	}
	return Rect{X: r.L, Y: r.T, W: r.R - r.L, H: r.B - r.T}, nil
}

// ClientRectOf reads the client area of hwnd in screen coordinates.
func ClientRectOf(hwnd uintptr) (Rect, error) {
	var r struct{ L, T, R, B int32 }
	if ok, _, err := procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&r))); ok == 0 {
		return Rect{}, fmt.Errorf("overlay: GetClientRect: %w", err)
	}
	pt := struct{ X, Y int32 }{}
	if ok, _, err := procClientToScreen.Call(hwnd, uintptr(unsafe.Pointer(&pt))); ok == 0 {
		return Rect{}, fmt.Errorf("overlay: ClientToScreen: %w", err)
	}
	return Rect{X: pt.X, Y: pt.Y, W: r.R - r.L, H: r.B - r.T}, nil
}

func getLong(hwnd uintptr, index int32) (uintptr, error) {
	runtimeLock()
	defer runtimeUnlock()
	procSetLastError.Call(0)
	v, _, err := procGetWindowLongPtr.Call(hwnd, uintptr(index))
	if v == 0 && err != nil && err != windows.ERROR_SUCCESS {
		return 0, fmt.Errorf("overlay: GetWindowLongPtr(%d): %w", index, err)
	}
	return v, nil
}

// Style returns GWL_STYLE of hwnd.
func Style(hwnd uintptr) (uintptr, error) {
	v, err := getLong(hwnd, GWLStyle)
	if err != nil {
		return 0, err
	}
	return uintptr(uint32(v)), nil
}

// ExStyle returns GWL_EXSTYLE of hwnd.
func ExStyle(hwnd uintptr) (uintptr, error) {
	return getLong(hwnd, GWLExStyle)
}

// Owner returns the GW_OWNER of hwnd.
func Owner(hwnd uintptr) uintptr {
	v, _, _ := procGetWindow.Call(hwnd, 4)
	return v
}

// Parent returns the parent of hwnd.
func Parent(hwnd uintptr) uintptr {
	v, _, _ := procGetParent.Call(hwnd)
	return v
}

// ChildOf reports whether own is a confirmed WS_CHILD of parent without WS_POPUP.
func ChildOf(own, parent uintptr) bool {
	if own == 0 || parent == 0 {
		return false
	}
	style, err := Style(own)
	if err != nil {
		return false
	}
	return style&WSChild != 0 && style&WSPopup == 0 && Parent(own) == parent
}

// HasChildStyle reports whether hwnd currently has WS_CHILD set.
func HasChildStyle(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	style, err := Style(hwnd)
	return err == nil && style&WSChild != 0
}

// Visible reports whether hwnd is visible.
func Visible(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	v, _, _ := procIsWindowVisible.Call(hwnd)
	return v != 0
}

// Minimized reports whether hwnd is minimized.
func Minimized(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	v, _, _ := procIsIconic.Call(hwnd)
	return v != 0
}

// ProcessOfWindow returns the process id owning hwnd.
func ProcessOfWindow(hwnd uintptr) uint32 {
	if hwnd == 0 {
		return 0
	}
	var pid uint32
	procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	return pid
}

// SetForeground asks Windows to bring hwnd to the foreground.
func SetForeground(hwnd uintptr) bool {
	if hwnd == 0 {
		return false
	}
	v, _, _ := procSetForegroundWindow.Call(hwnd)
	return v != 0
}

// SetPos moves and resizes hwnd without changing z-order or activation.
func SetPos(hwnd uintptr, r Rect, flags uintptr) error {
	if ok, _, err := procSetWindowPos.Call(hwnd, 0, uintptr(r.X), uintptr(r.Y), uintptr(r.W), uintptr(r.H), flags|SWPNoZOrder|SWPNoActivate|SWPNoOwnerZOrder); ok == 0 {
		return fmt.Errorf("overlay: SetWindowPos: %w", err)
	}
	return nil
}

// SetOwner makes target the owner of own (GWLP_HWNDPARENT). Both windows stay
// top-level; Windows keeps the owned window above its owner.
func SetOwner(own, target uintptr) error {
	if err := setLong(own, GWLOwner, target); err != nil {
		return err
	}
	if got := Owner(own); got != target {
		return fmt.Errorf("overlay: owner readback %#x, want %#x", got, target)
	}
	return nil
}

// SetChild reparents own into target as a WS_CHILD window and sizes it to the
// target client area. This mirrors the broker's embed_overlay sequence.
//
// SetParent returns the previous parent, which is NULL for a top-level window,
// so a zero result is success and only the last error tells otherwise.
func SetChild(own, target uintptr, client Rect) error {
	style, err := Style(own)
	if err != nil {
		return err
	}
	if style&WSChild != 0 {
		return errors.New("overlay: window is already a child")
	}
	style = style&^ChildDecorationMask | WSChild
	if err := setLong(own, GWLStyle, style); err != nil {
		return err
	}
	runtimeLock()
	procSetLastError.Call(0)
	old, _, callErr := procSetParent.Call(own, target)
	runtimeUnlock()
	if old == 0 && callErr != nil && callErr != windows.ERROR_SUCCESS {
		return fmt.Errorf("overlay: SetParent: %w", callErr)
	}
	if got := Parent(own); got != target {
		return fmt.Errorf("overlay: parent readback %#x, want %#x", got, target)
	}
	return SetPos(own, Rect{W: client.W, H: client.H}, SWPFrameChanged)
}

func setLong(hwnd uintptr, index int32, value uintptr) error {
	runtimeLock()
	defer runtimeUnlock()
	procSetLastError.Call(0)
	v, _, err := procSetWindowLongPtr.Call(hwnd, uintptr(index), value)
	if v == 0 && err != nil && err != windows.ERROR_SUCCESS {
		return fmt.Errorf("overlay: SetWindowLongPtr(%d): %w", index, err)
	}
	return nil
}

// MakeBannerStyle adds WS_EX_TRANSPARENT | WS_EX_TOOLWINDOW | WS_EX_NOACTIVATE
// so a welcome banner never takes focus, appears in the taskbar or intercepts a
// click meant for the game.
func MakeBannerStyle(hwnd uintptr) (uintptr, error) {
	before, err := ExStyle(hwnd)
	if err != nil {
		return 0, err
	}
	if err := setLong(hwnd, GWLExStyle, before|WSExTransparent|WSExToolWindow|WSExNoActivate); err != nil {
		return before, err
	}
	return before, nil
}

// DPI returns the window's DPI awareness context, or 0 if unavailable.
func DPI(hwnd uintptr) uintptr {
	if hwnd == 0 || procGetWindowDpiAwareness.Find() != nil {
		return 0
	}
	v, _, _ := procGetWindowDpiAwareness.Call(hwnd)
	return v
}

// SameDPI reports whether two windows share one DPI awareness context.
// GetDpiForWindow alone is not enough: two contexts can report the same DPI
// and still differ, and a window may not be DPI aware at all.
func SameDPI(a, b uintptr) bool {
	ca, cb := DPI(a), DPI(b)
	if ca == 0 || cb == 0 || procDpiContextsEqual.Find() != nil {
		return false
	}
	ok, _, _ := procDpiContextsEqual.Call(ca, cb)
	return ok != 0
}

// WithThreadDPI runs fn with the thread's DPI awareness context set to the
// target window's context, then restores the previous one. It must run on the
// thread that creates the overlay window. Only synchronous Win32 work belongs
// in fn; creating an HWND is the intended use.
func WithThreadDPI(target uintptr, fn func() error) error {
	ctx := DPI(target)
	if ctx == 0 || procSetThreadDpiAwareness.Find() != nil {
		return fn()
	}
	prev, _, _ := procSetThreadDpiAwareness.Call(ctx)
	if prev == 0 {
		// Unsupported or invalid context: fall back to the default thread context
		// so the broker can safely choose owner mode instead of failing creation.
		return fn()
	}
	defer func() {
		procSetThreadDpiAwareness.Call(prev)
	}()
	return fn()
}

// AnimationsEnabled reports the user's "Show animations in Windows" setting.
// It is false when Windows is asked to reduce motion.
func AnimationsEnabled() bool {
	var on int32
	ok, _, _ := procSystemParametersInfo.Call(spiGetClientAreaAnimation, 0, uintptr(unsafe.Pointer(&on)), 0)
	return ok != 0 && on != 0
}

// IsCloaked reports whether DWM hides hwnd (for example on another virtual
// desktop or a suspended UWP host).
func IsCloaked(hwnd uintptr) bool {
	var cloaked uint32
	hr, _, _ := procDwmGetWindowAttribute.Call(hwnd, DWMWACloaked, uintptr(unsafe.Pointer(&cloaked)), unsafe.Sizeof(cloaked))
	return hr == 0 && cloaked != 0
}

// Candidate checks the window-level half of the identity contract: visible,
// top-level, owned by nobody, not a tool/no-activate/click-through window (such
// as Windows 11's UAC Input Indicator overlays) and not cloaked.
func Candidate(hwnd uintptr) bool {
	if hwnd == 0 || !Visible(hwnd) || IsCloaked(hwnd) {
		return false
	}
	style, err := Style(hwnd)
	if err != nil || style&WSChild != 0 {
		return false
	}
	ex, err := ExStyle(hwnd)
	if err != nil || ex&(WSExToolWindow|WSExNoActivate|WSExTransparent) != 0 {
		return false
	}
	return Owner(hwnd) == 0
}

// CandidateOf checks Candidate(hwnd) and verifies that hwnd belongs to pid.
func CandidateOf(hwnd uintptr, pid uint32) bool {
	return pid != 0 && Candidate(hwnd) && ProcessOfWindow(hwnd) == pid
}

var (
	enumMu       sync.Mutex
	enumOnce     sync.Once
	enumCallback uintptr
	enumPID      uint32
	enumBest     enumItem
)

type enumItem struct {
	hwnd uintptr
	area int64
}

// FindMainWindow returns the largest top-level, visible, unowned window of pid.
func FindMainWindow(pid uint32) uintptr {
	if pid == 0 {
		return 0
	}
	enumMu.Lock()
	defer enumMu.Unlock()
	enumOnce.Do(func() {
		enumCallback = windows.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
			if ProcessOfWindow(hwnd) != enumPID || !Candidate(hwnd) {
				return 1
			}
			r, err := ClientRectOf(hwnd)
			if err != nil || r.Empty() {
				return 1
			}
			if area := int64(r.W) * int64(r.H); area > enumBest.area {
				enumBest = enumItem{hwnd: hwnd, area: area}
			}
			return 1
		})
	})
	enumPID, enumBest = pid, enumItem{}
	procEnumWindows.Call(enumCallback, 0)
	return enumBest.hwnd
}

var (
	childEnumMu       sync.Mutex
	childEnumOnce     sync.Once
	childEnumCallback uintptr
	childEnumTarget   string
	childEnumFound    uintptr
)

// FindChildClass finds the first descendant window of parent with className.
func FindChildClass(parent uintptr, className string) uintptr {
	if parent == 0 || className == "" {
		return 0
	}
	childEnumMu.Lock()
	defer childEnumMu.Unlock()
	childEnumOnce.Do(func() {
		childEnumCallback = windows.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
			var buf [64]uint16
			n, _, _ := procGetClassNameW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
			if n > 0 && windows.UTF16ToString(buf[:n]) == childEnumTarget {
				childEnumFound = hwnd
				return 0
			}
			return 1
		})
	})
	childEnumTarget, childEnumFound = className, 0
	procEnumChildWindows.Call(parent, childEnumCallback, 0)
	return childEnumFound
}
