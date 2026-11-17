// SPDX-License-Identifier: GPL-3.0-or-later

package gacha

import "testing"

func TestIsUpUsesStandardPoolLists(t *testing.T) {
	for _, tc := range []struct {
		pool Pool
		id   string
		want bool
	}{
		{Limited, "1055", false},
		{Limited, "1001", true},
		{Limited, "fork_butterfly", false},
		{Permanent, "1001", false},
		{Permanent, "1055", false},
		{Weapon, "fork_butterfly", false},
		{Weapon, "fork_newfeatured", true},
		{Weapon, "fork_FORK", true},
		{Weapon, "21001", false},
		{"unknown", "1001", false},
	} {
		if got := IsUp(tc.pool, tc.id); got != tc.want {
			t.Fatalf("IsUp(%q, %q) = %v, want %v", tc.pool, tc.id, got, tc.want)
		}
	}
}
