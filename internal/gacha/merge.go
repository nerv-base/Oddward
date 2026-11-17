// SPDX-License-Identifier: GPL-3.0-or-later

package gacha

import (
	"errors"
	"fmt"
)

// MergeReport counts unique keys. Missing records are retained; a rolling
// upstream window is not evidence that a local record should be removed.
type MergeReport struct {
	Added     int
	Missing   int
	Conflicts int
}

type recordKey struct {
	pool   Pool
	item   string
	millis int64
	pity   int
}

func keyOf(r Record) recordKey { return recordKey{r.Pool, r.ItemID, r.At.UnixMilli(), r.Pity} }

// Merge retains local records and known luck, upgrades unknown luck, and adds
// one distinct metadata entry. On failure it returns the original input intact.
func Merge(a Archive, s Snapshot) (Archive, MergeReport, error) {
	if a.RoleID != "" && a.RoleID != s.RoleID {
		return a, MergeReport{}, ErrRoleMismatch
	}
	if err := validateRole(s.RoleID); err != nil {
		return a, MergeReport{}, err
	}
	m := SnapshotMeta{FetchedAt: s.FetchedAt, LuckType: s.LuckType, LuckTitle: s.LuckTitle}
	incoming := Archive{RoleID: s.RoleID}
	for _, p := range s.Pools {
		m.Pools = append(m.Pools, PoolMeta{Pool: p.Pool, M: p.M, DrawCount: p.DrawCount, RareCount: p.RareCount, Average: p.Average, PlayerOver: p.PlayerOver, RuleMismatch: p.M != HardPity(p.Pool)})
		for _, r := range p.Records {
			if r.Pool != p.Pool {
				return a, MergeReport{}, errors.New("snapshot record pool differs from its containing pool")
			}
			incoming.Records = append(incoming.Records, r)
		}
	}
	incoming.Snapshots = []SnapshotMeta{m}
	return mergeArchives(a, incoming)
}

// Imported contains a validated full archive and its source format. Export
// timestamps are transport metadata, not fetch times or current-pity evidence.
type Imported struct {
	Format  string
	Archive Archive
}

// MergeImported uses the same key and luck rules as Merge. An empty archive
// adopts the import's role ID; confirmation for a new role belongs to callers.
func MergeImported(a Archive, i Imported) (Archive, MergeReport, error) {
	switch i.Format {
	case "oddward-gacha", "nteuid-gacha":
	default:
		return a, MergeReport{}, ErrUnknownFormat
	}
	return mergeArchives(a, i.Archive)
}

func mergeArchives(a, incoming Archive) (Archive, MergeReport, error) {
	if a.RoleID != "" && a.RoleID != incoming.RoleID {
		return a, MergeReport{}, ErrRoleMismatch
	}
	old, err := normalizeArchive(a, true)
	if err != nil {
		return a, MergeReport{}, err
	}
	fresh, err := normalizeArchive(incoming, false)
	if err != nil {
		return a, MergeReport{}, err
	}
	oldRecords, _ := indexRecords(old.Records)
	newRecords, newConflicts := indexRecords(fresh.Records)
	report := MergeReport{}
	for key := range oldRecords {
		if _, ok := newRecords[key]; !ok {
			report.Missing++
		}
	}
	for key, r := range newRecords {
		previous, ok := oldRecords[key]
		conflict := newConflicts[key]
		if !ok {
			oldRecords[key] = r
			report.Added++
		} else {
			switch {
			case previous.Luck == LuckUnknown && r.Luck != LuckUnknown:
				previous.Luck = r.Luck
				oldRecords[key] = previous
			case previous.Luck != LuckUnknown && r.Luck != LuckUnknown && previous.Luck != r.Luck:
				conflict = true
			}
		}
		if conflict {
			report.Conflicts++
		}
	}
	result := Archive{RoleID: fresh.RoleID}
	for _, r := range oldRecords {
		result.Records = append(result.Records, r)
	}
	sortRecords(result.Records)
	result.Snapshots, err = normalizeMetas(append(old.Snapshots, fresh.Snapshots...))
	if err != nil {
		return a, MergeReport{}, fmt.Errorf("merge metadata: %w", err)
	}
	return result, report, nil
}

// Contradictory duplicate input keys pick the smallest known luck, so shuffled
// input is deterministic. Conflicts are counted once per unique incoming key.
func indexRecords(records []Record) (map[recordKey]Record, map[recordKey]bool) {
	out := map[recordKey]Record{}
	conflicts := map[recordKey]bool{}
	for _, r := range records {
		key := keyOf(r)
		old, ok := out[key]
		if !ok {
			out[key] = r
			continue
		}
		if old.Luck != LuckUnknown && r.Luck != LuckUnknown && old.Luck != r.Luck {
			conflicts[key] = true
		}
		if old.Luck == LuckUnknown || r.Luck != LuckUnknown && r.Luck < old.Luck {
			out[key] = r
		}
	}
	return out, conflicts
}
