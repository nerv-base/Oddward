// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"context"
	"errors"
	"os"

	"github.com/nerv-base/Oddward/internal/gamedl"
	"github.com/nerv-base/Oddward/internal/gameinstall"
	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/launcher/state"
	"golang.org/x/sys/windows"
)

var errDownloadMutation = errors.New("此安装正在启动或提交资源，请稍后重试")

type downloadMutation struct {
	handle windows.Handle
	owned  bool
}

// Keep an exclusive kernel object alive until launch-start or transaction
// completion. Downloads use a separate work-root lease and can run meanwhile.
func acquireDownloadMutation(root string) (downloadMutation, error) {
	key, err := gameinstall.InstallKey(root)
	if err != nil {
		return downloadMutation{}, err
	}
	name, err := windows.UTF16PtrFromString(`Global\Oddward.GameMutation.` + key)
	if err != nil {
		return downloadMutation{}, err
	}
	h, createErr := windows.CreateMutex(nil, false, name)
	if h == 0 {
		return downloadMutation{}, errDownloadMutation
	}
	wait, waitErr := windows.WaitForSingleObject(h, 0)
	if waitErr != nil || (wait != windows.WAIT_OBJECT_0 && wait != windows.WAIT_ABANDONED) {
		_ = windows.CloseHandle(h)
		if waitErr != nil {
			return downloadMutation{}, errDownloadMutation
		}
		return downloadMutation{}, errDownloadMutation
	}
	if createErr != nil && !errors.Is(createErr, windows.ERROR_ALREADY_EXISTS) {
		_ = windows.ReleaseMutex(h)
		_ = windows.CloseHandle(h)
		return downloadMutation{}, errDownloadMutation
	}
	return downloadMutation{handle: h, owned: true}, nil
}
func (h *downloadMutation) Close() error {
	if h == nil || h.handle == 0 {
		return nil
	}
	handle, owned := h.handle, h.owned
	h.handle, h.owned = 0, false
	var releaseErr error
	if owned {
		releaseErr = windows.ReleaseMutex(handle)
	}
	closeErr := windows.CloseHandle(handle)
	return errors.Join(releaseErr, closeErr)
}

func (b *GameDownloadBackend) checkLaunch(ctx context.Context, root string) error {
	store, err := b.installStore()
	if err != nil {
		return err
	}
	status, err := store.TransactionStatus(ctx, root, downloadWork(root))
	if err != nil {
		return err
	}
	if status.LaunchBlocked {
		return gameinstall.ErrRecoveryRequired
	}
	return b.guard(ctx, true)
}

func (b *GameDownloadBackend) recoverDownload(ctx context.Context, req launcher.DownloadRequest, event state.Event) (state.Event, error) {
	event.Kind = state.DownloadRecoveryNeeded
	root, err := downloadDirectory(req.Root, true)
	if err != nil {
		return event, err
	}
	store, err := b.installStore()
	if err != nil {
		return event, err
	}
	status, err := store.TransactionStatus(ctx, root, downloadWork(root))
	if err != nil {
		return event, err
	}
	if status.LaunchBlocked && !req.Effect.Enabled {
		return event, gameinstall.ErrRecoveryRequired
	}
	locked := false
	if req.Effect.Enabled && (status.LaunchBlocked || status.Published) {
		lock, err := acquireDownloadMutation(root)
		if err != nil {
			return event, err
		}
		defer lock.Close()
		lease, err := gamedl.Acquire(downloadWork(root))
		if err != nil {
			return event, err
		}
		defer lease.Close()
		locked = true
		guard := func(ctx context.Context) error { return b.guard(ctx, true) }
		status, err = store.Recover(ctx, root, downloadWork(root), guard)
		if err != nil && !(status.Published && !status.LaunchBlocked && errors.Is(err, gameinstall.ErrCleanupPending)) {
			return event, err
		}
		if status.Resume {
			status, err = store.ResumeCommit(ctx, root, downloadWork(root), guard, func(context.Context) (int64, error) { return b.space(root) }, 2<<30)
			if err != nil {
				return event, err
			}
		}
		if status.LaunchBlocked {
			return event, gameinstall.ErrRecoveryRequired
		}
	}
	base, err := store.ReadInstall(root)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return event, err
	}
	event.CurrentVersion = base.Version
	event.Enabled = base.VerifyCoverage == "full"
	if status.Published {
		event.Message = base.Version
	}
	if req.Effect.Enabled {
		if status.Published {
			completed, cerr := b.preloadRecoveredCommit(ctx, root, base)
			if cerr != nil {
				event.Kind = state.DownloadRecoveryNeeded
				return event, cerr
			}
			if completed {
				event.Body = "completed"
			}
		}
		// A published record only proves the *last* transaction committed: an
		// earlier applied install keeps Published true, so releasing a preload
		// parked in applying cannot be skipped here. The precheck keeps a clean
		// installation from taking the locks an apply would need.
		pending, perr := b.preloadRecoveryPending(ctx, root)
		if perr != nil {
			event.Kind = state.DownloadRecoveryNeeded
			return event, perr
		}
		if !pending {
			event.Kind = state.DownloadRecoveryCleared
			return event, nil
		}
		// The recovery block above already holds both locks; Windows mutexes and
		// leases are not safely re-entrant from a different thread.
		if !locked {
			lock, lerr := acquireDownloadMutation(root)
			if lerr != nil {
				event.Kind = state.DownloadRecoveryNeeded
				return event, lerr
			}
			defer lock.Close()
			lease, lerr := preloadSystemRootLease(root)
			if lerr != nil {
				event.Kind = state.DownloadRecoveryNeeded
				return event, lerr
			}
			defer lease.Close()
		}
		stale, rerr := b.preloadRecoveredRollback(ctx, root, base)
		if rerr != nil {
			event.Kind = state.DownloadRecoveryNeeded
			return event, rerr
		}
		if stale && event.Body == "" {
			event.Body = "stale"
		}
		event.Kind = state.DownloadRecoveryCleared
		return event, nil
	}
	// A record parked in applying/recovery means the last apply was interrupted.
	// Releasing it stays the highest priority so the next reconcile or apply
	// starts from a usable record instead of a permanently stuck one.
	version, note, err := b.preloadRestore(ctx, root)
	if err != nil {
		return event, err
	}
	// A paused ordinary hotfix owns the plan and work tree; returning the staged
	// preload first would hide it until the user happened to start an update.
	event, found, err := b.restoreDownloadTask(ctx, root, base, event)
	if err != nil {
		event.Kind = state.DownloadRecoveryNeeded
		return event, err
	}
	if found {
		// The resumed task keeps the plan, but the preload record read above still
		// describes bytes on disk. Carrying it on the same event keeps the chip
		// available once the hotfix finishes instead of hiding a staged preload
		// until the next startup scan.
		if version != "" && note != "" {
			event.Body = note + ":" + version
		}
		return event, nil
	}
	if version != "" {
		event.Kind, event.Index, event.Message, event.Body = state.DownloadRestored, uint8(state.DownloadPreload), version, note
		return event, nil
	}
	event.Kind = state.DownloadRecoveryCleared
	return event, nil
}

