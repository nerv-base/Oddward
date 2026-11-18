// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"time"

	"github.com/nerv-base/Oddward/internal/gamedl"
	"github.com/nerv-base/Oddward/internal/gameinstall"
	"github.com/nerv-base/Oddward/internal/gameres"
	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func (p *downloadPlan) engineRoot() string {
	if p.workRoot != "" {
		return p.workRoot
	}
	return downloadWork(p.root)
}

func (p *downloadPlan) transferTask() gamedl.Task {
	if p.resumeTask != nil {
		return *p.resumeTask
	}
	kind := gamedl.TaskUpdateFiles
	if p.kind == state.DownloadInstall {
		kind = gamedl.TaskInstallFiles
	}
	if p.kind == state.DownloadPreload {
		kind = gamedl.TaskPreloadFiles
	}
	task := gamedl.Task{ID: p.id, InstallKey: p.key, Purpose: "game-resources", Branch: p.delivery.Branch, TargetVersion: p.delivery.Config.ResVersion, ListHash: p.delivery.Config.ListHash, Kind: kind, State: gamedl.StatePlanning}
	task.CacheRoot = p.externalCache
	blocks := make(map[gameres.Object][]gameres.Block)
	for _, f := range p.delivery.Manifest.Files {
		blocks[gameres.Object{MD5: f.MD5, Size: f.Size}] = f.Blocks
	}
	seen := make(map[gameres.Object]bool)
	add := func(id gameres.Object, source gameres.Source, parts []gameres.Block) {
		if seen[id] {
			return
		}
		seen[id] = true
		task.Transfers = append(task.Transfers, gamedl.Transfer{Object: id, Source: source, Blocks: parts})
	}
	for _, t := range p.plan.Targets {
		id := gameres.Object{MD5: t.MD5, Size: t.Size}
		task.Targets = append(task.Targets, gamedl.InstallTarget{Path: t.Path, Object: id, Action: t.Action, State: gamedl.TargetPending})
		switch t.Action {
		case gameres.ActionFull:
			add(id, t.Full, blocks[id])
		case gameres.ActionInstallCached:
			// A retained blob has already been verified. Its CAS identity gives no
			// permission to skip the next verification inside the engine.
			add(id, gameres.Source{Kind: gameres.SourceCAS, Object: id, Size: id.Size}, blocks[id])
		case gameres.ActionPatch:
			var steps []gamedl.PatchStep
			for _, s := range t.Route.Steps {
				source := gameres.Source{Kind: gameres.SourcePatchBlob, Object: s.Blob, Size: s.Blob.Size}
				add(s.Blob, source, nil)
				steps = append(steps, gamedl.PatchStep{Blob: s.Blob, Old: s.Old, New: s.New, Source: source})
			}
			task.Rebuilds = append(task.Rebuilds, gamedl.Rebuild{Target: id, Steps: steps, State: gamedl.RebuildPending})
		}
	}
	return task
}

