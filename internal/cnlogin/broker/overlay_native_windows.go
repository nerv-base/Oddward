// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package broker

import (
	"runtime"
	"unsafe"

	"github.com/nerv-base/Oddward/internal/process"
	"golang.org/x/sys/windows"
)

var (
	overlayUser32      = windows.NewLazySystemDLL("user32.dll")
	overlayGetAncestor = overlayUser32.NewProc("GetAncestor")
	overlayGetWindow   = overlayUser32.NewProc("GetWindow")
	overlayIsVisible   = overlayUser32.NewProc("IsWindowVisible")
	overlayGetLong     = overlayUser32.NewProc("GetWindowLongPtrW")
	overlaySetLong     = overlayUser32.NewProc("SetWindowLongPtrW")
	overlayGetDesktop  = overlayUser32.NewProc("GetDesktopWindow")
	overlayGetParent   = overlayUser32.NewProc("GetParent")
	overlaySetParent   = overlayUser32.NewProc("SetParent")
	overlaySetPos      = overlayUser32.NewProc("SetWindowPos")
	overlayGetDPI      = overlayUser32.NewProc("GetWindowDpiAwarenessContext")
	overlayEqualDPI    = overlayUser32.NewProc("AreDpiAwarenessContextsEqual")
	overlayClearError  = windows.NewLazySystemDLL("kernel32.dll").NewProc("SetLastError")
)

type nativeOverlaySystem struct{}

func (nativeOverlaySystem) open(expected process.Identity) (overlayProcess, error) {
	if expected.PID == 0 || expected.CreationTime == 0 {
		return overlayProcess{}, errRemote
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, expected.PID)
	if err != nil {
		return overlayProcess{}, errRemote
	}
	accepted := false
	defer func() {
		if !accepted {
			_ = windows.CloseHandle(h)
		}
	}()
	alive := func() bool {
		state, err := windows.WaitForSingleObject(h, 0)
		return err == nil && state == uint32(windows.WAIT_TIMEOUT)
	}
	if !alive() {
		return overlayProcess{}, errRemote
	}
	pid, err := windows.GetProcessId(h)
	if err != nil || pid != expected.PID {
		return overlayProcess{}, errRemote
	}
	var creation, end, kernel, user windows.Filetime
	if windows.GetProcessTimes(h, &creation, &end, &kernel, &user) != nil || uint64(creation.HighDateTime)<<32|uint64(creation.LowDateTime) != expected.CreationTime {
		return overlayProcess{}, errRemote
	}
	var basic windows.PROCESS_BASIC_INFORMATION
	var size uint32
	if windows.NtQueryInformationProcess(h, windows.ProcessBasicInformation, unsafe.Pointer(&basic), uint32(unsafe.Sizeof(basic)), &size) != nil || size != uint32(unsafe.Sizeof(basic)) || basic.UniqueProcessId != uintptr(pid) || basic.InheritedFromUniqueProcessId > uintptr(^uint32(0)) {
		return overlayProcess{}, errRemote
	}
	var token windows.Token
	if windows.OpenProcessToken(h, windows.TOKEN_QUERY, &token) != nil {
		return overlayProcess{}, errRemote
	}
	defer token.Close()
	tokenUser, err := token.GetTokenUser()
	if err != nil || tokenUser.User.Sid == nil || !tokenUser.User.Sid.IsValid() {
		return overlayProcess{}, errRemote
	}
	scalar := func(kind uint32) (uint32, error) {
		var value, n uint32
		err := windows.GetTokenInformation(token, kind, (*byte)(unsafe.Pointer(&value)), 4, &n)
		if err != nil || n != 4 {
			return 0, errRemote
		}
		return value, nil
	}
	session, err := scalar(windows.TokenSessionId)
	if err != nil {
		return overlayProcess{}, errRemote
	}
	elevated, err := scalar(windows.TokenElevation)
	if err != nil {
		return overlayProcess{}, errRemote
	}
	uiAccess, err := scalar(windows.TokenUIAccess)
	if err != nil {
		return overlayProcess{}, errRemote
	}
	// The typed header ensures TOKEN_MANDATORY_LABEL alignment and lets Go track
	// its returned SID pointer. A byte array may be unaligned on the Go stack.
	var integrity struct {
		label windows.Tokenmandatorylabel
		sid   [256]byte
	}
	if windows.GetTokenInformation(token, windows.TokenIntegrityLevel, (*byte)(unsafe.Pointer(&integrity)), uint32(unsafe.Sizeof(integrity)), &size) != nil || size < uint32(unsafe.Sizeof(windows.Tokenmandatorylabel{})) || size > uint32(unsafe.Sizeof(integrity)) {
		return overlayProcess{}, errRemote
	}
	sid := integrity.label.Label.Sid
	if sid == nil || !sid.IsValid() || sid.SubAuthorityCount() == 0 {
		return overlayProcess{}, errRemote
	}
	rid := sid.SubAuthority(uint32(sid.SubAuthorityCount() - 1))
	p := overlayProcess{identity: expected, parentPID: uint32(basic.InheritedFromUniqueProcessId), sid: tokenUser.User.Sid.String(), session: session, ordinary: elevated == 0 && uiAccess == 0 && rid >= 0x2000 && rid < 0x3000, alive: alive, close: func() { _ = windows.CloseHandle(h) }}
	if !alive() {
		return overlayProcess{}, errRemote
	}
	accepted = true
	return p, nil
}

