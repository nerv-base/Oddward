// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"context"
	"time"

	"github.com/nerv-base/Oddward/internal/gamedl"
	"github.com/nerv-base/Oddward/internal/gameres"
)

// The caller holds the installation lease. During a transaction, the
// transaction journal owns all staging and backup cleanup.
func (b *GameDownloadBackend) collectDownloadCache(ctx context.Context, p *downloadPlan, lease *gamedl.Lease) {
	if ctx.Err() != nil || b.guard(ctx, false) != nil {
		return
	}
	store, err := b.installStore()
	if err != nil {
		return
	}
	status, err := store.TransactionStatus(ctx, p.root, downloadWork(p.root))
	if err != nil || status.LaunchBlocked || status.Action == "cleanup_pending" {
		return
	}
	records, err := b.preloadRecords(ctx, p.root)
	if err != nil {
		return
	}
	var protected []gameres.Object
	for _, record := range records {
		for _, file := range record.Files {
			protected = append(protected, file.Object)
		}
	}
	prefs, err := b.preferences(ctx)
	if err != nil {
		return
	}
	options := gamedl.GCOptions{
		WorkRoot: downloadWork(p.root), StateRoot: b.taskRoot(p.key), Lease: lease,
		Protected: protected, MaxBlobBytes: int64(prefs.CacheLimitGiB) << 30, MaxAge: 14 * 24 * time.Hour,
	}
	_, _ = gamedl.Collect(ctx, options)
	if p.externalCache != "" {
		_, work, err := b.externalDownloadCache(p.root, p.externalCache, p.key)
		if err != nil || work != p.engineRoot() {
			return
		}
		cacheLease, err := gamedl.Acquire(work)
		if err != nil {
			return
		}
		defer cacheLease.Close()
		options.WorkRoot, options.Lease = work, cacheLease
		_, _ = gamedl.Collect(ctx, options)
	}
}
