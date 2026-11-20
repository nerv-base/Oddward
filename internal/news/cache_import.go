// SPDX-License-Identifier: GPL-3.0-or-later

package news

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/nerv-base/Oddward/internal/database"
)

// legacyBody is one body the previous build left in the bucket directory: the
// metadata the index needs plus the row values its file implies.
type legacyBody struct {
	kind     string
	key      string
	name     string
	source   string
	digest   string
	size     int64
	modified time.Time
}

// ImportLegacy reconciles the database with the cache the previous build left
// behind, and is the database.ImportFunc for Domain.
//
// It adopts feed.json as the snapshot, adopts every bucket body the old build
// still holds (recovering an article's URL from its own payload), and records the
// digest of the feed bytes it read so a later start can tell whether the mirror
// changed under it. Sources that are absent are a no-op; a corrupt feed names its
// file and leaves both the files and the rows untouched.
func (c *CachedClient) ImportLegacy(ctx context.Context) (source, detail string, err error) {
	// Without a database or a directory this build keeps nothing, so there is
	// nothing to adopt and nothing to reconcile. Recording the pass stops startup
	// from asking again; a build that caches later fills the same tables itself.
	if !c.enabled() {
		return c.dir, "caching is disabled", nil
	}
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, err := c.cacheDir(false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return c.dir, "no legacy cache directory", nil
		}
		return c.dir, "", err
	}
	feed, err := c.legacyFeed(ctx)
	if err != nil {
		return c.mirrorPath(), "", err
	}
	bodies, skipped := c.legacyBodies(ctx)
	if feed == nil && len(bodies) == 0 {
		return c.mirrorPath(), "no legacy feed snapshot and no bucket bodies", nil
	}
	feedBytes := 0
	if feed != nil {
		feedBytes = len(feed.payload)
	}
	detail = fmt.Sprintf("feed %d bytes, %d bodies adopted, %d files skipped", feedBytes, len(bodies), skipped)
	err = c.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if len(bodies) > 0 {
			// The bucket directory is the whole source of truth for bodies, so the
			// adopted set replaces this cache's rows rather than merging with them.
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM asset_cache WHERE kind IN (?, ?)`, kindArticle, kindImage); err != nil {
				return fmt.Errorf("news: clear the body index: %w", err)
			}
			for _, body := range bodies {
				if _, err := tx.ExecContext(ctx, `
					INSERT INTO asset_cache(
						kind, key, path, size, digest, source_url, etag, last_modified,
						fetched_at, expires_at, last_used, status, detail)
					VALUES(?, ?, ?, ?, ?, ?, '', '', ?, ?, ?, 'ok', '')`,
					body.kind, body.key, body.name, body.size, body.digest, body.source,
					database.Stamp(body.modified), database.Stamp(body.modified),
					database.Stamp(body.modified)); err != nil {
					return fmt.Errorf("news: adopt %s: %w", body.name, err)
				}
			}
		}
		if feed == nil {
			return nil
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO news_feed(id, payload, fetched_at) VALUES(?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				payload = excluded.payload, fetched_at = excluded.fetched_at`,
			feedID, string(feed.payload), database.Stamp(feed.fetchedAt)); err != nil {
			return fmt.Errorf("news: adopt the feed snapshot: %w", err)
		}
		return c.db.RecordMirror(ctx, tx, Domain, c.mirrorPath(), database.Digest(feed.payload))
	})
	if err != nil {
		return c.mirrorPath(), "", err
	}
	return c.mirrorPath(), detail, nil
}

// legacyFeed is the snapshot feed.json holds, already validated against the same
// rules a freshly fetched board has to pass.
type legacyFeed struct {
	payload   []byte
	fetchedAt time.Time
}

// legacyFeed reads and validates feed.json. A missing file is not an error: the
// snapshot row is left alone, because an older build that never wrote the file
// should not erase what a newer one stored.
func (c *CachedClient) legacyFeed(ctx context.Context) (*legacyFeed, error) {
	data, err := c.readCache(ctx, feedMirror, maxCachedFeed)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", c.mirrorPath(), err)
	}
	var feed Feed
	if json.Unmarshal(data, &feed) != nil || !c.validFeed(feed) {
		return nil, fmt.Errorf("%s: %w", c.mirrorPath(), errInvalidNewsCache)
	}
	return &legacyFeed{payload: data, fetchedAt: time.Now()}, nil
}

// legacyBodies reads every bucket file the previous build wrote. A file that no
// longer decodes is skipped rather than fatal: the caller gets the readable set
// and a count of the rest, so one bad banner cannot block startup. Orphan and
// overwritten slots are ignored and never deleted.
func (c *CachedClient) legacyBodies(ctx context.Context) ([]legacyBody, int) {
	var bodies []legacyBody
	seen := map[string]struct{}{}
	skipped := 0
	for _, set := range []cacheEntries{articleCache, imageCache} {
		for bucket := range set.buckets {
			for slot := range slotsPerBucket {
				if ctx.Err() != nil {
					return bodies, skipped
				}
				name := fmt.Sprintf("%s-%02d-%d", set.prefix, bucket, slot)
				if _, err := os.Lstat(filepath.Join(c.dir, name)); err != nil {
					continue
				}
				body, ok := c.legacyBody(ctx, set, name)
				if !ok {
					skipped++
					continue
				}
				if _, dup := seen[body.kind+body.key]; dup {
					skipped++
					continue
				}
				seen[body.kind+body.key] = struct{}{}
				bodies = append(bodies, body)
			}
		}
	}
	return bodies, skipped
}

// legacyBody reads one slot. The URL digest in the file header names the URL of
// an image only indirectly, so an image body is adopted without a source URL and
// an article is adopted only when its payload still names an allow-listed URL
// that hashes to the header.
func (c *CachedClient) legacyBody(ctx context.Context, set cacheEntries, name string) (legacyBody, bool) {
	file, modified, err := openCacheFile(filepath.Join(c.dir, name), set.limit+sha256.Size)
	if err != nil {
		return legacyBody{}, false
	}
	defer file.Close()
	var sum [sha256.Size]byte
	if _, err := io.ReadFull(file, sum[:]); err != nil {
		return legacyBody{}, false
	}
	data, err := readBounded(file, set.limit)
	if err != nil || len(data) == 0 || ctx.Err() != nil {
		return legacyBody{}, false
	}
	body := legacyBody{
		kind:     set.kind,
		key:      hex.EncodeToString(sum[:]),
		name:     name,
		digest:   database.Digest(data),
		size:     int64(len(data)),
		modified: modified,
	}
	if set.kind == kindImage {
		if validateCachedImage(data) != nil {
			return legacyBody{}, false
		}
		return body, true
	}
	var detail Detail
	if json.Unmarshal(data, &detail) != nil || !validCachedDetail(detail, detail.URL) {
		return legacyBody{}, false
	}
	if assetIDFor(detail.URL).key != body.key || allowedBy(detail.URL, c.Hosts) != nil {
		return legacyBody{}, false
	}
	body.source = detail.URL
	return body, true
}
