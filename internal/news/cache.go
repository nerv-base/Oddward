// SPDX-License-Identifier: GPL-3.0-or-later

package news

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nerv-base/Oddward/internal/database"
)

// Domain is this package's domain in migration_state and compat_mirrors.
const Domain = "news"

// ImportVersion is the importer generation recorded for Domain. Bump it when the
// importer learns to read a legacy document it did not read before.
const ImportVersion = 1

const (
	// maxCachedFeed bounds the stored snapshot and therefore the feed.json mirror.
	maxCachedFeed = 512 << 10
	// feedID is the only news_feed row: the board is one document that is always
	// replaced whole.
	feedID = 1
	// feedMirror is the legacy snapshot file a rolled-back build reads.
	feedMirror = "feed.json"

	// kindArticle and kindImage are this cache's asset_cache kinds. Gacha icons
	// share the table under their own kind and are never touched here.
	kindArticle = "news-article"
	kindImage   = "news-image"

	// cachedBodyTTL is how long a stored body is served without asking upstream.
	// An expired body is still served when the refresh fails.
	cachedBodyTTL = 14 * 24 * time.Hour
	// cachedBodyMaxAge is how long an unused body keeps its bucket slot. The slot
	// count already bounds the directory; this only stops abandoned URLs from
	// holding a slot forever.
	cachedBodyMaxAge = 180 * 24 * time.Hour

	// lastUsedBatch and lastUsedInterval bound what a burst of hits writes. One
	// page view reads the whole banner row set, so stamping every hit with its own
	// statement would turn reading into writing.
	lastUsedBatch    = 16
	lastUsedInterval = time.Minute
)

var errInvalidNewsCache = errors.New("news: invalid cache record")

