// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package sdkhost

import (
	"errors"
	"fmt"
	"github.com/nerv-base/Oddward/internal/cnlogin/cnproto"
	"golang.org/x/sys/windows"
	"runtime"
	"sync"
	"unsafe"
)

const (
	wmClose         = 0x10
	wmDestroy       = 2
	wmCopyData      = 0x4a
	wmLoginComplete = 0x8000 + 1
)

var (
	winUser             = windows.NewLazySystemDLL("user32.dll")
	winKernel           = windows.NewLazySystemDLL("kernel32.dll")
	winOLE              = windows.NewLazySystemDLL("ole32.dll")
	winRegisterClass    = winUser.NewProc("RegisterClassExW")
	winUnregisterClass  = winUser.NewProc("UnregisterClassW")
	winRegisterMessage  = winUser.NewProc("RegisterWindowMessageW")
	winCreateWindow     = winUser.NewProc("CreateWindowExW")
	winDestroyWindow    = winUser.NewProc("DestroyWindow")
	winDefWindowProc    = winUser.NewProc("DefWindowProcW")
	winPostMessage      = winUser.NewProc("PostMessageW")
	winPostQuit         = winUser.NewProc("PostQuitMessage")
	winGetMessage       = winUser.NewProc("GetMessageW")
	winTranslateMessage = winUser.NewProc("TranslateMessage")
	winDispatchMessage  = winUser.NewProc("DispatchMessageW")
	winWindowPID        = winUser.NewProc("GetWindowThreadProcessId")
	winSendTimeout      = winUser.NewProc("SendMessageTimeoutW")
	winModuleHandle     = winKernel.NewProc("GetModuleHandleW")
	winCoInitialize     = winOLE.NewProc("CoInitializeEx")
	winCoUninitialize   = winOLE.NewProc("CoUninitialize")
	hostWindows         sync.Map
	hostWindowProc      = windows.NewCallback(hostWndProc)
)

type windowClass struct {
	Size, Style                        uint32
	Proc                               uintptr
	ClassExtra, WindowExtra            int32
	Instance, Icon, Cursor, Background uintptr
	Menu, Name                         *uint16
	SmallIcon                          uintptr
}
type windowMessage struct {
	Window         uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	X, Y           int32
	Private        uint32
}

func (h *Host) windowLoop(id string, ready chan<- error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	defer close(h.windowDone)
	hr, _, _ := winCoInitialize.Call(0, 2)
	if int32(hr) < 0 {
		ready <- fmt.Errorf("sdkhost: initialize window apartment: %#x", uint32(hr))
		return
	}
	defer winCoUninitialize.Call()
	class, _ := windows.UTF16PtrFromString("Collapse.NTE.SdkProbe." + id)
	titleText := "Collapse NTE SDK Probe " + id
	title, _ := windows.UTF16PtrFromString(titleText)
	notify, _ := windows.UTF16PtrFromString("Collapse.NTE.SdkProbe." + id + ".Notify")
	module, _, _ := winModuleHandle.Call(0)
	wc := windowClass{Proc: hostWindowProc, Instance: module, Name: class}
	wc.Size = uint32(unsafe.Sizeof(wc))
	atom, _, err := winRegisterClass.Call(uintptr(unsafe.Pointer(&wc)))
	if atom == 0 {
		ready <- fmt.Errorf("sdkhost: register window class: %w", err)
		return
	}
	defer winUnregisterClass.Call(uintptr(unsafe.Pointer(class)), module)
	msg, _, err := winRegisterMessage.Call(uintptr(unsafe.Pointer(notify)))
	if msg == 0 {
		ready <- fmt.Errorf("sdkhost: register notification: %w", err)
		return
	}
	hwnd, _, err := winCreateWindow.Call(0, uintptr(unsafe.Pointer(class)), uintptr(unsafe.Pointer(title)), 0, 0, 0, 0, 0, 0, 0, module, 0)
	if hwnd == 0 {
		ready <- fmt.Errorf("sdkhost: create host window: %w", err)
		return
	}
	h.hwnd = hwnd
	h.env.NotifyMessage = uint32(msg)
	h.env.AnchorW = cnproto.AnchorW(hwnd)
	h.env.AnchorV = cnproto.AnchorV(titleText)
	hostWindows.Store(hwnd, h)
	defer hostWindows.Delete(hwnd)
	ready <- nil
	var m windowMessage
	for {
		result, _, err := winGetMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(result) == -1 {
			h.fail(fmt.Errorf("sdkhost: window message loop: %w", err))
			winDestroyWindow.Call(hwnd)
			return
		}
		if result == 0 {
			return
		}
		winTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		winDispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func hostWndProc(hwnd, msg, wparam, lparam uintptr) uintptr {
	switch msg {
	case wmClose:
		winDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		winPostQuit.Call(0)
		return 0
	case wmCopyData:
		return 0 // Unsolicited payloads are never acknowledged or retained.
	}
	if value, ok := hostWindows.Load(hwnd); ok {
		h := value.(*Host)
		if msg == wmLoginComplete {
			h.loginPending.Store(false)
			return 0
		}
		if uint32(msg) == h.env.NotifyMessage {
			h.notify(wparam)
			return 0
		}
	}
	result, _, _ := winDefWindowProc.Call(hwnd, msg, wparam, lparam)
	return result
}

func ownedWindow(hwnd uintptr, pid uint32) bool {
	var actual uint32
	thread, _, _ := winWindowPID.Call(hwnd, uintptr(unsafe.Pointer(&actual)))
	return hwnd != 0 && thread != 0 && actual == pid
}

type copyData struct {
	Tag  uintptr
	Size uint32
	Data *byte
}

func (h *Host) sendPacket(ctxErr error, packet []byte) error {
	if ctxErr != nil {
		return ctxErr
	}
	h.mu.Lock()
	closed, pid, target, game := h.closed, h.gamePID, h.callback, h.game
	h.mu.Unlock()
	if closed {
		return ErrClosed
	}
	if target == 0 {
		return ErrNotRegistered
	}
	state, err := windows.WaitForSingleObject(game, 0)
	if err != nil || state != uint32(windows.WAIT_TIMEOUT) || !ownedWindow(h.hwnd, h.env.HostPID) || !ownedWindow(target, pid) {
		return ErrIdentity
	}
	data := copyData{Tag: cnproto.CopyDataTag, Size: uint32(len(packet)), Data: &packet[0]}
	var result uintptr
	sent, _, _ := winSendTimeout.Call(target, wmCopyData, h.hwnd, uintptr(unsafe.Pointer(&data)), 0x23, 5000, uintptr(unsafe.Pointer(&result)))
	runtime.KeepAlive(packet)
	if sent == 0 {
		return errors.New("sdkhost: SDK callback delivery did not complete")
	}
	return nil
}