// restoreDownloadTask republishes the newest interrupted ordinary task so a
// paused rename or hotfix resumes after a restart. A staged preload record is
// deliberately left in place: it is independent of that task and stays
// available for the next reconcile.
func (b *GameDownloadBackend) restoreDownloadTask(ctx context.Context, root string, base gameinstall.InstallRecord, event state.Event) (state.Event, bool, error) {
	key, err := gameinstall.InstallKey(root)
	if err != nil {
		return event, false, err
	}
	if _, err = os.Lstat(b.taskRoot(key)); errors.Is(err, os.ErrNotExist) {
		return event, false, nil
	} else if err != nil {
		return event, false, err
	}
	tasks, err := gamedl.NewStore(b.DB, b.taskRoot(key))
	if err != nil {
		return event, false, err
	}
	defer tasks.Close()
	records, err := tasks.List(ctx)
	if err != nil {
		return event, false, err
	}
	var latest *gamedl.Task
	for i := range records {
		r := &records[i]
		if r.InstallKey != key || r.Kind != gamedl.TaskInstallFiles && r.Kind != gamedl.TaskUpdateFiles || r.State == gamedl.StateStale || r.State == gamedl.StateDone || r.ID == base.TxnID || r.Purpose != "game-resources" {
			continue
		}
		if latest == nil || r.UpdatedAt.After(latest.UpdatedAt) {
			latest = r
		}
	}
	if latest == nil {
		return event, false, nil
	}
	kind := state.DownloadUpdate
	if latest.Kind == gamedl.TaskInstallFiles {
		kind = state.DownloadInstall
	}
	p := &downloadPlan{root: root, key: key, id: latest.ID, kind: kind, base: base, resumeTask: latest}
	if latest.CacheRoot != "" {
		p.externalCache, p.workRoot, err = b.externalDownloadCache(root, latest.CacheRoot, key)
		if err != nil {
			return event, false, err
		}
	}
	b.putPlan(root, p)
	event.Kind, event.Index, event.Stage, event.Message = state.DownloadRestored, uint8(kind), state.StageNetwork, latest.TargetVersion
	event.Focused = latest.State == gamedl.StateRunning || latest.State == gamedl.StateVerifying
	return event, true, nil
}

func (b *GameDownloadBackend) revalidateResumedDownload(ctx context.Context, p *downloadPlan) (*downloadPlan, error) {
	task := p.resumeTask
	// Resolve through the trusted mirror's branch migration so a resumed task
	// cannot keep downloading a retired branch after <ResConfig> moved it.
	delivery, err := b.metadata().formalDelivery(ctx, task.Branch)
	if err != nil {
		return nil, err
	}
	if delivery.Branch != task.Branch || delivery.Config.ResVersion != task.TargetVersion || delivery.Config.ListHash != task.ListHash {
		// Leaving the row running would pick it again on the next start (and
		// auto-resume a dead branch), so the stale task is retired on disk and
		// its in-memory plan is dropped.
		staleErr := b.markResumedTaskStale(ctx, p)
		b.putPlan(p.root, nil)
		return nil, errors.Join(gameinstall.ErrStaleState, staleErr)
	}
	next, err := b.buildDownloadPlan(ctx, p.root, p.kind, delivery, p.base, task.CacheRoot)
	if err != nil {
		return nil, err
	}
	// A new in-memory plan uses authoritative source paths. Existing bitmaps and
	// blobs are global per identity, so old task JSON is never used as truth.
	next.supersedes = p.id
	b.putPlan(p.root, next)
	return next, nil
}

// markResumedTaskStale retires the rejected task on disk so the next scan no
// longer resumes a migrated branch. The caller keeps ErrStaleState first so the
// UI still reports a stale record, with the store failure appended for logs.
func (b *GameDownloadBackend) markResumedTaskStale(ctx context.Context, p *downloadPlan) error {
	tasks, err := gamedl.NewStore(b.DB, b.taskRoot(p.key))
	if err != nil {
		return err
	}
	defer tasks.Close()
	if err = tasks.UpdateTaskState(ctx, p.resumeTask.ID, gamedl.StateStale, ""); err != nil {
		return err
	}
	return nil
}
