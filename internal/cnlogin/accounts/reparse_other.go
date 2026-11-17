// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !windows

package accounts

import "io/fs"

func isReparse(fi fs.FileInfo) bool {
	return fi.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0
}
