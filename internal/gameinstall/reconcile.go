// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"context"
	"github.com/nerv-base/Oddward/internal/gameres"
	"time"
)

type ReconcileInput struct {
	Record                                                               PreloadRecord
	Formal, PreRelease                                                   PreloadManifest
	CurrentBranch, CurrentVersion                                        string
	BaselineVerified                                                     bool
	Local                                                                gameres.Local
	Capabilities                                                         gameres.Capabilities
	Availability                                                         gameres.AvailabilitySnapshot
	Budget                                                               gameres.BudgetInput
	Now                                                                  time.Time
	VerifiedBlobs                                                        map[gameres.Object]bool
	NewerFormalVersions                                                  int
	ManifestGone, Withdrawn, GameRunning, OfficialRunning, LockAvailable bool
}
type ReconcileResult struct {
	State       PreloadState
	Reason      string
	CanApply    bool
	Plan        PreloadPlan
	Reusable    []PreloadFile
	Discardable []PreloadFile
	Notes       []string
}

// Reconcile evaluates independently from download completion. VerifiedBlobs must
// be a fresh cryptographic snapshot, not a translation of persisted staged flags.
// LockAvailable is only a snapshot: the caller must acquire and hold the real lock
// and recheck processes/free space immediately before starting its transaction.
func Reconcile(ctx context.Context, i ReconcileInput) (ReconcileResult, error) {
	if err := ctx.Err(); err != nil {
		return ReconcileResult{}, err
	}
	out := ReconcileResult{}
	stop := func(s PreloadState, reason string) (ReconcileResult, error) {
		out.State = s
		out.Reason = reason
		return out, nil
	}
	if err := preloadValidateRecord(i.Record); err != nil {
		return out, err
	}
	if i.Record.State == PreloadApplying || i.Record.State == PreloadRecoveryRequired {
		return stop(PreloadRecoveryRequired, "transaction-recovery")
	}
	if i.Now.IsZero() || i.Now.Before(i.Record.UpdatedAt) {
		return stop(PreloadRecoveryRequired, "invalid-clock")
	}
	if i.Withdrawn {
		return stop(PreloadStale, "withdrawn")
	}
	if i.Now.Sub(i.Record.SavedAt) > 14*24*time.Hour || i.NewerFormalVersions >= 2 || i.ManifestGone {
		return stop(PreloadStale, "expired")
	}
	if !preloadManifestOK(i.PreRelease) || i.PreRelease.Branch != i.Record.Branch {
		return stop(PreloadStale, "branch-unresolvable")
	}
	if i.CurrentBranch != "" && i.CurrentBranch != i.Record.Branch {
		out.Notes = append(out.Notes, "branch differs: "+i.CurrentBranch+" → "+i.Record.Branch)
	}
	release, ok := preloadCompareVersion(i.Record.Version, i.Formal.Config.ResVersion)
	if !ok {
		return stop(PreloadStale, "invalid-version")
	}
	if release > 0 {
		return stop(PreloadWaitingRelease, "waiting-release")
	}
	newer, ok := preloadCompareVersion(i.Formal.Config.ResVersion, i.CurrentVersion)
	if !ok || newer <= 0 {
		return stop(PreloadStale, "not-newer")
	}
	if !preloadManifestOK(i.Formal) {
		return stop(PreloadStale, "formal-unresolvable")
	}
	if !i.BaselineVerified {
		return stop(PreloadPlanning, "baseline-untrusted")
	}
	formalTargets := map[string]gameres.Object{}
	for _, f := range i.Formal.Manifest.Files {
		formalTargets[f.Path] = gameres.Object{MD5: f.MD5, Size: f.Size}
	}
	for _, p := range i.Formal.Manifest.Packages {
		for _, e := range p.Entries {
			formalTargets[e.Path] = gameres.Object{MD5: e.MD5, Size: e.Size}
		}
	}
	patches := map[gameres.Object]bool{}
	if i.Capabilities.PatchApply && i.Formal.Graph != nil {
		for _, e := range i.Formal.Graph.Edges {
			if e.Section == "" {
				patches[e.Blob] = true
			}
		}
	}
	allowed := map[gameres.Object]bool{}
	for _, f := range i.Record.Files {
		if err := ctx.Err(); err != nil {
			return out, err
		}
		keep := f.State == PreloadFileStaged && i.VerifiedBlobs[f.Object] && (f.Kind == PreloadBlobFull && formalTargets[f.Path] == f.Object || f.Kind == PreloadBlobPatch && patches[f.Object])
		if keep {
			allowed[f.Object] = true
			out.Reusable = append(out.Reusable, f)
		} else {
			out.Discardable = append(out.Discardable, f)
		}
	}
	// The underlying cache may contain other verified objects, but a discarded
	// preload full blob cannot be reused by guessing its identity at a new path.
	blocked := map[gameres.Object]bool{}
	for _, f := range i.Record.Files {
		if !allowed[f.Object] {
			blocked[f.Object] = true
		}
	}
	local := preloadReconcileLocal{local: i.Local, allowed: allowed, blocked: blocked}
	in := PreloadInput{Advertisement: gameres.Config{PreReleaseStatus: "start", PreReleaseBranch: i.Formal.Branch, PreReleaseVersion: i.Formal.Config.ResVersion}, Snapshot: i.Formal, CurrentBranch: i.CurrentBranch, CurrentVersion: i.CurrentVersion, BaselineTrusted: true, Local: local, Capabilities: i.Capabilities, Availability: i.Availability, Budget: i.Budget, Now: i.Now}
	// Planning must run even with low space, so return a content result and a
	// separate disk gate. The original budget is used again for the Apply gate.
	in.Budget.VolumeFreeBytes = 1 << 62
	in.Budget.CacheFreeBytes = 1 << 62
	p, err := StartPreload(ctx, in)
	if err != nil {
		return out, err
	}
	out.Plan = p
	changed := p.Plan.DownloadBytes > 0
	oldTargets := map[string]PreloadTarget{}
	for _, t := range i.Record.Targets {
		oldTargets[t.Path] = t
	}
	for _, t := range p.Record.Targets {
		old, exists := oldTargets[t.Path]
		if !exists || old.Object != t.Object {
			changed = true
		}
		if old.Action == gameres.ActionPatch && (t.Action != gameres.ActionPatch || !preloadSameSteps(old.Steps, t.Steps)) {
			changed = true
		}
	}
	for _, f := range out.Discardable {
		if f.Kind == PreloadBlobFull && formalTargets[f.Path] != (gameres.Object{}) {
			changed = true
		}
	}
	if changed {
		return stop(PreloadPlanning, "replan")
	}
	out.State = PreloadApplicable
	// Cached target allocations still consume staging/commit space. The shared
	// planner provides the conservative same-volume commit peak, plus reserve.
	reserve := i.Budget.ReserveBytes
	if reserve == 0 {
		var total int64
		for _, o := range formalTargets {
			total += o.Size
		}
		reserve = max(2<<30, total/50)
	}
	need := p.Plan.PeakAllocBytes + reserve
	if min(i.Budget.VolumeFreeBytes, i.Budget.CacheFreeBytes) < need {
		return stop(PreloadApplicable, "disk-short")
	}
	if i.OfficialRunning {
		return stop(PreloadApplicable, "official-running")
	}
	if i.GameRunning {
		return stop(PreloadApplicable, "game-running")
	}
	if !i.LockAvailable {
		return stop(PreloadApplicable, "locked")
	}
	out.CanApply = true
	return out, nil
}

type preloadReconcileLocal struct {
	local            gameres.Local
	allowed, blocked map[gameres.Object]bool
}

func (l preloadReconcileLocal) Identity(p string) (gameres.Object, bool) {
	if l.local == nil {
		return gameres.Object{}, false
	}
	return l.local.Identity(p)
}
func (l preloadReconcileLocal) Trusted(p string, o gameres.Object) bool {
	return l.local != nil && l.local.Trusted(p, o)
}
func (l preloadReconcileLocal) Cached(o gameres.Object) bool {
	return l.allowed[o] || !l.blocked[o] && l.local != nil && l.local.Cached(o)
}
func preloadSameSteps(a, b []gameres.PatchEntry) bool {
	if len(a) != len(b) {
		return false
	}
	for n := range a {
		if a[n].Old != b[n].Old || a[n].New != b[n].New || a[n].Blob != b[n].Blob {
			return false
		}
	}
	return true
}
