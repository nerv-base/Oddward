// SPDX-License-Identifier: GPL-3.0-or-later
package system

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nerv-base/Oddward/internal/gamedl"
	"github.com/nerv-base/Oddward/internal/gameinstall"
	"github.com/nerv-base/Oddward/internal/gameres"
	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func preloadSystemRootLease(root string) (*gamedl.Lease, error) { return acquireDownloadWork(root) }
func preloadSystemMakeWork(root string, r gameinstall.PreloadRecord) error {
	anchor, err := os.OpenRoot(downloadWork(root))
	if err != nil {
		return err
	}
	defer anchor.Close()
	relative := ""
	for _, part := range []string{"preload", r.Branch, r.Version} {
		if strings.ContainsAny(part, "/\\\\") || part == "." || part == ".." {
			return gameres.ErrPathUnsafe
		}
		if _, err = gameres.NormalizePath("X/" + part); err != nil {
			return err
		}
		relative = filepath.Join(relative, part)
		if err = anchor.Mkdir(relative, 0700); err != nil && !os.IsExist(err) {
			return err
		}
		info, e := anchor.Lstat(relative)
		if e != nil {
			return e
		}
		if !info.IsDir() || downloadReparse(info) {
			return gameres.ErrPathUnsafe
		}
	}
	_, err = downloadDirectory(preloadSystemWork(root, r), false)
	return err
}

// preparePreloadWork is called only while the root installation lease is held.
func (b *GameDownloadBackend) preparePreloadWork(p *downloadPlan) error {
	if p.preload == nil {
		return gameinstall.ErrPreloadRecord
	}
	expected := preloadSystemWork(p.root, *p.preload)
	if p.workRoot != "" && !strings.EqualFold(p.workRoot, expected) {
		return gameres.ErrPathUnsafe
	}
	p.workRoot = expected
	return preloadSystemMakeWork(p.root, *p.preload)
}

// preloadOfferSnapshot is a read-only offer check. Shell owns cadence and
// eligibility; this method never creates an install state/work/cache/task tree.
func (b *GameDownloadBackend) preloadOfferSnapshot(ctx context.Context, root string) (latest, preload string, offered bool, err error) {
	if err = ctx.Err(); err != nil {
		return "", "", false, err
	}
	if _, err = downloadDirectory(filepath.Join(b.StateRoot, "installs"), false); err != nil {
		return "", "", false, err
	}
	input, _, _, err := b.preloadDiscoveryMode(ctx, root, true)
	if err != nil {
		return "", "", false, err
	}
	if err = ctx.Err(); err != nil {
		return "", "", false, err
	}
	verdict := gameinstall.EvaluatePreload(input)
	return input.Advertisement.ResVersion, verdict.Version, verdict.State == gameinstall.PreloadAvailable, nil
}

func (b *GameDownloadBackend) preloadDiscovery(ctx context.Context, root string) (gameinstall.PreloadInput, gameDelivery, gameinstall.InstallRecord, error) {
	return b.preloadDiscoveryMode(ctx, root, false)
}

func (b *GameDownloadBackend) preloadDiscoveryMode(ctx context.Context, root string, tolerateQueryErr bool) (gameinstall.PreloadInput, gameDelivery, gameinstall.InstallRecord, error) {
	root, base, local, err := b.preloadBase(ctx, root)
	if err != nil {
		return gameinstall.PreloadInput{}, gameDelivery{}, base, err
	}
	ad, _, _, err := b.metadata().formalConfig(ctx, base.Branch)
	if err != nil {
		return gameinstall.PreloadInput{}, gameDelivery{}, base, err
	}
	processes, err := b.observe(ctx)
	if err != nil {
		return gameinstall.PreloadInput{}, gameDelivery{}, base, errDownloadProcessCheck
	}
	free, err := b.space(root)
	if err != nil {
		return gameinstall.PreloadInput{}, gameDelivery{}, base, err
	}
	rates, _ := b.downloadPerfRates(ctx, root)
	input := gameinstall.PreloadInput{Advertisement: ad, CurrentBranch: base.Branch, CurrentVersion: base.Version, BaselineTrusted: true, OfficialRunning: processes.official, Local: local, Capabilities: gameres.Capabilities{PatchApply: patchApplyAdmitted}, Budget: gameres.BudgetInput{VolumeFreeBytes: free, CacheFreeBytes: free, ReserveBytes: max(2<<30, ad.ResSize/50), NetBytesPerSecond: rates.NetBytesPerSecond, VerifyBytesPerSecond: rates.HashBytesPerSecond, IOBytesPerSecond: rates.IOBytesPerSecond}, Now: time.Now().UTC()}
	if ad.PreReleaseStatus != "start" || b.preloadQuery == nil {
		return input, gameDelivery{}, base, nil
	}
	if _, e := gameres.VersionConfigURL(b.metadata().roots[0], ad.PreReleaseBranch, ad.PreReleaseVersion); e != nil || !downloadVersion.MatchString(ad.PreReleaseVersion) {
		return input, gameDelivery{}, base, nil
	}
	d, err := b.preloadQuery(ctx, ad.PreReleaseBranch, ad.PreReleaseVersion)
	if err != nil {
		if tolerateQueryErr && ctx.Err() == nil {
			return input, gameDelivery{}, base, nil
		}
		return input, gameDelivery{}, base, err
	}
	d, input.Snapshot, err = downloadPreloadEvidence(d, b.metadata().roots, true)
	if err != nil {
		if tolerateQueryErr && ctx.Err() == nil {
			return input, gameDelivery{}, base, nil
		}
		return input, gameDelivery{}, base, err
	}
	if d.Branch != ad.PreReleaseBranch || d.Config.ResVersion != ad.PreReleaseVersion {
		if tolerateQueryErr {
			input.Snapshot = gameinstall.PreloadManifest{}
			return input, gameDelivery{}, base, nil
		}
		return input, gameDelivery{}, base, errDownloadMetadata
	}
	return input, d, base, nil
}

