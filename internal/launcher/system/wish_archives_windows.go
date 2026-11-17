// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package system

import "golang.org/x/sys/windows"

// replaceExport atomically replaces destination with stage. Both paths share a
// directory, and the write-through flag flushes the move to disk.
func replaceExport(stage, destination string) error {
	source, err := windows.UTF16PtrFromString(stage)
	if err != nil {
		return err
	}
	target, err := windows.UTF16PtrFromString(destination)
	if err != nil {
		return err
	}
	return windows.MoveFileEx(source, target, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
