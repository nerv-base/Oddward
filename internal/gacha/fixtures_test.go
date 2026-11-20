// SPDX-License-Identifier: GPL-3.0-or-later

package gacha

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/database"
	"github.com/nerv-base/Oddward/internal/database/dbtest"
)

func fixtureTime() time.Time { return time.Date(2026, 11, 14, 12, 34, 56, 789000000, time.UTC) }

func fixtureArchive() Archive {
	return Archive{RoleID: "123456", Records: []Record{
		{Pool: Limited, ItemID: "1001", At: fixtureTime(), Pity: 71, Luck: 2},
		{Pool: Weapon, ItemID: "fork_test", At: fixtureTime().Add(-time.Hour), Pity: 60, Luck: LuckUnknown},
	}}
}

func fixtureSnapshot() Snapshot {
	a := fixtureArchive()
	return Snapshot{RoleID: a.RoleID, FetchedAt: fixtureTime().Add(time.Hour), LuckType: 2, LuckTitle: "synthetic", Pools: []PoolSnapshot{
		{Pool: Limited, M: 90, DrawCount: 100, RareCount: 1, Average: "71.0", PlayerOver: "50", Records: []Record{a.Records[0]}},
		{Pool: Weapon, M: 60, DrawCount: 60, RareCount: 1, Average: "60", PlayerOver: "10", Records: []Record{a.Records[1]}},
	}}
}

// newTestStore returns a store over a fresh database and a mirror directory that
// does not exist yet, so a test can prove which operation creates it.
func newTestStore(t *testing.T) (*Store, *database.DB, string) {
	t.Helper()
	db := dbtest.Open(t)
	dir := filepath.Join(t.TempDir(), "gacha")
	s, err := NewStore(db, dir)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s, db, dir
}

// legacyCorpus is a synthetic pre-migration directory: several roles, all three
// pools, and snapshots carrying several pool metas each.
func legacyCorpus() []Archive {
	at := fixtureTime()
	first := Archive{RoleID: "200000001", Records: []Record{
		{Pool: Limited, ItemID: "11001", At: at, Pity: 12, Luck: 2},
		{Pool: Permanent, ItemID: "11002", At: at.Add(-time.Minute), Pity: 40, Luck: LuckUnknown},
		{Pool: Weapon, ItemID: "fork_wushoutieyu", At: at.Add(-2 * time.Minute), Pity: 5, Luck: 1},
	}}
	first.Snapshots = []SnapshotMeta{
		{FetchedAt: at.Add(time.Hour), LuckType: 3, LuckTitle: "欧气满满", Pools: []PoolMeta{
			{Pool: Limited, M: 90, DrawCount: 120, RareCount: 2, Average: "60.0", PlayerOver: "30"},
			{Pool: Permanent, M: 90, DrawCount: 40, RareCount: 0, Average: "", PlayerOver: "0", RuleMismatch: true},
		}},
		{FetchedAt: at.Add(2 * time.Hour), LuckType: 1, LuckTitle: "非酋", Pools: []PoolMeta{
			{Pool: Weapon, M: 60, DrawCount: 5, RareCount: 1, Average: "5.0", PlayerOver: "0", RuleMismatch: true},
		}},
	}
	second := Archive{RoleID: "200000002", Records: []Record{
		{Pool: Limited, ItemID: "11003", At: at.Add(3 * time.Minute), Pity: 90, Luck: 4},
	}}
	second.Snapshots = []SnapshotMeta{
		{FetchedAt: at.Add(30 * time.Minute), LuckType: 0, LuckTitle: "平稳", Pools: []PoolMeta{
			{Pool: Limited, M: 90, DrawCount: 3, RareCount: 1, Average: "3.0", PlayerOver: "0"},
		}},
	}
	return []Archive{first, second}
}

// writeLegacyCorpus writes the archives as the files a pre-migration build left
// behind and returns their exact bytes by path.
func writeLegacyCorpus(t *testing.T, dir string, archives []Archive) map[string][]byte {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("create %s: %v", dir, err)
	}
	files := map[string][]byte{}
	for _, a := range archives {
		data, err := ExportOddward(a, fixtureTime())
		if err != nil {
			t.Fatalf("ExportOddward(%s): %v", a.RoleID, err)
		}
		path := filepath.Join(dir, a.RoleID+".json")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		files[path] = data
	}
	return files
}

// mustRead returns the bytes of path or fails the test.
func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}