func (b *GameDownloadBackend) planPreload(ctx context.Context, req launcher.DownloadRequest, event state.Event, report func(state.Event)) (state.Event, error) {
	event.Kind = state.DownloadPlanFailed
	input, delivery, base, err := b.preloadDiscovery(ctx, req.Root)
	if err != nil {
		return event, err
	}
	verdict := gameinstall.EvaluatePreload(input)
	if verdict.State != gameinstall.PreloadAvailable {
		return event, errors.New(verdict.Reason)
	}
	p, err := b.buildDownloadPlan(ctx, req.Root, state.DownloadPreload, delivery, base, "")
	if err != nil {
		return event, err
	}
	lease, err := preloadSystemRootLease(p.root)
	if err != nil {
		return event, err
	}
	defer lease.Close()
	_, current, local, err := b.preloadBase(ctx, p.root)
	if err != nil {
		return event, err
	}
	if current.Version != base.Version || current.ListHash != base.ListHash || current.ManifestDigest != base.ManifestDigest {
		return event, gameinstall.ErrStaleState
	}
	draft, err := gameinstall.StartPreload(ctx, input)
	if err != nil {
		return event, err
	}
	p.workRoot = preloadSystemWork(p.root, draft.Record)
	if err = preloadSystemMakeWork(p.root, draft.Record); err != nil {
		return event, err
	}
	engine, err := b.newDownloadEngine(ctx, p, nil)
	if err != nil {
		return event, err
	}
	defer engine.Close()
	for _, file := range draft.Record.Files {
		f, e := engine.OpenBlob(ctx, file.Object)
		if e == nil {
			local.cached[file.Object] = true
			f.Close()
		} else if ctx.Err() != nil {
			return event, ctx.Err()
		}
	}
	input.Local = local
	planned, err := gameinstall.StartPreload(ctx, input)
	if err != nil {
		return event, err
	}
	store, err := b.preloadStore(p.root)
	if err != nil {
		return event, err
	}
	planned.Record.TaskID = p.id
	if err = store.Save(ctx, planned.Record); err != nil {
		return event, err
	}
	p.preload = &planned.Record
	p.plan = planned.Plan
	p.need = p.plan.PeakAllocBytes + p.margin
	return b.presentDownloadPlan(p, event, report)
}

func (b *GameDownloadBackend) preloadCommand(ctx context.Context, req launcher.DownloadRequest, event state.Event, report func(state.Event)) (state.Event, error) {
	if req.Effect.Kind == state.EffectPreloadDiscard {
		return b.preloadDiscard(ctx, req, event)
	}
	event.Kind = state.PreloadChecked
	if req.Effect.Enabled {
		return b.preloadReconcile(ctx, req, event, report)
	}
	input, _, _, err := b.preloadDiscovery(ctx, req.Root)
	if err != nil {
		return event, err
	}
	verdict := gameinstall.EvaluatePreload(input)
	event.Enabled = verdict.State == gameinstall.PreloadAvailable
	event.Message, event.Body = verdict.Version, verdict.Reason
	if input.Advertisement.PreReleaseStatus == "start" && b.preloadQuery == nil {
		event.Body = "unverified"
	}
	return event, nil
}

