//go:build windows

// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"context"
	"errors"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func observeDownloadProcesses(ctx context.Context) (downloadProcesses, error) {
	if err := ctx.Err(); err != nil {
		return downloadProcesses{}, err
	}
	snapshot, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return downloadProcesses{}, err
	}
	defer windows.CloseHandle(snapshot)
	entry := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	var result downloadProcesses
	for err = windows.Process32First(snapshot, &entry); err == nil; err = windows.Process32Next(snapshot, &entry) {
		if err := ctx.Err(); err != nil {
			return downloadProcesses{}, err
		}
		switch strings.ToUpper(windows.UTF16ToString(entry.ExeFile[:])) {
		case "HTGAME.EXE":
			result.game = true
		case "NTEGAME.EXE", "NTEBROWSER.EXE", "NTEUPDATE.EXE", "NTELAUNCHER.EXE":
			result.official = true
		}
	}
	if !errors.Is(err, windows.ERROR_NO_MORE_FILES) {
		return downloadProcesses{}, err
	}
	return result, nil
}
