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
	procIsWindowVisible          = user32.NewProc("IsWindowVisible")
	procIsIconic                 = user32.NewProc("IsIconic")
	procSetWindowPos             = user32.NewProc("SetWindowPos")
	procSetParent                = user32.NewProc("SetParent")
	procGetWindowThreadProcessID = user32.NewProc("GetWindowThreadProcessId")
	procEnumWindows              = user32.NewProc("EnumWindows")
	procGetWindowDpiAwareness    = user32.NewProc("GetWindowDpiAwarenessContext")
	procSetThreadDpiAwareness    = user32.NewProc("SetThreadDpiAwarenessContext")
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

	WSChild = 0x40000000
	WSPopup = 0x80000000

	WSExTransparent = 0x00000020
	WSExToolWindow  = 0x00000080
	WSExNoActivate  = 0x08000000

	SWPNoZOrder      = 0x0004
	SWPNoActivate    = 0x0010
	SWPNoOwnerZOrder = 0x0200
	SWPFrameChanged  = 0x0020

	DWMWACloaked = 14
)

// Rect is a rectangle in physical pixels.
type Rect struct {
	X, Y, W, H int32
}

// Empty reports whether the rectangle has no area.
func (r Rect) Empty() bool { return r.W <= 0 || r.H <= 0 }

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

// Style returns GWL_STYLE of hwnd.
func Style(hwnd uintptr) (uintptr, error) {
	idx := int32(GWLStyle)
	v, _, err := procGetWindowLongPtr.Call(hwnd, uintptr(idx))
	if v == 0 && err != nil && err != windows.ERROR_SUCCESS {
		return 0, fmt.Errorf("overlay: GetWindowLongPtr(style): %w", err)
	}
	return v, nil
}

// ExStyle returns GWL_EXSTYLE of hwnd.
func ExStyle(hwnd uintptr) (uintptr, error) {
	idx := int32(GWLExStyle)
	v, _, err := procGetWindowLongPtr.Call(hwnd, uintptr(idx))
	if v == 0 && err != nil && err != windows.ERROR_SUCCESS {
		return 0, fmt.Errorf("overlay: GetWindowLongPtr(exstyle): %w", err)
	}
	return v, nil
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

// Visible reports whether hwnd is visible.
func Visible(hwnd uintptr) bool {
	v, _, _ := procIsWindowVisible.Call(hwnd)
	return v != 0
}

// Minimized reports whether hwnd is minimized.
func Minimized(hwnd uintptr) bool {
	v, _, _ := procIsIconic.Call(hwnd)
	return v != 0
}

// ProcessOfWindow returns the process id owning hwnd.
func ProcessOfWindow(hwnd uintptr) uint32 {
	var pid uint32
	procGetWindowThreadProcessID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	return pid
}

// SetForeground asks Windows to bring hwnd to the foreground.
func SetForeground(hwnd uintptr) bool {
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
func SetChild(own, target uintptr, client Rect) error {
	style, err := Style(own)
	if err != nil {
		return err
	}
	if style&WSChild != 0 {
		return errors.New("overlay: window is already a child")
	}
	if err := setLong(own, GWLStyle, style&^WSPopup|WSChild); err != nil {
		return err
	}
	if ok, _, err := procSetParent.Call(own, target); ok == 0 {
		return fmt.Errorf("overlay: SetParent: %w", err)
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
	idx := index
	v, _, err := procSetWindowLongPtr.Call(hwnd, uintptr(idx), value)
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

// DPI returns the window's DPI awareness context.
func DPI(hwnd uintptr) uintptr {
	v, _, _ := procGetWindowDpiAwareness.Call(hwnd)
	return v
}

// WithThreadDPI runs fn with the thread's DPI awareness context set to the
// target window's context, then restores the previous one. It must run on the
// thread that creates the overlay window. Only synchronous Win32 work belongs
// in fn; creating an HWND is the intended use.
func WithThreadDPI(target uintptr, fn func() error) error {
	ctx := DPI(target)
	if ctx == 0 {
		return fn()
	}
	prev, _, _ := procSetThreadDpiAwareness.Call(ctx)
	if prev == 0 {
		return errors.New("overlay: SetThreadDpiAwarenessContext failed")
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
	ok, _, _ := procSystemParametersInfo.Call(0x1042 /* SPI_GETCLIENTAREAANIMATION */, 0, uintptr(unsafe.Pointer(&on)), 0)
	return ok != 0 && on != 0
}

// IsCloaked reports whether DWM hides hwnd (for example on another virtual
// desktop or a suspended UWP host).
func IsCloaked(hwnd uintptr) bool {
	var cloaked uint32
	procDwmGetWindowAttribute.Call(hwnd, DWMWACloaked, uintptr(unsafe.Pointer(&cloaked)), unsafe.Sizeof(cloaked))
	return cloaked != 0
}

// Candidate checks the window-level half of the identity contract: visible,
// top-level, owned by nobody, not a tool window and not cloaked.
func Candidate(hwnd uintptr) bool {
	if hwnd == 0 || !Visible(hwnd) || IsCloaked(hwnd) {
		return false
	}
	style, err := Style(hwnd)
	if err != nil || style&WSChild != 0 {
		return false
	}
	ex, err := ExStyle(hwnd)
	if err != nil || ex&WSExToolWindow != 0 {
		return false
	}
	return Owner(hwnd) == 0
}

var enumOnce sync.Once
var enumCallback uintptr

type enumItem struct {
	hwnd uintptr
	area int64
}

// FindMainWindow returns the largest top-level, visible, unowned window of pid.
func FindMainWindow(pid uint32) uintptr {
	enumOnce.Do(func() {
		enumCallback = windows.NewCallback(func(hwnd uintptr, _ uintptr) uintptr {
			best := enumBest
			if ProcessOfWindow(hwnd) != enumPID || !Candidate(hwnd) {
				return 1
			}
			r, err := ClientRectOf(hwnd)
			if err != nil || r.Empty() {
				return 1
			}
			if area := int64(r.W) * int64(r.H); area > best.area {
				best = enumItem{hwnd: hwnd, area: area}
			}
			enumBest = best
			return 1
		})
	})
	enumPID, enumBest = pid, enumItem{}
	procEnumWindows.Call(enumCallback, 0)
	return enumBest.hwnd
}

var (
	enumPID  uint32
	enumBest enumItem
)
