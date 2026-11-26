// SPDX-License-Identifier: GPL-3.0-or-later
package system

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"time"

	"github.com/nerv-base/Oddward/internal/gameinstall"
	"github.com/nerv-base/Oddward/internal/gameres"
	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// Caller holds the installation lease. The engine takes the subordinate stage
// lease, so neither snapshots nor a cache writer can bypass the installation lock.
func (b *GameDownloadBackend) preloadReconcileLocked(ctx context.Context, root string, r gameinstall.PreloadRecord) (gameinstall.ReconcileResult, *downloadPlan, error) {
	root, base, local, err := b.preloadBase(ctx, root)
	if err != nil {
		return gameinstall.ReconcileResult{}, nil, err
	}
	store, err := b.preloadStore(root)
	if err != nil {
		return gameinstall.ReconcileResult{}, nil, err
	}
	if b.preloadQuery == nil {
		return gameinstall.ReconcileResult{State: gameinstall.PreloadStale, Reason: "unverified"}, nil, nil
	}
	formal, err := b.metadata().formalDelivery(ctx, base.Branch)
	if err != nil {
		if errors.Is(err, errDownloadNotFound) {
			return gameinstall.ReconcileResult{State: gameinstall.PreloadStale, Reason: "expired"}, nil, nil
		}
		return gameinstall.ReconcileResult{}, nil, err
	}
	formal, formalSnapshot, err := downloadPreloadEvidence(formal, b.metadata().roots, true)
	if err != nil {
		return gameinstall.ReconcileResult{}, nil, err
	}
	r = downloadPreloadObserveFormal(r, formal.Config.ResVersion)
	if err = store.Save(ctx, r); err != nil {
		return gameinstall.ReconcileResult{}, nil, err
	}
	key, err := gameinstall.InstallKey(root)
	if err != nil {
		return gameinstall.ReconcileResult{}, nil, err
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return gameinstall.ReconcileResult{}, nil, err
	}
	p := &downloadPlan{root: root, key: key, id: hex.EncodeToString(nonce[:]), kind: state.DownloadPreload, delivery: formal, base: base, preload: &r, workRoot: preloadSystemWork(root, r)}
	pre, err := b.preloadQuery(ctx, r.Branch, r.Version)
	if err != nil {
		if errors.Is(err, errDownloadNotFound) {
			return gameinstall.ReconcileResult{State: gameinstall.PreloadStale, Reason: "expired"}, nil, nil
		}
		// A pre-release endpoint outage must not withhold an already confirmed
		// intermediate formal update. The existing staged preload is kept and
		// the caller falls back to the ordinary update path.
		if downloadVersionNewer(formal.Config.ResVersion, base.Version) && (formal.Branch != r.Branch || downloadVersionNewer(r.Version, formal.Config.ResVersion)) {
			return gameinstall.ReconcileResult{State: gameinstall.PreloadWaitingRelease, Reason: "waiting-release"}, p, nil
		}
		return gameinstall.ReconcileResult{}, nil, err
	}
	_, preSnapshot, err := downloadPreloadEvidence(pre, b.metadata().roots, true)
	if err != nil {
		return gameinstall.ReconcileResult{}, nil, err
	}
	if _, err = downloadDirectory(p.workRoot, false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return gameinstall.ReconcileResult{State: gameinstall.PreloadStale, Reason: "expired"}, nil, nil
		}
		return gameinstall.ReconcileResult{}, nil, err
	}
	engine, err := b.newDownloadEngine(ctx, p, nil)
	if err != nil {
		return gameinstall.ReconcileResult{}, nil, err
	}
	defer engine.Close()
	verified := map[gameres.Object]bool{}
	for _, file := range r.Files {
		if err = ctx.Err(); err != nil {
			return gameinstall.ReconcileResult{}, nil, err
		}
		if verified[file.Object] {
			continue
		}
		f, e := engine.OpenBlob(ctx, file.Object)
		if e == nil {
			verified[file.Object] = true
			f.Close()
		} else if ctx.Err() != nil {
			return gameinstall.ReconcileResult{}, nil, ctx.Err()
		}
	}
	free, err := b.space(root)
	if err != nil {
		return gameinstall.ReconcileResult{}, nil, err
	}
	processes, err := b.observe(ctx)
	if err != nil {
		return gameinstall.ReconcileResult{}, nil, errDownloadProcessCheck
	}
	rates, _ := b.downloadPerfRates(ctx, root)
	budget := gameres.BudgetInput{VolumeFreeBytes: free, CacheFreeBytes: free, ReserveBytes: max(2<<30, formal.Config.ResSize/50), NetBytesPerSecond: rates.NetBytesPerSecond, VerifyBytesPerSecond: rates.HashBytesPerSecond, IOBytesPerSecond: rates.IOBytesPerSecond}
	result, err := gameinstall.Reconcile(ctx, gameinstall.ReconcileInput{Record: r, Formal: formalSnapshot, PreRelease: preSnapshot, CurrentBranch: base.Branch, CurrentVersion: base.Version, BaselineVerified: true, Local: local, Capabilities: gameres.Capabilities{PatchApply: patchApplyAdmitted}, Budget: budget, Now: time.Now().UTC(), NewerFormalVersions: len(r.ObservedFormalVersions), VerifiedBlobs: verified, GameRunning: processes.game, OfficialRunning: processes.official, LockAvailable: true})
	if err != nil {
		return result, nil, err
	}
	p.plan = result.Plan.Plan
	p.free = free
	p.margin = budget.ReserveBytes
	p.need = p.plan.PeakAllocBytes + p.margin
	// A supplemental formal plan keeps the original preload identity/age and
	// cache subtree. Only content-compatible blobs are marked cached by Reconcile.
	if result.State == gameinstall.PreloadPlanning && len(result.Plan.Record.Targets) > 0 {
		replacement := result.Plan.Record
		replacement.Branch = r.Branch
		replacement.Version = r.Version
		replacement.SavedAt = r.SavedAt
		replacement.TargetVersionSeenAt = r.TargetVersionSeenAt
		replacement.TaskID = p.id
		replacement.ObservedFormalVersions = append([]string(nil), r.ObservedFormalVersions...)
		p.preload = &replacement
	} else {
		r.State = result.State
		r.UpdatedAt = time.Now().UTC()
		if result.State == gameinstall.PreloadApplicable {
			r.ListHash = formal.Config.ListHash
		}
		p.preload = &r
	}
	if err = store.Save(ctx, *p.preload); err != nil {
		return result, nil, err
	}
	return result, p, nil
}

