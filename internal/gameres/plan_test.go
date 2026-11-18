// SPDX-License-Identifier: GPL-3.0-or-later

package gameres

import (
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
)

const (
	oldHash   = "11111111111111111111111111111111"
	newHash   = "22222222222222222222222222222222"
	midHash   = "33333333333333333333333333333333"
	patchHash = "44444444444444444444444444444444"
)

type planLocal struct {
	installed map[string]Object
	trusted   map[Object]bool
	cached    map[Object]bool
}

func (l planLocal) Identity(path string) (Object, bool) { o, ok := l.installed[path]; return o, ok }
func (l planLocal) Trusted(path string, o Object) bool  { return l.installed[path] == o && l.trusted[o] }
func (l planLocal) Cached(o Object) bool                { return l.cached[o] }

func TestBuildPlanDistinguishesInstalledCachedAndTransferred(t *testing.T) {
	keep := Object{oldHash, 14}
	cached := Object{newHash, 20}
	pak := Object{midHash, 90}
	m := &ResList{Tag: "baseTag", Files: []File{
		{Path: "Game/Keep", MD5: keep.MD5, Size: keep.Size},
		{Path: "Game/Replace", MD5: cached.MD5, Size: cached.Size},
	}, Packages: []Package{{Pak: pak, Entries: []Entry{{Path: "Game/Packed", MD5: patchHash, Size: 15, Offset: 30, Pak: pak}}}},
		Tags: []TagGroup{{Version: "1", Files: []File{{Path: "Game/Bonus", MD5: keep.MD5, Size: keep.Size}}}}}
	local := planLocal{installed: map[string]Object{"Game/Keep": keep, "Game/Replace": keep}, trusted: map[Object]bool{keep: true}, cached: map[Object]bool{cached: true}}
	p, err := BuildPlan(m, nil, local, Capabilities{}, AvailabilitySnapshot{}, roomyBudget())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Targets) != 3 || p.Targets[0].Action != ActionReuseInstalled || p.Targets[1].Action != ActionInstallCached || p.Targets[2].Action != ActionFull {
		t.Fatalf("wrong materialization actions: %+v", p.Targets)
	}
	if p.ReusedBytes != 14 || p.CachedBytes != 20 || p.DownloadBytes != 15 || p.FullBytes != 15 || p.PeakAllocBytes != 15 {
		t.Fatalf("wrong totals: %+v", p)
	}
	if p.Targets[2].Full != (Source{Kind: SourcePakRange, Object: pak, Offset: 30, Size: 15}) || !slices.Contains(p.Notes, "availability unknown: "+pak.String()) {
		t.Fatalf("wrong pak source or notes: %+v", p)
	}
}

func TestBuildPlanCountsCASOnceAndStagesBothTargets(t *testing.T) {
	m := &ResList{Tag: "baseTag", Files: []File{
		{Path: "Game/A", MD5: newHash, Size: 10}, {Path: "Game/B", MD5: newHash, Size: 10},
	}}
	p, err := BuildPlan(m, nil, nil, Capabilities{}, AvailabilitySnapshot{}, roomyBudget())
	if err != nil || p.DownloadBytes != 10 || p.FullBytes != 20 || p.PeakAllocBytes != 20 {
		t.Fatalf("CAS should transfer once and stage two targets: %+v, %v", p, err)
	}
}

