// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package loginui

import (
	"github.com/nerv-base/Oddward/internal/process"
	"golang.org/x/sys/windows"
)

// cmd.Process retains the owned child handle throughout this session. Read its
// creation time before starting Wait, rather than trusting a PID in UI messages.
func helperIdentity(pid int) (process.Identity, error) {
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return process.Identity{}, ErrHelper
	}
	defer windows.CloseHandle(h)
	var created, exited, kernel, user windows.Filetime
	if windows.GetProcessTimes(h, &created, &exited, &kernel, &user) != nil {
		return process.Identity{}, ErrHelper
	}
	wait, err := windows.WaitForSingleObject(h, 0)
	if err != nil || wait != uint32(windows.WAIT_TIMEOUT) {
		return process.Identity{}, ErrHelper
	}
	return process.Identity{PID: uint32(pid), CreationTime: uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)}, nil
}