// statementWriter is the write surface the hit path needs. *database.DB
// implements it; a counting stand-in in the tests measures how much a burst of
// hits actually writes.
type statementWriter interface {
	Exec(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// assetID is the identity of one cached body: the digest of its source URL.
type assetID struct {
	// key is the hexadecimal digest, the asset_cache key of the body.
	key string
	// sum is the raw digest, the prefix of the bucket file that holds the body.
	sum [sha256.Size]byte
}

func assetIDFor(rawURL string) assetID {
	sum := sha256.Sum256([]byte(rawURL))
	return assetID{key: hex.EncodeToString(sum[:]), sum: sum}
}

// assetRef is one indexed row: its kind and the identity of its body.
type assetRef struct {
	kind string
	id   assetID
}

// CachedClient adds a durable index and a best-effort body cache to the official
// news reader.
//
// The news_feed row is the authoritative snapshot and the asset_cache rows name
// the bytes of every cached article and image. The bucket files keep the exact
// layout of the build this one can be rolled back to, and feed.json is rewritten
// from the row as that build's mirror. An empty directory or a database that
// failed to open disables persistence entirely, so cache trouble never stops the
// launcher from reading the board online.
type CachedClient struct {
	*Client
	db       *database.DB
	lastUsed statementWriter
	dir      string

	// mu serialises snapshot writes, bucket slot selection and the pending hit
	// timestamps they flush.
	mu      sync.Mutex
	pending map[assetRef]struct{}
	flushed time.Time
}

// NewCachedClient binds the cache to db and to dir, the app-owned absolute
// directory the previous build kept feed.json and the body buckets in.
func NewCachedClient(db *database.DB, client *Client, dir string) *CachedClient {
	c := &CachedClient{Client: client, db: db, dir: dir, pending: map[assetRef]struct{}{}}
	if db == nil {
		c.dir = ""
	} else {
		c.lastUsed = db
	}
	return c
}

// enabled reports whether this client may read or write persistent state.
func (c *CachedClient) enabled() bool {
	return c.dir != "" && c.db != nil
}

// Feed refreshes the official board. Errors and fully empty responses are
// returned to the caller without replacing the last usable snapshot.
func (c *CachedClient) Feed(ctx context.Context) (Feed, error) {
	feed, err := c.Client.Feed(ctx)
	if err != nil || len(feed.Items)+len(feed.Banners) == 0 || !c.enabled() {
		return feed, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Feed{}, err
	}
	if old, err := c.CachedFeed(ctx); err == nil {
		if len(feed.Items) == 0 {
			feed.Items, feed.Source = old.Items, old.Source
		}
		if len(feed.Banners) == 0 {
			feed.Banners = old.Banners
		}
	}
	if !c.validFeed(feed) {
		return feed, nil
	}
	data, err := json.Marshal(feed)
	if err != nil || len(data) > maxCachedFeed {
		return feed, nil
	}
	c.storeFeed(ctx, data)
	return feed, nil
}

// CachedFeed reads the local snapshot without contacting either upstream.
func (c *CachedClient) CachedFeed(ctx context.Context) (Feed, error) {
	if !c.enabled() {
		return Feed{}, os.ErrNotExist
	}
	if err := ctx.Err(); err != nil {
		return Feed{}, err
	}
	var payload string
	err := c.db.QueryRow(ctx, `SELECT payload FROM news_feed WHERE id = ?`, feedID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return Feed{}, fmt.Errorf("news: no stored snapshot: %w", os.ErrNotExist)
	}
	if err != nil {
		return Feed{}, err
	}
	var feed Feed
	data := []byte(payload)
	// The row is written only from a validated feed this build accepted, but a
	// corrupted or foreign database must not make the launcher decode an
	// unbounded document at startup.
	if len(data) == 0 || len(data) > maxCachedFeed || !utf8.Valid(data) || json.Unmarshal(data, &feed) != nil || !c.validFeed(feed) {
		return Feed{}, errInvalidNewsCache
	}
	return feed, ctx.Err()
}

// storeFeed replaces the snapshot row and records the digest of the mirror this
// build wrote. A failure leaves the previous snapshot readable: the board is live
// data and the next successful read replaces it anyway.
func (c *CachedClient) storeFeed(ctx context.Context, data []byte) {
	mirror := c.mirrorPath()
	now := time.Now()
	err := c.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO news_feed(id, payload, fetched_at) VALUES(?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				payload = excluded.payload, fetched_at = excluded.fetched_at`,
			feedID, string(data), database.Stamp(now)); err != nil {
			return fmt.Errorf("news: store the snapshot: %w", err)
		}
		return c.db.RecordMirror(ctx, tx, Domain, mirror, database.Digest(data))
	})
	if err != nil {
		return
	}
	// The row is authoritative from here on; a mirror that cannot be written only
	// costs the rolled-back build its offline copy.
	_ = database.WriteMirror(mirror, data)
}

// Detail fetches the article first and falls back to its last readable copy.
func (c *CachedClient) Detail(ctx context.Context, rawURL string) (Detail, error) {
	if err := ctx.Err(); err != nil {
		return Detail{}, err
	}
	if err := allowedBy(rawURL, c.Hosts); err != nil {
		return Detail{}, err
	}
	detail, upstreamErr := c.Client.Detail(ctx, rawURL)
	if err := ctx.Err(); err != nil {
		return Detail{}, err
	}
	if upstreamErr == nil {
		if c.enabled() && validCachedDetail(detail, rawURL) {
			if data, err := json.Marshal(detail); err == nil {
				c.storeBody(ctx, articleCache, rawURL, data)
			}
		}
		return detail, nil
	}
	// The article is always revalidated, so its expiry only decides when the row
	// may be pruned; the stored copy is used whenever the request fails.
	data, _ := c.loadBody(ctx, articleCache, rawURL)
	if err := ctx.Err(); err != nil {
		return Detail{}, err
	}
	if utf8.Valid(data) && json.Unmarshal(data, &detail) == nil && validCachedDetail(detail, rawURL) {
		c.touchHit(ctx, kindArticle, rawURL)
		return detail, nil
	}
	return Detail{}, upstreamErr
}

// Image reuses artwork for the exact URL, validating the full pixel decode
// before returning cached bytes or storing new ones.
func (c *CachedClient) Image(ctx context.Context, rawURL string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := allowedBy(rawURL, c.ImageHosts); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrImage, err)
	}
	cached, expired := c.loadBody(ctx, imageCache, rawURL)
	if validateCachedImage(cached) == nil {
		if !expired {
			c.touchHit(ctx, kindImage, rawURL)
			return cached, ctx.Err()
		}
		// An expired body must be revalidated, but it stays a better fallback than
		// an error when the refresh fails.
		data, err := c.Client.Image(ctx, rawURL)
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err == nil && validateCachedImage(data) == nil {
			c.storeBody(ctx, imageCache, rawURL, data)
			return data, nil
		}
		c.touchHit(ctx, kindImage, rawURL)
		return cached, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := c.Client.Image(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	if err := validateCachedImage(data); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	c.storeBody(ctx, imageCache, rawURL, data)
	return data, nil
}

func validateCachedImage(data []byte) error {
	if err := checkImageBytes(data); err != nil {
		return err
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		return fmt.Errorf("%w: decode pixels: %w", ErrImage, err)
	}
	return nil
}

func validCachedDetail(detail Detail, rawURL string) bool {
	if detail.URL != rawURL || strings.TrimSpace(detail.Title) == "" || len(detail.Paragraphs) == 0 {
		return false
	}
	for _, paragraph := range detail.Paragraphs {
		if strings.TrimSpace(paragraph) == "" {
			return false
		}
	}
	return true
}

func (c *CachedClient) validFeed(feed Feed) bool {
	if feed.Source != SourceLauncher && feed.Source != SourceSiteFallback || len(feed.Items)+len(feed.Banners) == 0 {
		return false
	}
	for _, item := range feed.Items {
		_, category, _, ok := officialArticleURL(item.URL, c.origin(), c.Hosts)
		if !ok || category != item.Category || item.Channel != category.Channel() ||
			allowedBy(item.URL, c.Hosts) != nil || strings.TrimSpace(item.Title) == "" {
			return false
		}
	}
	for _, banner := range feed.Banners {
		if _, ok := officialPageURL(banner.URL, c.origin(), c.Hosts); !ok || allowedBy(banner.URL, c.Hosts) != nil {
			return false
		}
		if _, ok := officialImageURL(banner.ImageURL, c.ImageHosts); !ok || strings.TrimSpace(banner.Title) == "" {
			return false
		}
	}
	return true
}

// mirrorPath is the legacy snapshot file next to the buckets.
func (c *CachedClient) mirrorPath() string {
	return filepath.Join(c.dir, feedMirror)
}

func (c *CachedClient) cacheDir(create bool) (string, error) {
	if c.dir == "" {
		return "", os.ErrNotExist
	}
	dir := filepath.Clean(c.dir)
	if !filepath.IsAbs(dir) || filepath.Dir(dir) == dir {
		return "", errInvalidNewsCache
	}
	if create {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", err
		}
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
		return "", errInvalidNewsCache
	}
	return dir, nil
}

func (c *CachedClient) readCache(ctx context.Context, name string, limit int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := c.cacheDir(false)
	if err != nil {
		return nil, err
	}
	file, _, err := openCacheFile(filepath.Join(dir, name), limit)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := readBounded(file, limit)
	if err != nil {
		return nil, err
	}
	return data, ctx.Err()
}

func openCacheFile(path string, limit int64) (*os.File, time.Time, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	if !before.Mode().IsRegular() || before.Size() > limit {
		return nil, time.Time{}, errInvalidNewsCache
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, time.Time{}, err
	}
	after, err := file.Stat()
	if err != nil {
		file.Close()
		return nil, time.Time{}, err
	}
	if !after.Mode().IsRegular() || !os.SameFile(before, after) || after.Size() > limit {
		file.Close()
		return nil, time.Time{}, errInvalidNewsCache
	}
	return file, after.ModTime(), nil
}

const slotsPerBucket = 4

// cacheEntries describes one kind of cached body: the asset_cache kind that
// indexes it, the file name prefix and bucket count that keep its directory
// bounded, and the largest body accepted for it.
type cacheEntries struct {
	kind    string
	prefix  string
	buckets uint32
	limit   int64
}

var (
	articleCache = cacheEntries{kind: kindArticle, prefix: "article", buckets: 12, limit: 1 << 20}
	imageCache   = cacheEntries{kind: kindImage, prefix: "image", buckets: 8, limit: maxImage}
)

// loadBody returns the stored bytes for rawURL and reports whether the row
// outlived cachedBodyTTL. A missing row, a row whose file is gone, a file that
// another URL already claimed, and a body that no longer matches its row are all
// misses: a file is never trusted on its own.
func (c *CachedClient) loadBody(ctx context.Context, set cacheEntries, rawURL string) ([]byte, bool) {
	if !c.enabled() || ctx.Err() != nil {
		return nil, false
	}
	id := assetIDFor(rawURL)
	var name, digest string
	var size, expiresAt int64
	err := c.db.QueryRow(ctx,
		`SELECT path, size, digest, expires_at FROM asset_cache WHERE kind = ? AND key = ?`,
		set.kind, id.key).Scan(&name, &size, &digest, &expiresAt)
	if err != nil {
		return nil, false
	}
	data, err := c.readBody(ctx, set, name, id)
	if err != nil || int64(len(data)) != size || database.Digest(data) != digest {
		return nil, false
	}
	return data, expiresAt <= time.Now().UnixNano()
}

// readBody reads one bucket file and checks that it still holds the URL it was
// stored for, so a slot another URL overwrote can never be served as this one.
func (c *CachedClient) readBody(ctx context.Context, set cacheEntries, name string, id assetID) ([]byte, error) {
	if filepath.Base(name) != name || !strings.HasPrefix(name, set.prefix+"-") {
		return nil, errInvalidNewsCache
	}
	dir, err := c.cacheDir(false)
	if err != nil {
		return nil, err
	}
	file, _, err := openCacheFile(filepath.Join(dir, name), set.limit+sha256.Size)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	var stored [sha256.Size]byte
	if _, err := io.ReadFull(file, stored[:]); err != nil {
		return nil, errInvalidNewsCache
	}
	if stored != id.sum {
		return nil, errInvalidNewsCache
	}
	data, err := readBounded(file, set.limit)
	if err != nil {
		return nil, err
	}
	return data, ctx.Err()
}

// storeBody writes one body into its bucket and indexes it. The file is written
// before the row, so a committed row always names bytes that are on disk; when
// the commit fails the slot holds bytes no row resolves and the next store
// overwrites them.
func (c *CachedClient) storeBody(ctx context.Context, set cacheEntries, rawURL string, data []byte) {
	if !c.enabled() || int64(len(data)) > set.limit || ctx.Err() != nil {
		return
	}
	id := assetIDFor(rawURL)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.flushHitsLocked(ctx)
	name, evict, err := c.slotFor(ctx, set, id)
	if err != nil || name == "" {
		return
	}
	if err := c.writeBody(ctx, name, id, data); err != nil {
		return
	}
	now := time.Now()
	var pruned []string
	if err := c.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if evict.id.key != "" {
			if _, err := tx.ExecContext(ctx,
				`DELETE FROM asset_cache WHERE kind = ? AND key = ?`, evict.kind, evict.id.key); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO asset_cache(
				kind, key, path, size, digest, source_url, etag, last_modified,
				fetched_at, expires_at, last_used, status, detail)
			VALUES(?, ?, ?, ?, ?, ?, '', '', ?, ?, ?, 'ok', '')
			ON CONFLICT(kind, key) DO UPDATE SET
				path = excluded.path, size = excluded.size, digest = excluded.digest,
				source_url = excluded.source_url, fetched_at = excluded.fetched_at,
				expires_at = excluded.expires_at, last_used = excluded.last_used,
				status = excluded.status`,
			set.kind, id.key, name, len(data), database.Digest(data), rawURL,
			database.Stamp(now), database.Stamp(now.Add(cachedBodyTTL)), database.Stamp(now)); err != nil {
			return fmt.Errorf("news: index the cached body: %w", err)
		}
		if evict.id.key == "" {
			return nil
		}
		// Taking a slot from another URL is the moment to drop rows nothing has
		// used for cachedBodyMaxAge; their files are the only ones removed.
		pruned, err = c.pruneBodies(ctx, tx)
		return err
	}); err != nil {
		return
	}
	for _, path := range pruned {
		if path != name {
			_ = os.Remove(filepath.Join(c.dir, path))
		}
	}
}

