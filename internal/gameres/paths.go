// SPDX-License-Identifier: GPL-3.0-or-later

package gameres

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// NormalizePath validates a manifest path under Windows naming rules, keeping
// the original case for materialization and using slash separators on every OS.
func NormalizePath(input string) (string, error) {
	path := strings.ReplaceAll(input, `\`, "/")
	first, _ := utf8.DecodeRuneInString(path)
	if !utf8.ValidString(path) || !unicode.IsLetter(first) || windowsPathLen(path) > 240 {
		return "", ErrPathUnsafe
	}
	for part := range strings.SplitSeq(path, "/") {
		if part == "" || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") ||
			strings.ContainsAny(part, `:*?<>|"`) || strings.ContainsFunc(part, unicode.IsControl) {
			return "", ErrPathUnsafe
		}
		stem, _, _ := strings.Cut(part, ".")
		stem = strings.ToUpper(strings.TrimRight(stem, " "))
		switch stem {
		case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
			return "", ErrPathUnsafe
		}
		if (strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT")) && strings.Contains("123456789¹²³", stem[3:]) && utf8.RuneCountInString(stem[3:]) == 1 {
			return "", ErrPathUnsafe
		}
	}
	return path, nil
}

func windowsPathLen(path string) int {
	length := 0
	for _, r := range path {
		length++
		if r > 0xffff {
			length++
		}
	}
	return length
}

// Windows ordinal comparisons use uppercase mappings. Folding to lowercase
// would miss aliases such as Greek small sigma and final sigma.
func pathKey(path string) string { return strings.ToUpper(path) }
