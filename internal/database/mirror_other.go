// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !windows

package database

import "os"

// replaceMirrorFile renames temp over path.
func replaceMirrorFile(temp, path string) error {
	return os.Rename(temp, path)
}