func TestBuildPlanGatesPatchAndKeepsPerTargetFallback(t *testing.T) {
	old := Object{oldHash, 1000}
	target := Object{newHash, 2000}
	blob := Object{patchHash, 80}
	m := &ResList{Tag: "baseTag", CheckHash: "current", Files: []File{{Path: "Game/File", MD5: target.MD5, Size: target.Size}}}
	g := &PatchGraph{Edges: []PatchEntry{{Old: old, New: target, Blob: blob, Version: "current"}}}
	local := planLocal{installed: map[string]Object{"Game/File": old}}
	avail := AvailabilitySnapshot{Objects: map[Object]ObjectStatus{blob: StatusReachable, target: StatusReachable}}
	for _, test := range []struct {
		name  string
		caps  Capabilities
		trust bool
		want  ActionKind
	}{
		{"disabled", Capabilities{}, true, ActionFull},
		{"untrusted", Capabilities{PatchApply: true}, false, ActionFull},
		{"qualified", Capabilities{PatchApply: true}, true, ActionPatch},
		{"blob limit", Capabilities{PatchApply: true, MaxPatchBlob: 79}, true, ActionFull},
	} {
		t.Run(test.name, func(t *testing.T) {
			local.trusted = map[Object]bool{old: test.trust}
			p, err := BuildPlan(m, g, local, test.caps, avail, roomyBudget())
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Targets) != 1 || p.Targets[0].Action != test.want {
				t.Fatalf("plan = %+v", p)
			}
			if test.want == ActionFull {
				if p.DownloadBytes != 2000 || p.PatchBytes != 0 || p.PeakAllocBytes != 2000 {
					t.Fatalf("plan = %+v", p)
				}
				return
			}
			got := p.Targets[0]
			if got.Fallback == nil || got.Fallback.Action != ActionFull || got.Fallback.Full.Object != target || got.Current != old || p.Fallbacks != 1 || p.PatchBytes != 80 ||
				p.DownloadBytes != 80 || p.OldReadBytes != 1000 || p.RebuildBytes != 2000 || p.PeakAllocBytes != 2080 || p.FullBytes != 2000 {
				t.Fatalf("unexpected patch plan: target=%+v totals=%+v", got, p)
			}
			for _, want := range []string{old.String(), target.String(), blob.String(), "Full"} {
				if !strings.Contains(got.Reason, want) {
					t.Fatalf("reason %q missing %q", got.Reason, want)
				}
			}
		})
	}
}

func TestBuildPlanFiltersPatchGraphByAvailabilityVersionAndSection(t *testing.T) {
	old, mid, target := Object{oldHash, 1000}, Object{midHash, 1150}, Object{newHash, 2000}
	gone, fromCache, end, otherBranch := Object{patchHash, 1}, Object{patchHash, 25}, Object{patchHash, 30}, Object{patchHash, 2}
	m := &ResList{Tag: "baseTag", CheckHash: "current", Files: []File{{Path: "Game/File", MD5: target.MD5, Size: target.Size}}}
	g := &PatchGraph{Edges: []PatchEntry{
		{Old: old, New: target, Blob: gone, Version: "current"},
		{Old: old, New: target, Blob: otherBranch, Version: "older"},
		{Old: old, New: target, Blob: otherBranch, Version: "current", Section: "notBase"},
		{Old: old, New: mid, Blob: fromCache, Version: "older"},
		{Old: mid, New: target, Blob: end, Version: "current"},
	}}
	local := planLocal{map[string]Object{"Game/File": old}, map[Object]bool{old: true}, map[Object]bool{fromCache: true}}
	avail := AvailabilitySnapshot{Objects: map[Object]ObjectStatus{gone: StatusGone, fromCache: StatusGone, end: StatusReachable, target: StatusGone}}
	p, err := BuildPlan(m, g, local, Capabilities{PatchApply: true}, avail, roomyBudget())
	if err != nil {
		t.Fatal(err)
	}
	if p.Targets[0].Route == nil || len(p.Targets[0].Route.Steps) != 2 || p.PatchBytes != 30 || p.PeakAllocBytes != 3180 || p.RebuildBytes != 3150 {
		t.Fatalf("unexpected chain: %+v", p)
	}
	if _, err := BuildPlan(m, g, local, Capabilities{PatchApply: true, MaxChainSteps: 1}, avail, roomyBudget()); !errors.Is(err, ErrTargetUnavailable) {
		t.Fatalf("no available target: %v", err)
	}
}

