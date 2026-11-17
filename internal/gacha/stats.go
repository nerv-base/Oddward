// SPDX-License-Identifier: GPL-3.0-or-later

package gacha

import "time"

// PoolStatistics counts all reported S records and separately counts invalid
// pity values. Sum, average, minimum and maximum include only valid pity values.
// The time range covers all records. Empty ranges and extrema remain zero.
type PoolStatistics struct {
	SCount      int
	Anomalous   int
	SumPity     int64
	AveragePity float64
	MinPity     int
	MaxPity     int
	Earliest    time.Time
	Latest      time.Time
}

// Stats deliberately separates local calculations from upstream statistics.
// No field represents or estimates draws since the last S item.
type Stats struct {
	Pools   map[Pool]PoolStatistics
	Tajiduo *SnapshotMeta
}

// Statistics computes each pool directly from records. Raw upstream values
// come only from the latest snapshot, with deterministic equal-time selection.
func Statistics(a Archive) Stats {
	out := Stats{Pools: map[Pool]PoolStatistics{Limited: {}, Permanent: {}, Weapon: {}}}
	for _, r := range a.Records {
		if ValidatePool(r.Pool) != nil {
			continue
		}
		p := out.Pools[r.Pool]
		p.SCount++
		at := utcMillis(r.At)
		if p.Earliest.IsZero() || at.Before(p.Earliest) {
			p.Earliest = at
		}
		if p.Latest.IsZero() || at.After(p.Latest) {
			p.Latest = at
		}
		if Band(r.Pool, r.Pity) == Abnormal {
			p.Anomalous++
		} else {
			p.SumPity += int64(r.Pity)
			if p.MinPity == 0 || r.Pity < p.MinPity {
				p.MinPity = r.Pity
			}
			if r.Pity > p.MaxPity {
				p.MaxPity = r.Pity
			}
		}
		out.Pools[r.Pool] = p
	}
	for pool, p := range out.Pools {
		if valid := p.SCount - p.Anomalous; valid > 0 {
			p.AveragePity = float64(p.SumPity) / float64(valid)
		}
		out.Pools[pool] = p
	}
	if metas, err := normalizeMetas(a.Snapshots); err == nil && len(metas) > 0 {
		m := metas[0]
		out.Tajiduo = &m
	}
	return out
}

// FormatTime uses the caller's display location. A nil location means UTC,
// making formatting deterministic instead of silently consulting the machine.
func FormatTime(at time.Time, location *time.Location) string {
	if location == nil {
		location = time.UTC
	}
	return at.In(location).Format("2006-01-02 15:04")
}