// The engine has already validated every transfer. Reopen each retained blob to
// bind the persisted staged state to the dedicated cache, without copying it.
func (b *GameDownloadBackend) preloadStaged(ctx context.Context, p *downloadPlan, engine *gamedl.Engine) error {
	if p.preload == nil {
		return gameinstall.ErrPreloadRecord
	}
	r := *p.preload
	r.TaskID = p.id
	r.Files = append([]gameinstall.PreloadFile(nil), r.Files...)
	for i, file := range r.Files {
		f, err := engine.OpenBlob(ctx, file.Object)
		if err != nil {
			return err
		}
		if err = f.Close(); err != nil {
			return err
		}
		r.Files[i].State = gameinstall.PreloadFileStaged
	}
	store, err := b.preloadStore(p.root)
	if err != nil {
		return err
	}
	// Planning/download state is independent of the engine's resumable task state.
	r.State = gameinstall.PreloadVerifying
	r.UpdatedAt = time.Now().UTC()
	if err = store.Save(ctx, r); err != nil {
		return err
	}
	r, err = store.Transition(ctx, r, gameinstall.PreloadStaged, time.Now().UTC())
	if err != nil {
		return err
	}
	r, err = store.Transition(ctx, r, gameinstall.PreloadWaitingRelease, time.Now().UTC())
	if err != nil {
		return err
	}
	p.preload = &r
	// The preload record now owns these verified blobs; a resumable transfer task
	// must not survive as a second independent reference after staging.
	err = engine.Retire(ctx, p.id)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
func (b *GameDownloadBackend) preloadCommitted(ctx context.Context, p *downloadPlan) error {
	if p.preload == nil {
		return nil
	}
	store, err := b.preloadStore(p.root)
	if err != nil {
		return err
	}
	r, err := store.Load(ctx, p.preload.Branch, p.preload.Version)
	if err != nil {
		return err
	}
	if r.State == gameinstall.PreloadCompleted {
		return nil
	}
	r, err = store.Transition(ctx, r, gameinstall.PreloadCompleted, time.Now().UTC())
	if err == nil {
		p.preload = &r
	}
	return err
}
func (b *GameDownloadBackend) preloadRestore(ctx context.Context, root string) (string, string, error) {
	r, err := b.preloadLatest(ctx, root)
	if errors.Is(err, os.ErrNotExist) {
		return "", "", nil
	}
	if err != nil {
		return "", "", err
	}
	lease, err := preloadSystemRootLease(root)
	if err != nil {
		return "", "", err
	}
	defer lease.Close()
	store, err := b.preloadStore(root)
	if err != nil {
		return "", "", err
	}
	if r.State == gameinstall.PreloadApplying || r.State == gameinstall.PreloadRecoveryRequired {
		return r.Version, "recovery-required", gameinstall.ErrRecoveryRequired
	}
	if r.State == gameinstall.PreloadStale || time.Since(r.SavedAt) > 14*24*time.Hour {
		return r.Version, "stale", nil
	}
	if _, err = downloadDirectory(preloadSystemWork(root, r), false); err != nil {
		// A record can outlive its staging directory (the user or a cleaner
		// removed it). Reporting an error here would demand recovery on every
		// startup and block an ordinary hotfix resume, so the preload is simply
		// stale: it owns no bytes and the next reconcile re-plans from scratch.
		if errors.Is(err, os.ErrNotExist) {
			return r.Version, "stale", nil
		}
		return r.Version, "stale", err
	}
	stage, err := gamedl.Acquire(preloadSystemWork(root, r))
	if err != nil {
		return "", "", err
	}
	defer stage.Close()
	r, err = store.Recover(ctx, r, time.Now().UTC())
	if err != nil {
		return "", "", err
	}
	switch r.State {
	case gameinstall.PreloadStaged, gameinstall.PreloadWaitingRelease, gameinstall.PreloadReconcile, gameinstall.PreloadApplicable:
		return r.Version, "waiting-release", nil
	default:
		return r.Version, "stale", nil
	}
}

func preloadSystemOwnTask(task gamedl.Task, r gameinstall.PreloadRecord, key string) bool {
	return task.Kind == gamedl.TaskPreloadFiles && task.InstallKey == key && task.ListHash == r.ListHash && (task.ID == r.TaskID || task.Branch == r.Branch && task.TargetVersion == r.Version)
}

func (b *GameDownloadBackend) preloadReferenceScan(ctx context.Context, root string, discard gameinstall.PreloadRecord) (map[gameres.Object]bool, error) {
	refs := map[gameres.Object]bool{}
	key, err := gameinstall.InstallKey(root)
	if err != nil {
		return nil, err
	}
	tasks, err := gamedl.NewStore(b.DB, b.taskRoot(key))
	if err != nil {
		return nil, err
	}
	defer tasks.Close()
	list, err := tasks.List(ctx)
	if err != nil {
		return nil, err
	}
	for _, task := range list {
		if preloadSystemOwnTask(task, discard, key) {
			continue
		}
		for _, transfer := range task.Transfers {
			refs[transfer.Object] = true
		}
		for _, target := range task.Targets {
			refs[target.Object] = true
		}
		for _, rebuild := range task.Rebuilds {
			refs[rebuild.Target] = true
			for _, step := range rebuild.Steps {
				refs[step.Old] = true
				refs[step.New] = true
				refs[step.Blob] = true
			}
		}
	}
	records, err := b.preloadRecords(ctx, root)
	if err != nil {
		return nil, err
	}
	for _, other := range records {
		if other.Branch == discard.Branch && other.Version == discard.Version {
			continue
		}
		for _, file := range other.Files {
			refs[file.Object] = true
		}
	}
	// Retained transaction journals are separately owned and cannot be inferred
	// from file names. Refuse GC while the transaction layer reports pending work.
	install, err := b.installStore()
	if err != nil {
		return nil, err
	}
	status, err := install.TransactionStatus(ctx, root, downloadWork(root))
	if err != nil {
		return nil, err
	}
	if status.LaunchBlocked || status.Resume {
		return nil, gameinstall.ErrRecoveryRequired
	}
	return refs, nil
}
func (b *GameDownloadBackend) preloadDiscard(ctx context.Context, req launcher.DownloadRequest, event state.Event) (state.Event, error) {
	event.Kind = state.PreloadFailed
	if err := b.guard(ctx, false); err != nil {
		return event, err
	}
	r, err := b.preloadLatest(ctx, req.Root)
	if errors.Is(err, os.ErrNotExist) {
		event.Kind = state.PreloadDiscarded
		return event, nil
	}
	if err != nil {
		return event, err
	}
	lease, err := preloadSystemRootLease(req.Root)
	if err != nil {
		return event, err
	}
	defer lease.Close()
	store, err := b.preloadStore(req.Root)
	if err != nil {
		return event, err
	}
	if r.State == gameinstall.PreloadRecoveryRequired || r.State == gameinstall.PreloadApplying {
		return event, gameinstall.ErrRecoveryRequired
	}
	if _, err = downloadDirectory(preloadSystemWork(req.Root, r), false); err == nil {
		stage, e := gamedl.Acquire(preloadSystemWork(req.Root, r))
		if e != nil {
			return event, e
		}
		defer func() {
			if stage != nil {
				_ = stage.Close()
			}
		}()
		var hadRefs bool
		if err = gameinstall.DiscardPreload(ctx, store, r, func(ctx context.Context) (map[gameres.Object]bool, error) {
			refs, e := b.preloadReferenceScan(ctx, req.Root, r)
			hadRefs = len(refs) > 0
			return refs, e
		}); err != nil {
			return event, err
		}
		_ = stage.Close()
		stage = nil
		if !hadRefs {
			b.cleanupPreloadWork(req.Root, r)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return event, err
	} else if err = gameinstall.DiscardPreload(ctx, store, r, func(ctx context.Context) (map[gameres.Object]bool, error) {
		return b.preloadReferenceScan(ctx, req.Root, r)
	}); err != nil {
		return event, err
	}
	r, err = store.Load(ctx, r.Branch, r.Version)
	if err != nil {
		return event, err
	}
	if _, err = store.Transition(ctx, r, gameinstall.PreloadUnavailable, time.Now().UTC()); err != nil {
		return event, err
	}
	if p := b.getPlan(req.Root); p != nil && p.preload != nil && strings.EqualFold(p.preload.Branch, r.Branch) && p.preload.Version == r.Version {
		b.putPlan(req.Root, nil)
	}
	event.Kind = state.PreloadDiscarded
	return event, nil
}

// cleanupPreloadWork removes one completed or discarded preload's dedicated
// subtree after its engine and stage lease have closed. The caller holds the
// root installation lease. Failures are non-fatal maintenance.
func (b *GameDownloadBackend) cleanupPreloadWork(root string, r gameinstall.PreloadRecord) {
	if r.Branch == "" || r.Version == "" {
		return
	}
	for _, part := range []string{r.Branch, r.Version} {
		if strings.ContainsAny(part, `/\`) || part == "." || part == ".." {
			return
		}
		if _, err := gameres.NormalizePath("X/" + part); err != nil {
			return
		}
	}
	work := preloadSystemWork(root, r)
	if _, err := downloadDirectory(work, false); err != nil {
		return
	}
	safe := true
	_ = filepath.WalkDir(work, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			safe = false
			return err
		}
		info, lerr := d.Info()
		if lerr != nil || downloadReparse(info) {
			safe = false
			return gameres.ErrPathUnsafe
		}
		return nil
	})
	if !safe {
		return
	}
	anchor, err := os.OpenRoot(downloadWork(root))
	if err != nil {
		return
	}
	defer anchor.Close()
	_ = anchor.RemoveAll(filepath.Join("preload", r.Branch, r.Version))
	_ = anchor.Remove(filepath.Join("preload", r.Branch))
	_ = anchor.Remove("preload")
}
