// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package accounts

import (
	"io/fs"
	"syscall"
)

// isReparse reports symlinks, junctions and any other reparse point.
func isReparse(fi fs.FileInfo) bool {
	if fi.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
		return true
	}
	if d, ok := fi.Sys().(*syscall.Win32FileAttributeData); ok {
		return d.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
	}
	return false
}
