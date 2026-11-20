// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package database

import (
	"os"

	"golang.org/x/sys/windows"
)

// replaceMirrorFile renames temp over path, flushing the directory entry so a
// power loss cannot leave the mirror missing after the database recorded it.
func replaceMirrorFile(temp, path string) error {
	from, err := windows.UTF16PtrFromString(temp)
	if err != nil {
		return err
	}
	to, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	if err := windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH); err != nil {
		return &os.LinkError{Op: "rename", Old: temp, New: path, Err: err}
	}
	return nil
}
