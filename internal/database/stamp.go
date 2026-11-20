// SPDX-License-Identifier: GPL-3.0-or-later

package database

import (
	"database/sql"
	"time"
)

// Timestamps are stored as unix nanoseconds in UTC. Integers sort and range
// exactly like the instants they describe, so an index on a timestamp works for
// both "newest first" and "older than" queries, and the value round-trips to a
// time.Time without re-parsing text. The zero time is stored as NULL: writing
// UnixNano() of the zero time would silently mean 1754.

// Stamp converts t to its stored form, or nil for the zero time.
func Stamp(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC().UnixNano()
}

// StampTime converts a scanned column back to a UTC time. NULL, zero and
// negative values that no launcher write produces all map to the zero time.
func StampTime(ns sql.NullInt64) time.Time {
	if !ns.Valid || ns.Int64 <= 0 {
		return time.Time{}
	}
	return time.Unix(0, ns.Int64).UTC()
}

// StampInt converts a scanned non-null column back to a UTC time.
func StampInt(ns int64) time.Time {
	if ns <= 0 {
		return time.Time{}
	}
	return time.Unix(0, ns).UTC()
}

// StampMillis converts an exact millisecond instant to its stored integer form.
// Gacha records carry millisecond precision in their identity, so they are
// compared on this column directly.
func StampMillis(t time.Time) int64 { return t.UTC().UnixMilli() }

// StampMillisTime converts stored milliseconds back to a UTC time.
func StampMillisTime(ms int64) time.Time { return time.UnixMilli(ms).UTC() }
