// SPDX-License-Identifier: GPL-3.0-or-later
package system

import (
	"github.com/nerv-base/Oddward/internal/gameinstall"
	"strings"
)

func downloadPreloadObserveFormal(r gameinstall.PreloadRecord, version string) gameinstall.PreloadRecord {
	if !downloadVersion.MatchString(version) || !downloadVersion.MatchString(r.Version) {
		return r
	}
	a, b := strings.Split(version, "."), strings.Split(r.Version, ".")
	newer := false
	for i := 0; i < max(len(a), len(b)); i++ {
		x, y := "0", "0"
		if i < len(a) {
			x = strings.TrimLeft(a[i], "0")
		}
		if i < len(b) {
			y = strings.TrimLeft(b[i], "0")
		}
		if x == "" {
			x = "0"
		}
		if y == "" {
			y = "0"
		}
		if len(x) != len(y) {
			newer = len(x) > len(y)
			break
		}
		if x != y {
			newer = x > y
			break
		}
	}
	if !newer {
		return r
	}
	for _, seen := range r.ObservedFormalVersions {
		if seen == version {
			return r
		}
	}
	r.ObservedFormalVersions = append(append([]string(nil), r.ObservedFormalVersions...), version)
	return r
}
