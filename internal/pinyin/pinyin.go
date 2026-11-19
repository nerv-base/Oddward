// SPDX-License-Identifier: GPL-3.0-or-later

// Package pinyin turns Chinese characters into their most common Mandarin
// reading without tone marks. The embedded table is the one MyGo generates from
// Unicode's Unihan kMandarin field; see THIRD_PARTY_NOTICES.md.
package pinyin

import (
	_ "embed"
	"strings"
	"sync"
)

//go:embed pinyin.bin
var data string

var table = sync.OnceValues(func() ([]string, string) {
	syllables, readings, _ := strings.Cut(data, "\n")
	return strings.Fields(syllables), readings
})

// Mandarin returns the reading of r without its tone, or "" when the table has
// no reading for it.
func Mandarin(r rune) string {
	if r < 0x4E00 || r > 0x9FFF {
		return ""
	}
	syllables, readings := table()
	i := int(r-0x4E00) * 2
	if n := int(readings[i])<<8 | int(readings[i+1]); n > 0 {
		return syllables[n-1]
	}
	return ""
}

// Key returns lower-case ASCII letters and digits from s, replacing each Han
// character with its reading. Punctuation and other scripts are dropped.
func Key(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case Mandarin(r) != "":
			b.WriteString(Mandarin(r))
		}
	}
	return b.String()
}