func (b *GameDownloadBackend) runDownload(ctx context.Context, req launcher.DownloadRequest, event state.Event, report func(state.Event)) (state.Event, error) {
	event.Kind = state.DownloadFailed
	p := b.getPlan(req.Root)
	if p == nil || p.kind != req.Kind {
		return event, errDownloadPlanMissing
	}
	if err := b.guard(ctx, false); err != nil {
		return event, err
	}
	if p.resumeTask != nil {
		var err error
		p, err = b.revalidateResumedDownload(ctx, p)
		if err != nil {
			return event, err
		}
	}
	if _, err := downloadDirectory(p.root, p.kind == state.DownloadInstall); err != nil {
		return event, err
	}
	if p.kind == state.DownloadInstall {
		if err := checkFreshDownloadTree(p.root); err != nil {
			return event, err
		}
		if err := os.Mkdir(p.root, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return event, err
		}
	}
	lease, err := acquireDownloadWork(p.root)
	if err != nil {
		return event, err
	}
	defer lease.Close()
	if p.kind == state.DownloadPreload {
		if err := b.preparePreloadWork(p); err != nil {
			return event, err
		}
	}
	if p.externalCache != "" {
		if err := b.prepareExternalDownload(p); err != nil {
			return event, err
		}
	}
	e, err := b.openPlanEngine(ctx, p, lease)
	if err != nil {
		return event, err
	}
	defer e.Close()
	if err = b.checkPlanBase(ctx, p); err != nil {
		return event, err
	}
	task := p.transferTask()
	remaining, err := e.Remaining(ctx, task.Transfers)
	if err != nil {
		return event, err
	}
	if err = b.checkDownloadSpace(p, p.margin+remaining); err != nil {
		return event, err
	}
	prefs, err := b.preferences(ctx)
	if err != nil {
		return event, err
	}
	processes, err := b.observe(ctx)
	if err != nil {
		return event, errDownloadProcessCheck
	}
	if processes.official {
		return event, errDownloadOfficial
	}
	start, progress := state.DownloadStarted, state.DownloadProgress
	var updateLimits func(bool) error
	if p.kind == state.DownloadPreload {
		start, progress = state.PreloadStarted, state.PreloadProgress
		updateLimits = func(game bool) error {
			files, connections := prefs.FilesInFlight, prefs.ConnectionsPerFile
			if game {
				files, connections = min(files, 2), min(connections, 2)
			}
			if err := e.SetConcurrencyLimits(files, connections); err != nil {
				return err
			}
			return e.SetRateLimitKiB(preloadGameRate(int64(prefs.RateLimitKiB), game))
		}
		if err := updateLimits(processes.game); err != nil {
			return event, err
		}
	}
	report(state.Event{Kind: start})
	report(state.Event{Kind: state.DownloadProcessesObserved, Focused: processes.game})
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan struct{})
	go b.watchDownloadProcesses(runCtx, stop, done, report, updateLimits)
	defer func() { stop(); <-done }()
	var networkSample PerfSample
	fallback := false
	if err = e.Run(runCtx, task, func(pr gamedl.Progress) {
		if pr.Stage == "staged" {
			networkSample = PerfSample{NetworkBytes: pr.NetworkBytes, NetworkDuration: pr.NetworkActiveDuration}
			return
		}
		if pr.Stage != "downloading" && pr.Stage != "restarting" {
			return
		}
		rate := int64(0)
		if pr.BytesPerSecond > 0 && pr.BytesPerSecond < math.MaxInt64 {
			rate = int64(pr.BytesPerSecond)
		}
		eta := int32(0)
		if pr.ETAKnown && pr.ETA > 0 {
			eta = int32(min(pr.ETA/time.Second, math.MaxInt32))
		}
		report(state.Event{Kind: progress, Stage: state.StageNetwork, Downloaded: pr.Downloaded, Total: pr.Total, Rate: rate, ETASecs: eta, Reused: p.plan.ReusedBytes})
	}); err != nil {
		if runCtx.Err() != nil {
			return event, err
		}
		if paused, ok := downloadDiskPause(event, err); ok {
			return paused, nil
		}
		fallback, err = b.patchDownloadFallback(runCtx, p, e, report, err)
		if err != nil {
			if paused, ok := downloadDiskPause(event, err); ok {
				return paused, nil
			}
			return event, err
		}
	}
	if p.kind != state.DownloadPreload && prefs.RateLimitKiB == 0 && networkSample.NetworkDuration >= 20*time.Second {
		_ = b.recordDownloadPerf(runCtx, p.root, networkSample)
	}
	if p.supersedes != "" {
		if err = e.Retire(runCtx, p.supersedes); err != nil && !errors.Is(err, os.ErrNotExist) {
			return event, err
		}
	}
	if p.kind == state.DownloadPreload {
		if err = b.preloadStaged(runCtx, p, e); err != nil {
			return event, err
		}
		event.Kind = state.PreloadStaged
		return event, nil
	}
	if p.hasPatch() {
		if err = b.guard(runCtx, true); err != nil {
			return event, err
		}
		report(state.Event{Kind: state.ApplyStarted, FilesTotal: uint16(len(task.Rebuilds))})
		appliedWithFallback, err := b.applyDownloadPatches(runCtx, p, e, report)
		if err != nil {
			return event, err
		}
		event.Kind, event.Enabled, event.FilesDone = state.ApplyFinished, fallback || appliedWithFallback, uint16(len(task.Rebuilds))
		return event, nil
	}
	return b.verifyWithEngine(runCtx, p, e, event, report)
}

func (b *GameDownloadBackend) openPlanEngine(ctx context.Context, p *downloadPlan, lease *gamedl.Lease) (*gamedl.Engine, error) {
	if p.engineRoot() != downloadWork(p.root) {
		lease = nil
	}
	return b.newDownloadEngine(ctx, p, lease)
}

func downloadDiskPause(event state.Event, err error) (state.Event, bool) {
	var taskErr *gamedl.TaskError
	if !errors.As(err, &taskErr) || taskErr.Kind != gamedl.ErrDiskFull {
		return event, false
	}
	event.Kind, event.Enabled, event.Body = state.DownloadPaused, true, "disk-full"
	return event, true
}

func preloadGameRate(configured int64, game bool) int64 {
	if !game {
		return configured
	}
	if configured == 0 {
		return 512
	}
	return min(configured, 512)
}

