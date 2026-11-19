// SPDX-License-Identifier: GPL-3.0-or-later

package pinyin

import "testing"

func TestMandarin(t *testing.T) {
	for _, tc := range []struct {
		r    rune
		want string
	}{
		{'焰', "yan"},
		{'魂', "hun"},
		{'狂', "kuang"},
		{'飙', "biao"},
		{'无', "wu"},
		{'首', "shou"},
		{'铁', "tie"},
		{'驭', "yu"},
		{'A', ""},
		{'あ', ""},
	} {
		if got := Mandarin(tc.r); got != tc.want {
			t.Errorf("Mandarin(%q) = %q, want %q", tc.r, got, tc.want)
		}
	}
}

func TestKey(t *testing.T) {
	for _, tc := range []struct {
		in, want string
	}{
		{"「无首铁驭」", "wushoutieyu"},
		{"焰魂狂飙", "yanhunkuangbiao"},
		{"「胶片-MANISHI」", "jiaopianmanishi"},
		{"", ""},
		{"fork_x", "forkx"},
	} {
		if got := Key(tc.in); got != tc.want {
			t.Errorf("Key(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