func TestBuildPlanLimitsPatchSearchAcrossTargets(t *testing.T) {
	old, blob := graphObject(1, 1), graphObject(2, 1)
	g := &PatchGraph{Edges: make([]PatchEntry, maxPatchEdges)}
	for i := range g.Edges {
		g.Edges[i] = PatchEntry{Old: old, New: graphObject(i+4, 1), Blob: blob}
	}
	m := &ResList{Tag: "baseTag", CheckHash: "current"}
	l := planLocal{installed: map[string]Object{}, trusted: map[Object]bool{old: true}}
	for i := range 66 {
		path := fmt.Sprintf("game/file%d", i)
		m.Files = append(m.Files, File{Path: path, MD5: newHash, Size: 1})
		l.installed[path] = old
	}
	p, err := BuildPlan(m, g, l, Capabilities{PatchApply: true}, AvailabilitySnapshot{}, roomyBudget())
	if err != nil || len(p.Targets) != 66 || !slices.Contains(p.Notes, "patch search limit reached: game/file65") {
		t.Fatalf("expected Full after exhausting the shared patch search budget: %v, %v", p.Notes, err)
	}
}

func TestBuildPlanFitsVerifiedContentWithinVolumeBudget(t *testing.T) {
	old, target, patch := Object{oldHash, 50 << 30}, Object{newHash, 9 << 30}, Object{patchHash, 1 << 30}
	old.Size = 10 << 30
	m := &ResList{Tag: "baseTag", CheckHash: "current", Files: []File{{Path: "Game/File", MD5: target.MD5, Size: target.Size}}}
	local := planLocal{installed: map[string]Object{"Game/File": old}, trusted: map[Object]bool{old: true}}
	g := &PatchGraph{Edges: []PatchEntry{{Old: old, New: target, Blob: patch, Version: "current"}}}
	budget := BudgetInput{VolumeFreeBytes: 11 << 30, CacheFreeBytes: 11 << 30}
	p, err := BuildPlan(m, g, local, Capabilities{PatchApply: true}, AvailabilitySnapshot{}, budget)
	if err != nil || len(p.Targets) != 1 || p.Targets[0].Action != ActionFull || p.PeakAllocBytes != 9<<30 {
		t.Fatalf("exact fit for Full: plan=%+v err=%v", p, err)
	}
	budget.CacheFreeBytes--
	if _, err := BuildPlan(m, g, local, Capabilities{PatchApply: true}, AvailabilitySnapshot{}, budget); !errors.Is(err, ErrInsufficientSpace) {
		t.Fatalf("no candidate fits available space: %v", err)
	}
}

func TestBuildPlanEstimatesTimeAndPrefersLessNetworkWithinTolerance(t *testing.T) {
	old, target := Object{oldHash, 1000}, Object{newHash, 1000}
	m := &ResList{Tag: "baseTag", CheckHash: "current", Files: []File{{Path: "Game/File", MD5: target.MD5, Size: target.Size}}}
	local := planLocal{installed: map[string]Object{"Game/File": old}, trusted: map[Object]bool{old: true}}
	budget := roomyBudget()
	budget.NetBytesPerSecond, budget.IOBytesPerSecond, budget.VerifyBytesPerSecond = 100, 100, 100
	// Full: 10s network + 10s write + 10s verify. Patch adds 10s for source reads.
	for _, test := range []struct {
		size   int64
		action ActionKind
	}{{50, ActionPatch}, {110, ActionPatch}, {200, ActionFull}} {
		g := &PatchGraph{Edges: []PatchEntry{{Old: old, New: target, Blob: Object{patchHash, test.size}, Version: "current"}}}
		p, err := BuildPlan(m, g, local, Capabilities{PatchApply: true}, AvailabilitySnapshot{}, budget)
		if err != nil || len(p.Targets) != 1 || p.Targets[0].Action != test.action {
			t.Fatalf("patch size %d: plan=%+v err=%v, want action %d", test.size, p, err, test.action)
		}
	}
}

