// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"context"
	"github.com/nerv-base/Oddward/internal/gameres"
	"testing"
	"time"
)

func preloadReconcileFixture(t *testing.T) ReconcileInput {
	t.Helper()
	i := preloadFixture()
	p, e := StartPreload(context.Background(), i)
	if e != nil {
		t.Fatal(e)
	}
	r := p.Record
	r.State = PreloadWaitingRelease
	verified := map[gameres.Object]bool{}
	for n := range r.Files {
		r.Files[n].State = PreloadFileStaged
		verified[r.Files[n].Object] = true
	}
	return ReconcileInput{Record: r, Formal: i.Snapshot, PreRelease: i.Snapshot, CurrentBranch: i.CurrentBranch, CurrentVersion: i.CurrentVersion, BaselineVerified: true, Local: i.Local, Capabilities: i.Capabilities, Availability: i.Availability, Budget: i.Budget, Now: i.Now, VerifiedBlobs: verified, LockAvailable: true}
}
func TestReconcileElevenRules(t *testing.T) {
	for _, tc := range []struct {
		name   string
		state  PreloadState
		reason string
		change func(*ReconcileInput)
	}{
		{"1-branch", PreloadStale, "branch-unresolvable", func(i *ReconcileInput) { i.PreRelease.Available = false }},
		{"2-unreleased", PreloadWaitingRelease, "waiting-release", func(i *ReconcileInput) { i.Formal.Config.ResVersion = "1.0"; i.Formal.Manifest = nil }},
		{"3-hash-only", PreloadApplicable, "", func(i *ReconcileInput) { i.Formal.Config.ListHash = preloadID("changed list").MD5 }},
		{"4-content", PreloadPlanning, "replan", func(i *ReconcileInput) {
			m := *i.Formal.Manifest
			m.Files = append([]gameres.File(nil), m.Files...)
			id := preloadID("changed content")
			m.Files[0].MD5 = id.MD5
			m.Files[0].Size = id.Size
			i.Formal.Manifest = &m
		}},
		{"5-corruption", PreloadPlanning, "replan", func(i *ReconcileInput) { i.VerifiedBlobs = map[gameres.Object]bool{} }},
		{"6-baseline", PreloadPlanning, "baseline-untrusted", func(i *ReconcileInput) { i.BaselineVerified = false }},
		{"7-chain", PreloadPlanning, "replan", func(i *ReconcileInput) {
			i.Record.Files[0].Kind = PreloadBlobPatch
			i.Record.Files[0].Path = ""
			i.Record.Patches = []gameres.Object{i.Record.Files[0].Object}
			i.Record.Route = PreloadRoutePatch
		}},
		{"8-patch-retired", PreloadApplicable, "", func(i *ReconcileInput) {
			id := preloadID("unused patch")
			i.Record.Patches = []gameres.Object{id}
			i.Record.Files = append(i.Record.Files, PreloadFile{Kind: PreloadBlobPatch, Object: id, State: PreloadFileStaged})
			i.VerifiedBlobs[id] = true
		}},
		{"9-space", PreloadApplicable, "disk-short", func(i *ReconcileInput) { i.Budget.VolumeFreeBytes = 0 }},
		{"10-game", PreloadApplicable, "game-running", func(i *ReconcileInput) { i.GameRunning = true }},
		{"10-official", PreloadApplicable, "official-running", func(i *ReconcileInput) { i.OfficialRunning = true }},
		{"10-lock", PreloadApplicable, "locked", func(i *ReconcileInput) { i.LockAvailable = false }},
		{"11-downgrade", PreloadStale, "not-newer", func(i *ReconcileInput) { i.CurrentVersion = "1.1" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			i := preloadReconcileFixture(t)
			tc.change(&i)
			r, err := Reconcile(context.Background(), i)
			if err != nil {
				t.Fatal(err)
			}
			if r.State != tc.state || r.Reason != tc.reason {
				t.Fatalf("got state=%s reason=%s want %s/%s", r.State, r.Reason, tc.state, tc.reason)
			}
			if r.Reason != "" && r.CanApply {
				t.Fatal("blocked result can apply")
			}
		})
	}
}
func TestReconcilePatchChainsAndAvailabilityTTL(t *testing.T) {
	i := preloadFixture()
	target := gameres.Object{MD5: i.Snapshot.Manifest.Files[0].MD5, Size: i.Snapshot.Manifest.Files[0].Size}
	old, mid := preloadID("old contents"), preloadID("mid contents")
	a, b := preloadID("a"), preloadID("b")
	i.Local = preloadTestLocal{ids: map[string]gameres.Object{i.Snapshot.Manifest.Files[0].Path: old}}
	i.Capabilities.PatchApply = true
	i.Snapshot.Graph = &gameres.PatchGraph{Edges: []gameres.PatchEntry{{Old: old, New: mid, Blob: a, Version: "next"}, {Old: mid, New: target, Blob: b, Version: "next"}}}
	p, e := StartPreload(context.Background(), i)
	if e != nil || p.Record.Route != PreloadRoutePatch {
		t.Fatalf("chain fixture failed: %+v %v", p, e)
	}
	r := p.Record
	r.State = PreloadWaitingRelease
	verified := map[gameres.Object]bool{}
	for n := range r.Files {
		r.Files[n].State = PreloadFileStaged
		verified[r.Files[n].Object] = true
	}
	in := ReconcileInput{Record: r, Formal: i.Snapshot, PreRelease: i.Snapshot, CurrentVersion: i.CurrentVersion, CurrentBranch: i.CurrentBranch, BaselineVerified: true, Local: i.Local, Capabilities: i.Capabilities, Availability: i.Availability, Budget: i.Budget, Now: i.Now, VerifiedBlobs: verified, LockAvailable: true}
	got, e := Reconcile(context.Background(), in)
	if e != nil || !got.CanApply || len(got.Reusable) != 2 {
		t.Fatalf("trusted chain rejected: %+v %v", got, e)
	}
	in.Capabilities.MaxChainSteps = 1
	got, e = Reconcile(context.Background(), in)
	if e != nil || got.State != PreloadPlanning || got.Plan.Plan.Targets[0].Action != gameres.ActionFull {
		t.Fatalf("excess chain retained: %+v %v", got, e)
	}
	in.Capabilities = i.Capabilities
	in.Local = preloadTestLocal{}
	got, e = Reconcile(context.Background(), in)
	if e != nil || got.State != PreloadPlanning || got.Plan.Plan.Targets[0].Action != gameres.ActionFull {
		t.Fatalf("missing trusted source retained: %+v %v", got, e)
	}
	// An observed 404 expires with its snapshot. After expiry the caller must
	// probe at execution; the pure planner must not perpetuate a historical 404.
	i.Availability.Objects = map[gameres.Object]gameres.ObjectStatus{a: gameres.StatusGone}
	p, e = StartPreload(context.Background(), i)
	if e != nil || p.Record.Route != PreloadRouteFull {
		t.Fatalf("fresh gone patch selected: %+v %v", p, e)
	}
	i.Now = i.Now.Add(i.Availability.TTL + time.Second)
	p, e = StartPreload(context.Background(), i)
	if e != nil || p.Record.Route != PreloadRoutePatch {
		t.Fatalf("stale 404 retained: %+v %v", p, e)
	}
}
func TestReconcileRejectsFutureUpdateAndActiveTransaction(t *testing.T) {
	for _, change := range []func(*ReconcileInput){func(i *ReconcileInput) { i.Record.UpdatedAt = i.Now.Add(time.Hour) }, func(i *ReconcileInput) { i.Record.State = PreloadApplying }} {
		i := preloadReconcileFixture(t)
		change(&i)
		r, e := Reconcile(context.Background(), i)
		if e != nil || r.CanApply || r.State != PreloadRecoveryRequired {
			t.Fatalf("unsafe recovery apply: %+v %v", r, e)
		}
	}
}
func TestReconcileExpiryAndPartialReuse(t *testing.T) {
	for _, change := range []func(*ReconcileInput){func(i *ReconcileInput) { i.Now = i.Now.Add(14*24*time.Hour + time.Nanosecond) }, func(i *ReconcileInput) { i.NewerFormalVersions = 2 }, func(i *ReconcileInput) { i.ManifestGone = true }} {
		i := preloadReconcileFixture(t)
		change(&i)
		r, e := Reconcile(context.Background(), i)
		if e != nil || r.State != PreloadStale {
			t.Fatalf("expired accepted %+v %v", r, e)
		}
	}
	i := preloadReconcileFixture(t)
	i.Formal.Config.ListHash = preloadID("new manifest").MD5
	m := *i.Formal.Manifest
	other := preloadID("other file")
	m.Files = append(append([]gameres.File(nil), m.Files...), gameres.File{Path: "Client/other.bin", MD5: other.MD5, Size: other.Size})
	i.Formal.Manifest = &m
	r, e := Reconcile(context.Background(), i)
	if e != nil {
		t.Fatal(e)
	}
	if r.State != PreloadPlanning || r.Plan.Plan.DownloadBytes != other.Size || len(r.Reusable) != 1 {
		t.Fatalf("partial reuse lost: %+v", r)
	}
	// A blob at another path cannot be guessed to be a cross-version full hit.
	i = preloadReconcileFixture(t)
	m = *i.Formal.Manifest
	m.Files = append([]gameres.File(nil), m.Files...)
	m.Files[0].Path = "Client/moved.bin"
	i.Formal.Manifest = &m
	r, e = Reconcile(context.Background(), i)
	if e != nil || len(r.Reusable) != 0 || r.State != PreloadPlanning {
		t.Fatalf("cross-path reuse accepted: %+v %v", r, e)
	}
}
