// SPDX-License-Identifier: GPL-3.0-or-later

package accounts

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaskPhone turns an 11-digit mainland mobile number into the default label
// form 138****0000. The phone number itself is never stored.
func MaskPhone(phone string) (string, error) {
	if len(phone) != 11 || phone[0] != '1' {
		return "", ErrInvalidPhone
	}
	for i := 0; i < len(phone); i++ {
		if phone[i] < '0' || phone[i] > '9' {
			return "", ErrInvalidPhone
		}
	}
	return phone[:3] + "****" + phone[7:], nil
}

// sanitizeLabel drops control, format (bidi overrides, zero-width) and line
// separator characters, trims surrounding space and enforces the length
// limit. A label that still contains a full phone number is rejected so a
// remark cannot persist the number in clear.
func sanitizeLabel(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", ErrInvalidLabel
	}
	var b strings.Builder
	for _, r := range raw {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			continue
		}
		b.WriteRune(r)
	}
	label := strings.TrimSpace(b.String())
	if label == "" || utf8.RuneCountInString(label) > MaxLabelRunes || hasPhoneLikeRun(label) {
		return "", ErrInvalidLabel
	}
	return label, nil
}

// isMaskedLabel reports whether label is the generated masked phone form
// 138****0000 rather than a remark the user typed.
func isMaskedLabel(label string) bool {
	if len(label) != 11 || label[0] != '1' || label[3:7] != "****" {
		return false
	}
	for i := 0; i < len(label); i++ {
		if i >= 3 && i < 7 {
			continue
		}
		if label[i] < '0' || label[i] > '9' {
			return false
		}
	}
	return true
}

func hasPhoneLikeRun(s string) bool {
	run := 0
	for _, r := range s {
		if !unicode.IsDigit(r) {
			run = 0
			continue
		}
		if run++; run >= 11 {
			return true
		}
	}
	return false
}