func TestBuildPlanReconsidersActionsWhenWholePlanExceedsBudget(t *testing.T) {
	old, a, b := Object{oldHash, 100}, Object{newHash, 100}, Object{midHash, 100}
	m := &ResList{Tag: "baseTag", CheckHash: "current", Files: []File{
		{Path: "game/a", MD5: a.MD5, Size: a.Size}, {Path: "game/b", MD5: b.MD5, Size: b.Size},
	}}
	g := &PatchGraph{Edges: []PatchEntry{
		{Old: old, New: a, Blob: Object{patchHash, 10}, Version: "current"},
		{Old: old, New: b, Blob: Object{patchHash, 11}, Version: "current"},
	}}
	l := planLocal{installed: map[string]Object{"game/a": old, "game/b": old}, trusted: map[Object]bool{old: true}}
	budget := BudgetInput{VolumeFreeBytes: 211, CacheFreeBytes: 211, ReserveBytes: 1}
	p, err := BuildPlan(m, g, l, Capabilities{PatchApply: true}, AvailabilitySnapshot{}, budget)
	if err != nil || len(p.Targets) != 2 || p.Targets[0].Action != ActionPatch || p.Targets[1].Action != ActionFull || p.PeakAllocBytes != 210 {
		t.Fatalf("should fit one patch alongside one full target: %+v, %v", p, err)
	}
}

func TestBuildPlanReportsRequiredSpaceWithoutOverflow(t *testing.T) {
	m := &ResList{Tag: "baseTag", Files: []File{
		{Path: "game/a", MD5: newHash, Size: 10}, {Path: "game/b", MD5: midHash, Size: 10},
	}}
	budget := BudgetInput{VolumeFreeBytes: math.MaxInt64, CacheFreeBytes: math.MaxInt64, ReserveBytes: math.MaxInt64 - 10}
	_, err := BuildPlan(m, nil, nil, Capabilities{}, AvailabilitySnapshot{}, budget)
	if !errors.Is(err, ErrInsufficientSpace) || !strings.Contains(err.Error(), "need 9223372036854775817 bytes") {
		t.Fatalf("expected aggregate space error above int64: %v", err)
	}
}

func TestBuildPlanSharesRebuildSpaceWhenReconsideringActions(t *testing.T) {
	old := Object{oldHash, 100}
	a, b, c := Object{newHash, 10}, Object{newHash, 100}, Object{newHash, 20}
	midA, midC := Object{midHash, 90}, Object{midHash, 91}
	m := &ResList{Tag: "baseTag", CheckHash: "current", Files: []File{
		{Path: "game/a", MD5: a.MD5, Size: a.Size}, {Path: "game/b", MD5: b.MD5, Size: b.Size}, {Path: "game/c", MD5: c.MD5, Size: c.Size},
	}}
	blob := func(i string) Object { return Object{strings.Repeat(i, 32), 1} }
	g := &PatchGraph{Edges: []PatchEntry{
		{Old: old, New: midA, Blob: blob("4"), Version: "older"}, {Old: midA, New: a, Blob: blob("5"), Version: "current"},
		{Old: old, New: b, Blob: blob("6"), Version: "current"},
		{Old: old, New: midC, Blob: blob("7"), Version: "older"}, {Old: midC, New: c, Blob: blob("8"), Version: "current"},
	}}
	l := planLocal{installed: map[string]Object{"game/a": old, "game/b": old, "game/c": old}, trusted: map[Object]bool{old: true}}
	avail := AvailabilitySnapshot{Objects: map[Object]ObjectStatus{a: StatusGone}}
	budget := BudgetInput{VolumeFreeBytes: 135, CacheFreeBytes: 135, ReserveBytes: 1}
	p, err := BuildPlan(m, g, l, Capabilities{PatchApply: true}, avail, budget)
	if err != nil || len(p.Targets) != 3 || p.Targets[0].Action != ActionPatch || p.Targets[1].Action != ActionPatch || p.Targets[2].Action != ActionFull || p.PeakAllocBytes != 133 {
		t.Fatalf("mixed plan should share rebuild allocation: %+v, %v", p, err)
	}
}