// slotFor picks the bucket slot for id: the slot that already holds it, else a
// slot no row claims, else the least recently used slot of that bucket. Fixed
// names and a fixed slot count bound disk use without scanning a directory a
// caller could have filled with entries.
func (c *CachedClient) slotFor(ctx context.Context, set cacheEntries, id assetID) (string, assetRef, error) {
	bucket := binary.LittleEndian.Uint32(id.sum[:4]) % set.buckets
	names := make([]string, 0, slotsPerBucket)
	for slot := range slotsPerBucket {
		names = append(names, fmt.Sprintf("%s-%02d-%d", set.prefix, bucket, slot))
	}
	rows, err := c.db.Query(ctx, `
		SELECT key, path, last_used FROM asset_cache
		WHERE kind = ? AND path IN (?, ?, ?, ?)`,
		set.kind, names[0], names[1], names[2], names[3])
	if err != nil {
		return "", assetRef{}, err
	}
	defer rows.Close()
	taken := map[string]struct{}{}
	var oldest assetRef
	var oldestPath string
	var oldestAt time.Time
	for rows.Next() {
		var storedKey, path string
		var lastUsed sql.NullInt64
		if err := rows.Scan(&storedKey, &path, &lastUsed); err != nil {
			return "", assetRef{}, err
		}
		if storedKey == id.key {
			return path, assetRef{}, nil
		}
		taken[path] = struct{}{}
		if used := database.StampTime(lastUsed); oldestPath == "" || used.Before(oldestAt) {
			oldest, oldestPath, oldestAt = assetRef{kind: set.kind, id: assetID{key: storedKey}}, path, used
		}
	}
	if err := rows.Err(); err != nil {
		return "", assetRef{}, err
	}
	for _, name := range names {
		if _, used := taken[name]; !used {
			return name, assetRef{}, nil
		}
	}
	return oldestPath, oldest, nil
}

