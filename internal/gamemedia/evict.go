// SPDX-License-Identifier: GPL-3.0-or-later
package gamemedia

import (
	"context"
	"database/sql"
	"errors"
	"os"

	"github.com/nerv-base/Oddward/internal/database"
)

const countRowsSQL = `SELECT COUNT(*), COALESCE(SUM(size), 0) FROM asset_cache WHERE kind = ?`
const oldestRowsSQL = `SELECT key, size FROM asset_cache WHERE kind = ? ORDER BY last_used ASC, key ASC`

// evict keeps the indexed set inside the row and byte budgets by walking the
// index oldest-first. It never scans the directory: unindexed files (an
// interrupted commit, a file another program copied in) are neither counted nor
// deleted. For the entries it does delete, the <kind>-<id>.png naming rule is the
// ownership proof, and the file is removed before its row so no row ever points
// at a file that is still being served.
func (c *Cache) evict(ctx context.Context) error {
	c.evictMu.Lock()
	defer c.evictMu.Unlock()
	// Pending touches describe real uses; without them the row just used could
	// look like the oldest one.
	if err := c.flushUsage(ctx); err != nil && !errors.Is(err, database.ErrClosed) && !errors.Is(err, database.ErrCorrupt) {
		return err
	}
	return c.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var count, total int64
		if err := tx.QueryRowContext(ctx, countRowsSQL, assetKind).Scan(&count, &total); err != nil {
			return err
		}
		if count <= int64(c.maxEntries) && total <= c.maxBytes {
			return nil
		}
		rows, err := tx.QueryContext(ctx, oldestRowsSQL, assetKind)
		if err != nil {
			return err
		}
		type victim struct {
			name string
			size int64
		}
		victims := make([]victim, 0, count)
		for rows.Next() {
			var v victim
			if err := rows.Scan(&v.name, &v.size); err != nil {
				rows.Close()
				return err
			}
			victims = append(victims, v)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		for _, v := range victims {
			if count <= int64(c.maxEntries) && total <= c.maxBytes {
				break
			}
			if validFilename(v.name) {
				if err := c.removeIcon(ctx, v.name); err != nil && !errors.Is(err, os.ErrNotExist) {
					// A file the path guard refuses to delete keeps its row, so a
					// later eviction can retry instead of leaking the entry.
					continue
				}
			}
			if _, err := tx.ExecContext(ctx, deleteRowSQL, assetKind, v.name); err != nil {
				return err
			}
			if v.size > 0 && v.size <= total {
				total -= v.size
			}
			count--
		}
		return nil
	})
}