func (b *GameDownloadBackend) watchDownloadProcesses(ctx context.Context, stop context.CancelFunc, done chan<- struct{}, report func(state.Event), updateLimits func(bool) error) {
	defer close(done)
	timer := time.NewTicker(time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			p, err := b.observe(ctx)
			if err != nil || p.official {
				if ctx.Err() == nil {
					report(state.Event{Kind: state.DownloadProcessesObserved, Enabled: true, Focused: p.game})
				}
				stop()
				return
			}
			if updateLimits != nil {
				if err := updateLimits(p.game); err != nil {
					stop()
					return
				}
			}
			report(state.Event{Kind: state.DownloadProcessesObserved, Focused: p.game})
		}
	}
}

func (b *GameDownloadBackend) checkPlanBase(ctx context.Context, p *downloadPlan) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	store, err := b.installStore()
	if err != nil {
		return err
	}
	status, err := store.TransactionStatus(ctx, p.root, downloadWork(p.root))
	if err != nil {
		return err
	}
	if status.LaunchBlocked {
		return gameinstall.ErrRecoveryRequired
	}
	r, err := store.ReadInstall(p.root)
	if p.base.Root == "" && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if r.Version != p.base.Version || r.ListHash != p.base.ListHash || r.ManifestDigest != p.base.ManifestDigest || r.TxnID != p.base.TxnID || r.RootFileID != p.base.RootFileID {
		return gameinstall.ErrStaleState
	}
	return nil
}

func (b *GameDownloadBackend) checkDownloadSpace(p *downloadPlan, want int64) error {
	if p.externalCache != "" {
		return b.checkExternalDownloadSpace(p, want)
	}
	free, err := b.space(p.root)
	if err != nil {
		return err
	}
	if free < want {
		return fmt.Errorf("%w: need %d bytes, have %d bytes", gameres.ErrInsufficientSpace, want, free)
	}
	return nil
}

func (p *downloadPlan) hasPatch() bool {
	return p.plan.PatchBytes > 0 || len(p.transferTask().Rebuilds) > 0
}

func (b *GameDownloadBackend) verifyPlan(ctx context.Context, req launcher.DownloadRequest, event state.Event, report func(state.Event)) (state.Event, error) {
	event.Kind = state.DownloadFailed
	p := b.getPlan(req.Root)
	if p == nil {
		return event, errDownloadPlanMissing
	}
	lease, err := acquireDownloadWork(p.root)
	if err != nil {
		return event, err
	}
	defer lease.Close()
	engine, err := b.openPlanEngine(ctx, p, lease)
	if err != nil {
		return event, err
	}
	defer engine.Close()
	return b.verifyWithEngine(ctx, p, engine, event, report)
}

func (b *GameDownloadBackend) verifyWithEngine(ctx context.Context, p *downloadPlan, e *gamedl.Engine, event state.Event, report func(state.Event)) (state.Event, error) {
	kind := state.DownloadProgress
	if p.kind == state.DownloadPreload {
		kind = state.PreloadProgress
	}
	var total, checked, hashed int64
	var hashDuration time.Duration
	for _, t := range p.plan.Targets {
		if t.Action != gameres.ActionReuseInstalled {
			total += t.Size
		}
	}
	var filesDone uint16
	progress := func() {
		report(state.Event{Kind: kind, Stage: state.StageVerify, Downloaded: checked, Total: total, FilesDone: filesDone, FilesTotal: uint16(len(p.plan.Targets)), Reused: p.plan.ReusedBytes})
	}
	progress()
	for _, t := range p.plan.Targets {
		if err := ctx.Err(); err != nil {
			return event, err
		}
		if t.Action != gameres.ActionReuseInstalled {
			start := time.Now()
			f, err := e.OpenBlob(ctx, gameres.Object{MD5: t.MD5, Size: t.Size})
			if err != nil {
				return event, err
			}
			f.Close()
			hashDuration += time.Since(start)
			hashed += t.Size
			checked += t.Size
		}
		filesDone++
		progress()
	}
	if hashDuration >= 20*time.Second {
		_ = b.recordDownloadPerf(ctx, p.root, PerfSample{HashReadBytes: hashed, HashDuration: hashDuration})
	}
	event.Kind = state.DownloadVerified
	return event, nil
}

func (b *GameDownloadBackend) cancelPlan(ctx context.Context, root string) error {
	p := b.getPlan(root)
	if p == nil {
		return nil
	}
	if _, err := os.Lstat(downloadWork(root)); errors.Is(err, os.ErrNotExist) {
		b.putPlan(root, nil)
		return nil
	}
	lease, err := gamedl.Acquire(downloadWork(root))
	if err != nil {
		return err
	}
	defer lease.Close()
	engine, err := b.openPlanEngine(ctx, p, lease)
	if err != nil {
		return err
	}
	defer engine.Close()
	if err = retirePatchDownloads(ctx, p, engine); err != nil {
		return err
	}
	if err = engine.Retire(ctx, p.id); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_ = engine.Close()
	b.putPlan(root, nil)
	b.collectDownloadCache(ctx, p, lease)
	return nil
}
