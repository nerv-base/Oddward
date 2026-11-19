// SPDX-License-Identifier: GPL-3.0-or-later

package gacha

import (
	"strings"
	"time"
)

// ItemKind describes only the documented shape of an item identifier.
type ItemKind string

const (
	Character ItemKind = "character"
	Arc       ItemKind = "arc"
	Unknown   ItemKind = "unknown"
)

// Item is a display name with an optional rarity hint. Names are verified
// against an official source; rarity may come from the community wiki and is
// never required to show a record.
type Item struct {
	Kind   ItemKind
	Name   string
	Rarity string
}

// Names are included only after official-source verification; none are verified yet.
var builtinItems = map[string]Item{}

// Catalog is the name supplement fetched from the official tajiduo characters
// endpoint. It holds identifiers and names only, never credentials or roles.
type Catalog struct {
	FetchedAt time.Time
	Items     map[string]Item
}

// Lookup prefers the official supplement, then the built-in table. An
// identifier whose shape is unrecognized is never resolved to a name.
func (c Catalog) Lookup(id string) (Item, bool) {
	if Kind(id) == Unknown {
		return Item{}, false
	}
	if item, ok := c.Items[id]; ok && item.Name != "" {
		return item, true
	}
	return Lookup(id)
}

// Kind recognizes ASCII numeric characters and fork_[a-z0-9_]+ arc identifiers.
// Unknown identifiers remain valid archive data but must not query the catalog.
func Kind(id string) ItemKind {
	if asciiDigits(id) {
		return Character
	}
	if !strings.HasPrefix(id, "fork_") || len(id) == 5 {
		return Unknown
	}
	for _, c := range id[5:] {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			return Unknown
		}
	}
	return Arc
}

// Lookup returns only verified, built-in entries with recognized identifiers.
func Lookup(id string) (Item, bool) {
	if Kind(id) == Unknown {
		return Item{}, false
	}
	item, ok := builtinItems[id]
	return item, ok
}