func overlayLong(window uintptr, index int32) (uintptr, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	overlayClearError.Call(0)
	value, _, err := overlayGetLong.Call(window, uintptr(index))
	if value == 0 && err != windows.ERROR_SUCCESS {
		return 0, errRemote
	}
	return value, nil
}
func (nativeOverlaySystem) window(window uintptr) (overlayWindow, error) {
	var pid uint32
	tid, err := windows.GetWindowThreadProcessId(windows.HWND(window), &pid)
	if err != nil || tid == 0 || pid == 0 {
		return overlayWindow{}, errRemote
	}
	ancestor, _, _ := overlayGetAncestor.Call(window, 2) // GA_ROOT excludes WS_CHILD windows, not owners.
	if ancestor == 0 {
		return overlayWindow{}, errRemote
	}
	style, err := overlayLong(window, -16)
	if err != nil {
		return overlayWindow{}, errRemote
	}
	exStyle, err := overlayLong(window, -20)
	if err != nil {
		return overlayWindow{}, errRemote
	}
	// GWL_STYLE is a 32-bit value even on 64-bit Windows.
	style = uintptr(uint32(style))
	owner, _, _ := overlayGetWindow.Call(window, 4) // GW_OWNER excludes child parents.
	var parent, dpi uintptr
	if style&overlayChild != 0 {
		parent, _, _ = overlayGetParent.Call(window)
		// Between changing WS_CHILD and SetParent, Windows reports the desktop.
		// Represent that detached state as SetParent's canonical NULL parent.
		desktop, _, _ := overlayGetDesktop.Call()
		if parent == desktop {
			parent = 0
		}
	}
	if overlayGetDPI.Find() == nil {
		dpi, _, _ = overlayGetDPI.Call(window)
	}
	visible, _, _ := overlayIsVisible.Call(window)
	var finalPID uint32
	finalTID, err := windows.GetWindowThreadProcessId(windows.HWND(window), &finalPID)
	if err != nil || finalTID != tid || finalPID != pid {
		return overlayWindow{}, errRemote
	}
	return overlayWindow{pid: pid, tid: tid, owner: owner, parent: parent, style: style, dpi: dpi, topLevel: ancestor == window && style&overlayChild == 0, visible: visible != 0, tool: exStyle&0x80 != 0, topmost: exStyle&0x8 != 0}, nil
}
func overlayWriteLong(window uintptr, index int32, value uintptr) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	overlayClearError.Call(0)
	previous, _, err := overlaySetLong.Call(window, uintptr(index), value)
	if previous == 0 && err != windows.ERROR_SUCCESS {
		return errRemote
	}
	return nil
}
func (nativeOverlaySystem) setOwner(window, owner uintptr) error {
	return overlayWriteLong(window, -8, owner) // GWLP_HWNDPARENT on verified top-level UI.
}
func (nativeOverlaySystem) sameDPI(a, b uintptr) bool {
	if a == 0 || b == 0 || overlayEqualDPI.Find() != nil {
		return false
	}
	equal, _, _ := overlayEqualDPI.Call(a, b)
	return equal != 0
}
func (nativeOverlaySystem) setStyle(window, style uintptr) error {
	return overlayWriteLong(window, -16, uintptr(uint32(style)))
}
func (nativeOverlaySystem) setParent(window, parent uintptr) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	overlayClearError.Call(0)
	previous, _, err := overlaySetParent.Call(window, parent)
	if previous == 0 && err != windows.ERROR_SUCCESS {
		return errRemote
	}
	return nil
}
func (nativeOverlaySystem) frameChanged(window uintptr) error {
	// NOSIZE | NOMOVE | NOZORDER | NOACTIVATE | FRAMECHANGED | NOOWNERZORDER.
	ok, _, _ := overlaySetPos.Call(window, 0, 0, 0, 0, 0, 0x237)
	if ok == 0 {
		return errRemote
	}
	return nil
}