func TestBuildPlanReconsidersTargetsSharingPatchBlob(t *testing.T) {
	obj := func(n int, size int64) Object { return Object{fmt.Sprintf("%032x", n), size} }
	baseA, baseBC, baseD := obj(1, 1), obj(2, 1), obj(3, 1)
	midA, midD := obj(4, 90), obj(5, 90)
	a, bc, d := obj(6, 10), obj(7, 20), obj(8, 20)
	m := &ResList{Tag: "baseTag", CheckHash: "current", Files: []File{
		{Path: "game/a", MD5: a.MD5, Size: a.Size}, {Path: "game/b", MD5: bc.MD5, Size: bc.Size},
		{Path: "game/c", MD5: bc.MD5, Size: bc.Size}, {Path: "game/d", MD5: d.MD5, Size: d.Size},
	}}
	g := &PatchGraph{Edges: []PatchEntry{
		{Old: baseA, New: midA, Blob: obj(9, 1)}, {Old: midA, New: a, Blob: obj(10, 1), Version: "current"},
		{Old: baseBC, New: bc, Blob: obj(11, 20), Version: "current"},
		{Old: baseD, New: midD, Blob: obj(12, 1)}, {Old: midD, New: d, Blob: obj(13, 1), Version: "current"},
	}}
	l := planLocal{installed: map[string]Object{"game/a": baseA, "game/b": baseBC, "game/c": baseBC, "game/d": baseD},
		trusted: map[Object]bool{baseA: true, baseBC: true, baseD: true}}
	avail := AvailabilitySnapshot{Objects: map[Object]ObjectStatus{a: StatusGone}}
	budget := BudgetInput{VolumeFreeBytes: 146, CacheFreeBytes: 146, ReserveBytes: 1,
		NetBytesPerSecond: 1, IOBytesPerSecond: 1000, VerifyBytesPerSecond: 1000}
	p, err := BuildPlan(m, g, l, Capabilities{PatchApply: true}, avail, budget)
	if err != nil || len(p.Targets) != 4 || p.Targets[0].Action != ActionPatch || p.Targets[1].Action != ActionPatch ||
		p.Targets[2].Action != ActionPatch || p.Targets[3].Action != ActionFull || p.PeakAllocBytes != 142 || p.PatchBytes != 22 {
		t.Fatalf("two targets should share the patch allocation: %+v, %v", p, err)
	}
}

func TestBuildPlanPreservesFullAlternateAfterPatch(t *testing.T) {
	old, target, pak := Object{oldHash, 1000}, Object{newHash, 2000}, Object{midHash, 2500}
	m := &ResList{Tag: "baseTag", CheckHash: "current", Packages: []Package{
		{Pak: pak, Entries: []Entry{{Path: "game/a", MD5: newHash, Size: target.Size, Offset: 10, Pak: pak}}},
	}}
	g := &PatchGraph{Edges: []PatchEntry{{Old: old, New: target, Blob: Object{patchHash, 50}, Version: "current"}}}
	l := planLocal{installed: map[string]Object{"game/a": old}, trusted: map[Object]bool{old: true}}
	p, err := BuildPlan(m, g, l, Capabilities{PatchApply: true}, AvailabilitySnapshot{Objects: map[Object]ObjectStatus{target: StatusReachable}}, roomyBudget())
	if err != nil || p.Targets[0].Action != ActionPatch || p.Targets[0].Fallback.Full.Kind != SourcePakRange ||
		p.Targets[0].Fallback.Alternate.Kind != SourceCAS || p.Targets[0].Fallback.Alternate.Object != target {
		t.Fatalf("patch then Pak should preserve final CAS alternate: %+v, %v", p, err)
	}
}

func roomyBudget() BudgetInput {
	return BudgetInput{VolumeFreeBytes: 100 << 30, CacheFreeBytes: 100 << 30, ReserveBytes: 1}
}
