// SPDX-License-Identifier: GPL-3.0-or-later

package brandingcache

import (
	"fmt"
	"os"
)

func openRegular(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("brandingcache: cache file is not a regular file")
	}
	return os.Open(path)
}
