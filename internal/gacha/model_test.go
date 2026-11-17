// SPDX-License-Identifier: GPL-3.0-or-later

package gacha

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestRulesAndKinds(t *testing.T) {
	for _, p := range []Pool{Limited, Permanent} {
		for _, tc := range []struct {
			n    int
			want PityBand
		}{{70, Regular}, {71, Boosted}, {89, Boosted}, {90, Guaranteed}, {91, Abnormal}, {0, Abnormal}, {-1, Abnormal}} {
			if got := Band(p, tc.n); got != tc.want {
				t.Errorf("Band(%s,%d)=%s want %s", p, tc.n, got, tc.want)
			}
		}
		if HardPity(p) != 90 || BoostStart(p) != 71 || Ratio(p, 45) != 0.5 {
			t.Errorf("character rule: %s", p)
		}
	}
	for _, tc := range []struct {
		n    int
		want PityBand
	}{{59, Regular}, {60, Guaranteed}, {61, Abnormal}, {0, Abnormal}} {
		if Band(Weapon, tc.n) != tc.want {
			t.Errorf("weapon %d", tc.n)
		}
	}
	if HardPity(Weapon) != 60 || BoostStart(Weapon) != 0 || BoostStart("other") != 0 || WeaponLimitedGuarantee != 80 || Ratio(Weapon, -1) != 0 || Ratio(Weapon, 61) != 1 || Ratio("other", 1) != 0 || Band("other", 1) != Abnormal {
		t.Fatal("rule limits")
	}
	for tab, p := range map[string]Pool{"限定卡池": Limited, "常驻卡池": Permanent, "弧盘池": Weapon} {
		got, err := PoolFromTab(tab)
		if err != nil || got != p || ValidatePool(got) != nil {
			t.Fatalf("tab %s: %s %v", tab, got, err)
		}
	}
	if _, err := PoolFromTab("other"); err == nil {
		t.Fatal("unknown tab")
	}
	if ValidatePool("other") == nil {
		t.Fatal("unknown pool")
	}
	for id, want := range map[string]ItemKind{"1001": Character, "000": Character, "fork_a_0": Arc, "fork_": Unknown, "fork_ABC": Unknown, "fork_a-": Unknown, "１２": Unknown, "": Unknown, "other": Unknown} {
		if Kind(id) != want {
			t.Errorf("Kind(%q)", id)
		}
	}
	for id, item := range builtinItems {
		if Kind(id) != item.Kind || item.Kind == Unknown || strings.TrimSpace(item.Name) != item.Name || item.Name == "" {
			t.Errorf("invalid catalog entry %q", id)
		}
	}
	if _, ok := Lookup("unverified"); ok {
		t.Fatal("unverified catalog item")
	}
}

func TestMergeIdempotenceOrderAndRetention(t *testing.T) {
	a := fixtureArchive()
	old := a.Records[0]
	old.At = old.At.AddDate(-1, 0, 0)
	old.ItemID = "old"
	a.Records = append(a.Records, old)
	s := fixtureSnapshot()
	fresh := s.Pools[0].Records[0]
	fresh.ItemID = "1002"
	fresh.At = fresh.At.Add(time.Minute)
	s.Pools[0].Records = append(s.Pools[0].Records, fresh)
	s.Pools[0].M = 91
	got, r, err := Merge(a, s)
	if err != nil || r.Added != 1 || r.Missing != 1 || r.Conflicts != 0 || len(got.Records) != 4 {
		t.Fatalf("merge: %+v %v %+v", r, err, got)
	}
	if !got.Snapshots[0].Pools[0].RuleMismatch {
		t.Fatal("M mismatch not recorded")
	}
	twice, r, err := Merge(got, s)
	if err != nil || r.Added != 0 || !reflect.DeepEqual(got, twice) {
		t.Fatalf("not idempotent: %+v %v", r, err)
	}
	s.Pools[0].Records[0], s.Pools[0].Records[1] = s.Pools[0].Records[1], s.Pools[0].Records[0]
	s.Pools[0], s.Pools[1] = s.Pools[1], s.Pools[0]
	shuffled, _, err := Merge(a, s)
	if err != nil || !reflect.DeepEqual(got, shuffled) {
		t.Fatalf("order changed result: %v", err)
	}
	if !reflect.DeepEqual(a.Records[0], fixtureArchive().Records[0]) {
		t.Fatal("input modified")
	}
	s.RoleID = "999"
	before := fixtureArchive()
	mismatch, _, err := Merge(before, s)
	if !errors.Is(err, ErrRoleMismatch) || !reflect.DeepEqual(mismatch, before) {
		t.Fatalf("mismatch: %v", err)
	}
}

func TestMergeLuck(t *testing.T) {
	for _, tc := range []struct{ old, new, want, conflict int }{{-1, 2, 2, 0}, {2, -1, 2, 0}, {2, 3, 2, 1}, {2, 2, 2, 0}, {-1, -1, -1, 0}} {
		a := fixtureArchive()
		a.Records = a.Records[:1]
		a.Records[0].Luck = tc.old
		s := fixtureSnapshot()
		s.Pools = s.Pools[:1]
		s.Pools[0].Records[0].Luck = tc.new
		got, r, err := Merge(a, s)
		if err != nil || got.Records[0].Luck != tc.want || r.Conflicts != tc.conflict {
			t.Fatalf("%+v: %+v %+v %v", tc, got, r, err)
		}
	}
}

func TestSnapshotLimitAndLatestRawStatistics(t *testing.T) {
	a := Archive{}
	for i := 0; i < 70; i++ {
		s := fixtureSnapshot()
		s.FetchedAt = s.FetchedAt.Add(time.Duration(i) * time.Minute)
		s.LuckTitle = "latest"
		var err error
		a, _, err = Merge(a, s)
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(a.Snapshots) != 64 {
		t.Fatalf("snapshots %d", len(a.Snapshots))
	}
	s := Statistics(a)
	if s.Tajiduo == nil || s.Tajiduo.FetchedAt != a.Snapshots[0].FetchedAt || s.Tajiduo.LuckTitle != "latest" {
		t.Fatal("latest raw stats")
	}
}

func TestStatisticsExcludeAnomaliesAndFormatTime(t *testing.T) {
	a := fixtureArchive()
	a.Records = a.Records[:1]
	for _, p := range []int{10, 0, -2, 91} {
		r := a.Records[0]
		r.Pity = p
		r.At = r.At.Add(time.Duration(p) * time.Minute)
		a.Records = append(a.Records, r)
	}
	got := Statistics(a).Pools[Limited]
	if got.SCount != 5 || got.Anomalous != 3 || got.SumPity != 81 || got.AveragePity != 40.5 || got.MinPity != 10 || got.MaxPity != 71 || got.Earliest.IsZero() || got.Latest.IsZero() {
		t.Fatalf("stats %+v", got)
	}
	if Statistics(a).Tajiduo != nil {
		t.Fatal("invented raw statistics")
	}
	if FormatTime(fixtureTime(), time.FixedZone("UTC+8", 8*3600)) != "2026-11-14 20:34" {
		t.Fatal("display timezone")
	}
}
