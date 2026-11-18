// SPDX-License-Identifier: GPL-3.0-or-later
package system

import (
	"context"
	"strings"
	"time"

	"github.com/nerv-base/Oddward/internal/gameinstall"
)

// preloadApplying is the last metadata step before the transaction begins.
// Setup failures before this call leave a recheckable applicable record.
func (b *GameDownloadBackend) preloadApplying(ctx context.Context, p *downloadPlan) error {
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
	r.ApplyTxnID = p.id
	r, err = store.Transition(ctx, r, gameinstall.PreloadApplying, time.Now().UTC())
	if err == nil {
		p.preload = &r
	}
	return err
}

// The installation lease remains held. A transaction that has started owns
// recovery; a failure before its journal exists can safely require reconciliation.
func (b *GameDownloadBackend) preloadApplyFailed(ctx context.Context, p *downloadPlan) error {
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
	if r.State != gameinstall.PreloadApplying {
		return nil
	}
	install, err := b.installStore()
	if err != nil {
		return err
	}
	status, err := install.TransactionStatus(ctx, p.root, downloadWork(p.root))
	if err != nil {
		return err
	}
	if status.LaunchBlocked || status.Resume {
		return nil
	}
	if status.Published && status.TxnID == r.ApplyTxnID {
		published, e := install.ReadInstall(p.root)
		if e != nil {
			return e
		}
		return b.preloadRecoveredCommit(ctx, p.root, published)
	}
	r, err = store.Transition(ctx, r, gameinstall.PreloadStale, time.Now().UTC())
	if err == nil {
		p.preload = &r
	}
	return err
}

// A published install record is supplied by the caller after transaction
// recovery. Version alone is never sufficient publication evidence.
func (b *GameDownloadBackend) preloadRecoveredCommit(ctx context.Context, root string, published gameinstall.InstallRecord) error {
	records, err := b.preloadRecords(ctx, root)
	if err != nil {
		return err
	}
	key, err := gameinstall.InstallKey(root)
	if err != nil {
		return err
	}
	if published.InstallKey != key || !strings.EqualFold(published.Root, root) {
		return gameinstall.ErrStaleState
	}
	store, err := b.preloadStore(root)
	if err != nil {
		return err
	}
	for _, r := range records {
		if r.State != gameinstall.PreloadApplying && r.State != gameinstall.PreloadRecoveryRequired {
			continue
		}
		if published.State != "ready" || r.ApplyTxnID == "" || r.ApplyTxnID != published.TxnID {
			continue
		}
		if r.ListHash != published.ListHash || len(r.Targets) != len(published.Targets) {
			return gameinstall.ErrStaleState
		}
		targets := map[string]gameinstall.InstallTarget{}
		for _, target := range published.Targets {
			targets[strings.ToUpper(target.Path)] = target
		}
		for _, expected := range r.Targets {
			actual, ok := targets[strings.ToUpper(expected.Path)]
			if !ok || actual.MD5 != expected.Object.MD5 || actual.Size != expected.Object.Size {
				return gameinstall.ErrStaleState
			}
		}
		if _, err = store.Transition(ctx, r, gameinstall.PreloadCompleted, time.Now().UTC()); err != nil {
			return err
		}
	}
	return nil
}