// pruneBodies deletes rows that nothing has used for cachedBodyMaxAge and returns
// the files they named. Only files recorded in this cache's own rows are removed:
// a bucket file without a row may be a body the importer is about to adopt, so it
// is left where it is.
func (c *CachedClient) pruneBodies(ctx context.Context, tx *sql.Tx) ([]string, error) {
	cutoff := database.Stamp(time.Now().Add(-cachedBodyMaxAge))
	rows, err := tx.QueryContext(ctx, `
		SELECT path FROM asset_cache WHERE kind IN (?, ?) AND last_used < ?`,
		kindArticle, kindImage, cutoff)
	if err != nil {
		return nil, err
	}
	var pruned []string
	for rows.Next() {
		var path string
		if err := rows.Scan(&path); err != nil {
			rows.Close()
			return nil, err
		}
		pruned = append(pruned, path)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(pruned) == 0 {
		return nil, nil
	}
	if _, err := tx.ExecContext(ctx, `
		DELETE FROM asset_cache WHERE kind IN (?, ?) AND last_used < ?`,
		kindArticle, kindImage, cutoff); err != nil {
		return nil, err
	}
	return pruned, nil
}

// touchHit records that one body was served. Hits are batched because a page of
// banners reads a whole row set at once: the index keeps its freshness while the
// write volume stays per batch rather than per hit.
func (c *CachedClient) touchHit(ctx context.Context, kind, rawURL string) {
	if !c.enabled() {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pending[assetRef{kind: kind, id: assetIDFor(rawURL)}] = struct{}{}
	if len(c.pending) >= lastUsedBatch || time.Since(c.flushed) >= lastUsedInterval {
		c.flushHitsLocked(ctx)
	}
}

// flushHitsLocked writes the pending hit timestamps as one statement per kind. A
// failed write keeps the batch for the next attempt, but the batch is dropped
// once it grows past a few rounds so a database that keeps refusing the write
// cannot grow it without bound.
func (c *CachedClient) flushHitsLocked(ctx context.Context) {
	if len(c.pending) == 0 {
		return
	}
	// Only a real batch resets the window; an empty flush would postpone the next
	// hit's timestamp without writing anything.
	defer func() { c.flushed = time.Now() }()
	byKind := map[string][]string{}
	for ref := range c.pending {
		byKind[ref.kind] = append(byKind[ref.kind], ref.id.key)
	}
	now := database.Stamp(time.Now())
	failed := false
	for kind, keys := range byKind {
		query, args := lastUsedStatement(kind, keys, now)
		if _, err := c.lastUsed.Exec(ctx, query, args...); err != nil {
			failed = true
		}
	}
	if !failed || len(c.pending) > lastUsedBatch*4 {
		c.pending = map[assetRef]struct{}{}
	}
}

// lastUsedStatement builds the single UPDATE that stamps every key of one kind.
func lastUsedStatement(kind string, keys []string, stamp any) (string, []any) {
	args := make([]any, 0, len(keys)+2)
	args = append(args, stamp, kind)
	placeholders := make([]string, 0, len(keys))
	for _, key := range keys {
		args = append(args, key)
		placeholders = append(placeholders, "?")
	}
	return `UPDATE asset_cache SET last_used = ? WHERE kind = ? AND key IN (` +
		strings.Join(placeholders, ", ") + `)`, args
}

// writeBody replaces the bucket file with the body prefixed by its URL digest.
// The prefix is what lets a rolled-back build and this one recognise which URL a
// slot holds, which is why the file format is unchanged.
func (c *CachedClient) writeBody(ctx context.Context, name string, id assetID, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := c.cacheDir(true)
	if err != nil {
		return err
	}
	stage, err := os.CreateTemp(dir, ".news-stage-*")
	if err != nil {
		return err
	}
	defer os.Remove(stage.Name())
	body := make([]byte, 0, sha256.Size+len(data))
	body = append(append(body, id.sum[:]...), data...)
	if _, err := stage.Write(body); err != nil {
		stage.Close()
		return err
	}
	if err := stage.Sync(); err != nil {
		stage.Close()
		return err
	}
	if err := stage.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return replaceCachedNewsFile(stage.Name(), filepath.Join(dir, name))
}