func (b *GameDownloadBackend) preloadReconcile(ctx context.Context, req launcher.DownloadRequest, event state.Event, report func(state.Event)) (state.Event, error) {
	fail := func(err error) (state.Event, error) {
		event.Kind = state.PreloadFailed
		if errors.Is(err, gameinstall.ErrRecoveryRequired) {
			event.Kind = state.DownloadRecoveryNeeded
		}
		return event, err
	}
	r, err := b.preloadLatest(ctx, req.Root)
	if errors.Is(err, os.ErrNotExist) {
		event.Kind = state.PreloadStale
		event.Body = "stale"
		return event, nil
	}
	if err != nil {
		return fail(err)
	}
	lease, err := preloadSystemRootLease(req.Root)
	if err != nil {
		return fail(err)
	}
	defer lease.Close()
	result, p, err := b.preloadReconcileLocked(ctx, req.Root, r)
	if err != nil {
		return fail(err)
	}
	event.Kind = state.PreloadChecked
	event.Message, event.Body = r.Version, result.Reason
	switch result.State {
	case gameinstall.PreloadWaitingRelease:
		if p != nil {
			event.CurrentVersion = p.delivery.Config.ResVersion
			event.Focused = downloadVersionNewer(p.delivery.Config.ResVersion, p.base.Version)
		}
	case gameinstall.PreloadApplicable:
		if result.CanApply {
			b.putPlan(req.Root, p)
			event.Kind = state.PreloadAvailableToApply
			event.Enabled = true
		}
	case gameinstall.PreloadPlanning:
		if p != nil && len(p.plan.Targets) > 0 {
			return b.presentDownloadPlan(p, event, report)
		}
	case gameinstall.PreloadStale:
		store, e := b.preloadStore(req.Root)
		if e != nil {
			return fail(e)
		}
		r, e = store.Load(ctx, r.Branch, r.Version)
		if e != nil {
			return fail(e)
		}
		r.State = gameinstall.PreloadStale
		r.UpdatedAt = time.Now().UTC()
		if e = store.Save(ctx, r); e != nil {
			return fail(e)
		}
		event.Kind = state.PreloadStale
	case gameinstall.PreloadRecoveryRequired:
		return fail(gameinstall.ErrRecoveryRequired)
	}
	return event, nil
}

// Called only with the installation lease held immediately before Commit. A UI
// applicability event and persisted flags never substitute for these new proofs.
func (b *GameDownloadBackend) preloadBeforeApply(ctx context.Context, p *downloadPlan) error {
	if p.preload == nil {
		return gameinstall.ErrPreloadRecord
	}
	store, err := b.preloadStore(p.root)
	if err != nil {
		return err
	}
	r, err := store.Load(ctx, p.preload.Branch, p.preload.Version)
	if err != nil {
		return err
	}
	if r.State != gameinstall.PreloadApplicable {
		return gameinstall.ErrPreloadBlocked
	}
	result, fresh, err := b.preloadReconcileLocked(ctx, p.root, r)
	if err != nil {
		return err
	}
	if !result.CanApply || result.State != gameinstall.PreloadApplicable || fresh == nil {
		return gameinstall.ErrPreloadBlocked
	}
	if fresh.delivery.Config.ResVersion != p.delivery.Config.ResVersion || fresh.delivery.Config.ListHash != p.delivery.Config.ListHash || fresh.delivery.Branch != p.delivery.Branch {
		return gameinstall.ErrStaleState
	}
	r = *fresh.preload
	p.preload = &r
	p.plan = fresh.plan
	p.delivery = fresh.delivery
	p.base = fresh.base
	p.need = fresh.need
	p.free = fresh.free
	p.margin = fresh.margin
	return nil
}
