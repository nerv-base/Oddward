// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import "strings"

func mainlandPhone(s string) bool {
	if len(s) != 11 || s[0] != '1' {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func digitsOnly(s string, limit int) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' && b.Len() < limit {
			b.WriteRune(r)
		}
	}
	return b.String()
}
