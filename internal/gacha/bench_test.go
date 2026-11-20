// SPDX-License-Identifier: GPL-3.0-or-later

package gacha

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/database/dbtest"
)

// benchmarkTime is the newest record time the synthetic archives use. Records
// walk backwards from it, one second apart, which keeps the (pool, item, time,
// pity) identity of every record distinct.
func benchmarkTime() time.Time {
	return time.Date(2026, 11, 14, 12, 0, 0, 0, time.UTC)
}

func benchmarkArchive(roleID string, records int) Archive {
	pools := []Pool{Limited, Permanent, Weapon}
	a := Archive{RoleID: roleID, Records: make([]Record, 0, records)}
	for i := range records {
		a.Records = append(a.Records, Record{
			Pool:   pools[i%len(pools)],
			ItemID: fmt.Sprintf("%d", 100000+i),
			At:     benchmarkTime().Add(-time.Duration(i) * time.Second),
			Pity:   i % 80,
			Luck:   []int{LuckUnknown, 0, 1, 2, 3, 4}[i%6],
		})
	}
	return a
}

func BenchmarkStoreLoad(b *testing.B) {
	for _, size := range []int{1000, 10000} {
		b.Run(fmt.Sprintf("%d-records", size), func(b *testing.B) {
			ctx := context.Background()
			store, err := NewStore(dbtest.Open(b), b.TempDir())
			if err != nil {
				b.Fatal(err)
			}
			archive := benchmarkArchive("123456", size)
			if err := store.Save(ctx, archive); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				got, err := store.Load(ctx, archive.RoleID)
				if err != nil {
					b.Fatal(err)
				}
				if len(got.Records) != size {
					b.Fatalf("loaded %d records, want %d", len(got.Records), size)
				}
			}
		})
	}
}

func BenchmarkMergeImported(b *testing.B) {
	archive := benchmarkArchive("123456", 1000)
	// Half of the increment repeats records that are already stored, which is
	// what a tajiduo refresh looks like.
	increment := benchmarkArchive("123456", 100)
	copy(increment.Records[:50], benchmarkArchive("123456", 50).Records)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, _, err := MergeImported(archive, Imported{Format: "oddward-gacha", Archive: increment}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkStatistics(b *testing.B) {
	archive := benchmarkArchive("123456", 10000)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if stats := Statistics(archive); stats.Pools[Limited].SCount == 0 {
			b.Fatal("empty statistics")
		}
	}
}

func BenchmarkStoreSave(b *testing.B) {
	for _, size := range []int{1000, 10000} {
		b.Run(fmt.Sprintf("%d-records", size), func(b *testing.B) {
			ctx := context.Background()
			store, err := NewStore(dbtest.Open(b), b.TempDir())
			if err != nil {
				b.Fatal(err)
			}
			archive := benchmarkArchive("123456", size)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if err := store.Save(ctx, archive); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
