// SPDX-License-Identifier: GPL-3.0-or-later

// Package gacha handles local draw archives containing no secrets. It performs
// no network requests or encryption. Callers select the data directory and own
// account association, UI consent, and all network-response conversion.
package gacha

import (
	"errors"
	"fmt"
	"sort"
	"time"
	"unicode/utf8"
)

// Pool identifies an independent draw history.
type Pool string

const (
	Limited   Pool = "limited"
	Permanent Pool = "permanent"
	Weapon    Pool = "weapon"
)

// LuckUnknown identifies sources that do not provide a luck value.
const LuckUnknown = -1

// LuckMax is the highest luck tier the official gacha response has reported.
// The upstream value is not documented; extend this only after observing it.
const LuckMax = 4

// MaxDocumentBytes bounds both stored and imported documents.
const MaxDocumentBytes = 8 * 1024 * 1024

// MaxSnapshots is the number of recent, distinct metadata entries retained.
const MaxSnapshots = 64

var (
	ErrRoleMismatch  = errors.New("gacha role mismatch")
	ErrCorrupt       = errors.New("corrupt gacha archive")
	ErrUnknownFormat = errors.New("unknown gacha format")
)

// Record describes one reported S item. Pity anomalies remain available for
// diagnostics; they never contribute to valid-pity statistics.
type Record struct {
	Pool   Pool
	ItemID string
	At     time.Time
	Pity   int
	Luck   int
}

// Archive is independent of a saved account. Records are newest first, with
// ties ordered by Pool, ItemID, Pity, then Luck. Times use UTC milliseconds.
type Archive struct {
	RoleID    string
	Records   []Record
	Snapshots []SnapshotMeta
}

// PoolSnapshot contains data already converted by a network adapter.
type PoolSnapshot struct {
	Pool       Pool
	M          int
	DrawCount  int
	RareCount  int
	Average    string
	PlayerOver string
	Records    []Record
}

// Snapshot is plain, non-secret data from one fetch, not a network response.
type Snapshot struct {
	RoleID    string
	FetchedAt time.Time
	Pools     []PoolSnapshot
	LuckType  int
	LuckTitle string
}

// PoolMeta preserves upstream statistics separately from local calculations.
type PoolMeta struct {
	Pool         Pool
	M            int
	DrawCount    int
	RareCount    int
	Average      string
	PlayerOver   string
	RuleMismatch bool
}

// SnapshotMeta retains statistics without repeating draw records.
type SnapshotMeta struct {
	FetchedAt time.Time
	Pools     []PoolMeta
	LuckType  int
	LuckTitle string
}

// ValidatePool rejects unknown pool names.
func ValidatePool(p Pool) error {
	switch p {
	case Limited, Permanent, Weapon:
		return nil
	}
	return fmt.Errorf("invalid gacha pool %q", p)
}

// PoolFromTab translates the exact upstream tab labels; unknown tabs fail.
func PoolFromTab(tab string) (Pool, error) {
	switch tab {
	case "限定卡池":
		return Limited, nil
	case "常驻卡池":
		return Permanent, nil
	case "弧盘池":
		return Weapon, nil
	}
	return "", fmt.Errorf("unknown gacha tab %q", tab)
}

func validateRole(id string) error {
	if len(id) < 1 || len(id) > 20 || !asciiDigits(id) {
		return errors.New("role ID must contain 1 to 20 ASCII digits")
	}
	return nil
}

func asciiDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func validateRecord(r Record) error {
	if err := ValidatePool(r.Pool); err != nil {
		return err
	}
	if len(r.ItemID) == 0 || len(r.ItemID) > 64 {
		return errors.New("item ID must contain 1 to 64 printable ASCII bytes")
	}
	for i := 0; i < len(r.ItemID); i++ {
		if r.ItemID[i] < 0x20 || r.ItemID[i] > 0x7e {
			return errors.New("item ID must be printable ASCII")
		}
	}
	if r.Luck < LuckUnknown || r.Luck > LuckMax {
		return fmt.Errorf("luck must be unknown or between 0 and %d", LuckMax)
	}
	if err := validateTime(r.At); err != nil {
		return fmt.Errorf("record time: %w", err)
	}
	return nil
}

