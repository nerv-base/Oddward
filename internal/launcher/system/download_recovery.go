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

type downloadMutation windows.Handle

// Keep an exclusive kernel object alive until launch-start or transaction
// completion. Downloads use a separate work-root lease and can run meanwhile.
func acquireDownloadMutation(root string) (downloadMutation, error) {
	key, err := gameinstall.InstallKey(root)
	if err != nil {
		return 0, err
	}
	name, err := windows.UTF16PtrFromString(`Global\Oddward.GameMutation.` + key)
	if err != nil {
		return 0, err
	}
	h, err := windows.CreateMutex(nil, false, name)
	if err != nil {
		if h != 0 {
			_ = windows.CloseHandle(h)
		}
		return 0, errDownloadMutation
	}
	return downloadMutation(h), nil
}
func (h downloadMutation) Close() error { return windows.CloseHandle(windows.Handle(h)) }

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
	event.Kind = state.DownloadRecoveryCleared
	event.CurrentVersion = base.Version
	event.Enabled = base.VerifyCoverage == "full"
	if status.Published {
		event.Message = base.Version
	}
	if req.Effect.Enabled {
		if status.Published {
			if err := b.preloadRecoveredCommit(ctx, root, base); err != nil {
				event.Kind = state.DownloadRecoveryNeeded
				return event, err
			}
		}
		return event, nil
	}
	version, note, err := b.preloadRestore(ctx, root)
	if err != nil {
		return event, err
	}
	if version != "" {
		event.Kind, event.Index, event.Message, event.Body = state.DownloadRestored, uint8(state.DownloadPreload), version, note
		return event, nil
	}
	key, err := gameinstall.InstallKey(root)
	if err != nil {
		return event, err
	}
	if _, err = os.Lstat(b.taskRoot(key)); errors.Is(err, os.ErrNotExist) {
		return event, nil
	} else if err != nil {
		return event, err
	}
	tasks, err := gamedl.NewStore(b.taskRoot(key))
	if err != nil {
		return event, err
	}
	defer tasks.Close()
	records, err := tasks.List(ctx)
	if err != nil {
		return event, err
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
		return event, nil
	}
	kind := state.DownloadUpdate
	if latest.Kind == gamedl.TaskInstallFiles {
		kind = state.DownloadInstall
	}
	p := &downloadPlan{root: root, key: key, id: latest.ID, kind: kind, base: base, resumeTask: latest}
	if latest.CacheRoot != "" {
		p.externalCache, p.workRoot, err = b.externalDownloadCache(root, latest.CacheRoot, key)
		if err != nil {
			event.Kind = state.DownloadRecoveryNeeded
			return event, err
		}
	}
	b.putPlan(root, p)
	event.Kind, event.Index, event.Stage, event.Message = state.DownloadRestored, uint8(kind), state.StageNetwork, latest.TargetVersion
	event.Focused = latest.State == gamedl.StateRunning || latest.State == gamedl.StateVerifying
	return event, nil
}

func (b *GameDownloadBackend) revalidateResumedDownload(ctx context.Context, p *downloadPlan) (*downloadPlan, error) {
	task := p.resumeTask
	delivery, err := b.metadata().delivery(ctx, task.Branch, task.TargetVersion)
	if err != nil {
		return nil, err
	}
	if delivery.Config.ListHash != task.ListHash {
		return nil, gameinstall.ErrStaleState
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
