// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !windows

package brandingcache

import "os"

func replaceFile(source, destination string) error {
	return os.Rename(source, destination)
}