func validateTime(t time.Time) error {
	ms := t.UnixMilli()
	if t.IsZero() || ms <= 0 || !time.UnixMilli(ms).Equal(t.Truncate(time.Millisecond)) {
		return errors.New("time must be representable as positive Unix milliseconds")
	}
	return nil
}

func utcMillis(t time.Time) time.Time { return time.UnixMilli(t.UnixMilli()).UTC() }

func normalizeArchive(a Archive, empty bool) (Archive, error) {
	if a.RoleID == "" && empty {
		if len(a.Records) > 0 || len(a.Snapshots) > 0 {
			return Archive{}, errors.New("records require a role ID")
		}
		return Archive{}, nil
	}
	if err := validateRole(a.RoleID); err != nil {
		return Archive{}, err
	}
	out := Archive{RoleID: a.RoleID, Records: append([]Record(nil), a.Records...)}
	for i, r := range out.Records {
		if err := validateRecord(r); err != nil {
			return Archive{}, fmt.Errorf("record %d: %w", i, err)
		}
		out.Records[i].At = utcMillis(r.At)
	}
	sortRecords(out.Records)
	metas, err := normalizeMetas(a.Snapshots)
	if err != nil {
		return Archive{}, err
	}
	out.Snapshots = metas
	return out, nil
}

func sortRecords(records []Record) {
	sort.Slice(records, func(i, j int) bool {
		a, b := records[i], records[j]
		if !a.At.Equal(b.At) {
			return a.At.After(b.At)
		}
		if a.Pool != b.Pool {
			return a.Pool < b.Pool
		}
		if a.ItemID != b.ItemID {
			return a.ItemID < b.ItemID
		}
		if a.Pity != b.Pity {
			return a.Pity < b.Pity
		}
		return a.Luck < b.Luck
	})
}

func normalizeMetas(metas []SnapshotMeta) ([]SnapshotMeta, error) {
	var out []SnapshotMeta
	for _, m := range metas {
		if err := validateTime(m.FetchedAt); err != nil {
			return nil, fmt.Errorf("snapshot time: %w", err)
		}
		if !utf8.ValidString(m.LuckTitle) {
			return nil, errors.New("luck title must be UTF-8")
		}
		m.FetchedAt = utcMillis(m.FetchedAt)
		m.Pools = append([]PoolMeta(nil), m.Pools...)
		seen := map[Pool]bool{}
		for _, p := range m.Pools {
			if err := ValidatePool(p.Pool); err != nil {
				return nil, err
			}
			if seen[p.Pool] {
				return nil, errors.New("duplicate snapshot pool")
			}
			seen[p.Pool] = true
			if p.M < 0 || p.DrawCount < 0 || p.RareCount < 0 || !utf8.ValidString(p.Average) || !utf8.ValidString(p.PlayerOver) {
				return nil, errors.New("invalid snapshot statistics")
			}
		}
		sort.Slice(m.Pools, func(i, j int) bool { return m.Pools[i].Pool < m.Pools[j].Pool })
		out = append(out, m)
	}
	// The schema encoding provides a deterministic tie break for equal fetch times.
	sort.Slice(out, func(i, j int) bool {
		if !out[i].FetchedAt.Equal(out[j].FetchedAt) {
			return out[i].FetchedAt.After(out[j].FetchedAt)
		}
		return metaKey(out[i]) < metaKey(out[j])
	})
	result := out[:0]
	for _, m := range out {
		if len(result) == 0 || metaKey(result[len(result)-1]) != metaKey(m) {
			result = append(result, m)
		}
	}
	if len(result) > MaxSnapshots {
		result = result[:MaxSnapshots]
	}
	return result, nil
}
