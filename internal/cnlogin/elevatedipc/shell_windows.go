// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package elevatedipc

import (
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var shellExecuteEx = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

type shellExecuteInfo struct {
	Size, Mask                        uint32
	Window                            windows.Handle
	Verb, File, Parameters, Directory *uint16
	Show                              int32
	Instance                          windows.Handle
	IDList                            uintptr
	Class                             *uint16
	KeyClass                          windows.Handle
	HotKey                            uint32
	Icon                              windows.Handle
	Process                           windows.Handle
}

func shellLaunch(exe string, args []string) (windows.Handle, error) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE)
	if err != nil && err != syscall.Errno(1) {
		return 0, errLaunch
	} // S_FALSE is successful initialization.
	defer windows.CoUninitialize()
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = windows.EscapeArg(arg)
	}
	info := shellExecuteInfo{
		Size: uint32(unsafe.Sizeof(shellExecuteInfo{})),
		// NOCLOSEPROCESS | NOASYNC | FLAG_NO_UI | NO_CONSOLE.
		Mask:       0x40 | 0x100 | 0x400 | 0x8000,
		Verb:       windows.StringToUTF16Ptr("runas"),
		File:       windows.StringToUTF16Ptr(exe),
		Parameters: windows.StringToUTF16Ptr(strings.Join(quoted, " ")),
		Show:       windows.SW_HIDE,
	}
	ok, _, _ := shellExecuteEx.Call(uintptr(unsafe.Pointer(&info)))
	runtime.KeepAlive(&info)
	if ok == 0 || info.Process == 0 {
		if info.Process != 0 {
			windows.CloseHandle(info.Process)
		}
		return 0, errLaunch
	}
	return info.Process, nil
}
