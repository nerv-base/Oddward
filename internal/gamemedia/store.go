// SPDX-License-Identifier: GPL-3.0-or-later
package gamemedia

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/nerv-base/Oddward/internal/database"
)

// The index of this cache is asset_cache: one row per icon, keyed by the same
// <kind>-<id>.png name as the file on disk. Image bytes never enter the database,
// so losing the database can never lose an image: the fetch path adopts a valid
// file it finds without a row.
const (
	assetKind = "icon"
	// A hit spends its frame budget on pixels, not SQL. last_used updates are
	// collected in memory and written as one transaction after this debounce, so
	// a screenful of hits costs one or two writes instead of one write per icon.
	usageDebounce = 250 * time.Millisecond
	// A background revalidation that failed must not be retried on every frame:
	// the bytes on disk are still valid, only their freshness is unconfirmed.
	revalidateBackoff = 5 * time.Minute
	maxValidatorBytes = 4096
)

// cachedIcon is one asset_cache row for this cache.
type cachedIcon struct {
	size         int64
	etag         string
	lastModified string
	fetchedAt    time.Time
	expiresAt    time.Time
	lastUsed     time.Time
	missing      bool
}

const selectRowSQL = `
SELECT status, size, etag, last_modified, fetched_at, expires_at, last_used
FROM asset_cache
WHERE kind = ? AND key = ?`

// Every column describes this one image, so the row is replaced wholesale.
// digest and detail are reserved for other cache users and stay empty here.
const upsertRowSQL = `
INSERT INTO asset_cache
	(kind, key, path, size, digest, source_url, etag, last_modified, fetched_at, expires_at, last_used, status, detail)
VALUES (?, ?, ?, ?, '', ?, ?, ?, ?, ?, ?, ?, '')
ON CONFLICT(kind, key) DO UPDATE SET
	path = excluded.path,
	size = excluded.size,
	digest = '',
	source_url = excluded.source_url,
	etag = excluded.etag,
	last_modified = excluded.last_modified,
	fetched_at = excluded.fetched_at,
	expires_at = excluded.expires_at,
	last_used = excluded.last_used,
	status = excluded.status,
	detail = ''`

const deleteRowSQL = `DELETE FROM asset_cache WHERE kind = ? AND key = ?`
const touchRowSQL = `UPDATE asset_cache SET last_used = ? WHERE kind = ? AND key = ?`

// lookup reads one index row. Every database problem (absent table, corrupt
// file, closed handle) degrades to "no row" instead of an error: a broken index
// must never stop a valid file on disk from being served.
func (c *Cache) lookup(ctx context.Context, key Key) (cachedIcon, bool) {
	var (
		status                         string
		size                           int64
		etag, lastModified             string
		fetchedAt, expiresAt, lastUsed sql.NullInt64
	)
	if err := c.db.QueryRow(ctx, selectRowSQL, assetKind, key.filename()).Scan(&status, &size, &etag, &lastModified, &fetchedAt, &expiresAt, &lastUsed); err != nil {
		return cachedIcon{}, false
	}
	// Never trust a row's size or validators for I/O decisions; the file path is
	// always derived from the validated Key, so a corrupt row cannot redirect a
	// read outside the cache directory.
	if size < 0 || size > maxIconBytes {
		size = 0
	}
	row := cachedIcon{
		size:         size,
		etag:         validValidator(etag),
		lastModified: validValidator(lastModified),
		fetchedAt:    database.StampTime(fetchedAt),
		expiresAt:    database.StampTime(expiresAt),
		lastUsed:     database.StampTime(lastUsed),
		missing:      status == "missing",
	}
	if status == "stale" {
		// "stale" means the source no longer vouches for the bytes; treat the row
		// as expired so the next use revalidates instead of trusting it.
		row.expiresAt = row.fetchedAt
	}
	return row, true
}

func validValidator(value string) string {
	if len(value) > maxValidatorBytes {
		return ""
	}
	return value
}

// store writes one row. The asset_cache columns are NOT NULL, so a caller that
// left a timestamp zero still stores a usable row instead of failing the write.
func (c *Cache) store(ctx context.Context, key Key, row cachedIcon) error {
	now := c.now().UTC()
	if row.fetchedAt.IsZero() {
		row.fetchedAt = now
	}
	if row.expiresAt.IsZero() {
		row.expiresAt = now
	}
	if row.lastUsed.IsZero() {
		row.lastUsed = now
	}
	status := "ok"
	if row.missing {
		status = "missing"
	}
	_, err := c.db.Exec(ctx, upsertRowSQL, assetKind, key.filename(), key.filename(), row.size,
		c.base+key.remotePath(), row.etag, row.lastModified,
		database.Stamp(row.fetchedAt), database.Stamp(row.expiresAt), database.Stamp(row.lastUsed), status)
	return err
}

func (c *Cache) deleteRow(ctx context.Context, key Key) error {
	_, err := c.db.Exec(ctx, deleteRowSQL, assetKind, key.filename())
	return err
}

// touch records a use in memory. The hit path must not pay for a write, so the
// update is deferred to flushUsage's batch.
func (c *Cache) touch(key Key, at time.Time) {
	c.usageMu.Lock()
	defer c.usageMu.Unlock()
	if c.usage == nil {
		c.usage = make(map[Key]time.Time)
	}
	if current, ok := c.usage[key]; !ok || at.After(current) {
		c.usage[key] = at
	}
	if c.usageTimer == nil {
		c.usageTimer = time.AfterFunc(usageDebounce, func() {
			_ = c.flushUsage(context.Background())
		})
	}
}

// flushUsage writes the collected last_used values as one transaction holding
// one small UPDATE per touched key. It never rewrites other columns or rows.
func (c *Cache) flushUsage(ctx context.Context) error {
	c.usageMu.Lock()
	pending := c.usage
	c.usage = nil
	if timer := c.usageTimer; timer != nil {
		timer.Stop()
		c.usageTimer = nil
	}
	c.usageMu.Unlock()
	if len(pending) == 0 {
		return nil
	}
	err := c.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for key, at := range pending {
			if _, err := tx.ExecContext(ctx, touchRowSQL, database.Stamp(at), assetKind, key.filename()); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		// Keep the batch for a retry, except for failures that cannot recover in
		// this process: those would only schedule timers forever.
		if !errors.Is(err, database.ErrClosed) && !errors.Is(err, database.ErrCorrupt) {
			c.requeueUsage(pending)
		}
		return err
	}
	c.usageBatches.Add(1)
	c.usageUpdates.Add(int64(len(pending)))
	return nil
}

func (c *Cache) requeueUsage(pending map[Key]time.Time) {
	c.usageMu.Lock()
	defer c.usageMu.Unlock()
	if c.usage == nil {
		c.usage = make(map[Key]time.Time)
	}
	for key, at := range pending {
		if current, ok := c.usage[key]; !ok || at.After(current) {
			c.usage[key] = at
		}
	}
	if c.usageTimer == nil {
		c.usageTimer = time.AfterFunc(usageDebounce, func() {
			_ = c.flushUsage(context.Background())
		})
	}
}
