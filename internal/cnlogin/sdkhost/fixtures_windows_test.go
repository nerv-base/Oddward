// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package sdkhost

import (
	"encoding/binary"
	"golang.org/x/sys/windows"
	"unicode/utf16"
	"unsafe"
)

var testUser32 = windows.NewLazySystemDLL("user32.dll")
var getParent = testUser32.NewProc("GetParent")
var isWindowVisible = testUser32.NewProc("IsWindowVisible")
var postMessage = testUser32.NewProc("PostMessageW")
var sendMessageTimeout = testUser32.NewProc("SendMessageTimeoutW")
var testWindowProcess = testUser32.NewProc("GetWindowThreadProcessId")
var testOpenMapping = windows.NewLazySystemDLL("kernel32.dll").NewProc("OpenFileMappingW")

func windowPID(hwnd uintptr) uint32 {
	var pid uint32
	testWindowProcess.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	return pid
}
func openMapping(name *uint16) (windows.Handle, error) {
	h, _, err := testOpenMapping.Call(windows.FILE_MAP_READ, 0, uintptr(unsafe.Pointer(name)))
	if h == 0 {
		return 0, err
	}
	return windows.Handle(h), nil
}
func readWide(b []byte) string {
	var units []uint16
	for i := 0; i+1 < len(b); i += 2 {
		u := binary.LittleEndian.Uint16(b[i:])
		if u == 0 {
			break
		}
		units = append(units, u)
	}
	return string(utf16.Decode(units))
}
func processCreation(pid uint32) (uint64, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return 0, err
	}
	defer windows.CloseHandle(h)
	var c, e, k, u windows.Filetime
	err = windows.GetProcessTimes(h, &c, &e, &k, &u)
	return uint64(c.HighDateTime)<<32 | uint64(c.LowDateTime), err
}
