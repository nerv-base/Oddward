// SPDX-License-Identifier: GPL-3.0-or-later

package gacha

import "time"

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
