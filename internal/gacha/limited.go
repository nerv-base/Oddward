// SPDX-License-Identifier: GPL-3.0-or-later

package gacha

import "strings"

// The standard-pool S items are public game metadata, not account data. The
// list mirrors the one used by NTE-Maid's local analyzer (MIT); it needs a
// manual update when the game adds another standard S item. Anything outside
// the list in a limited pool is the current featured item.
var (
	standardSCharacters = map[string]bool{
		"1055": true,
		"1054": true,
		"1039": true,
		"1025": true,
		"1023": true,
		"1003": true,
	}
	standardSArcs = map[string]bool{
		"fork_butterfly":      true,
		"fork_blackbook":      true,
		"fork_mofeikesi":      true,
		"fork_jingmotingyuan": true,
		"fork_wushoutieyu":    true,
		"fork_bitgame":        true,
		"fork_rishi":          true,
		"fork_nestbird":       true,
		"fork_arachne":        true,
		"fork_whale":          true,
		"fork_nakupeda":       true,
		"fork_policerat":      true,
		"fork_mamen":          true,
	}
)

// IsUp reports whether an S record belongs to the current featured item of a
// limited pool. Permanent pools have no featured item. The check is based on
// item identity only; it never infers a lost 50/50 from pity counts.
func IsUp(p Pool, itemID string) bool {
	id := strings.ToLower(strings.TrimSpace(itemID))
	switch p {
	case Limited:
		return Kind(id) == Character && !standardSCharacters[id]
	case Weapon:
		return Kind(id) == Arc && !standardSArcs[id]
	default:
		return false
	}
}
