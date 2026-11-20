// SPDX-License-Identifier: GPL-3.0-or-later

// Package gamemedia caches public game-record PNG icons. Use one Cache per
// directory in the launcher process; it does not provide a cross-process lock.
// The index of cached files is asset_cache in the launcher database, so a second
// Cache over the same directory and database sees the same entries.
package gamemedia

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nerv-base/Oddward/internal/database"
)

const defaultBaseURL = "https://webstatic.tajiduo.com/bbs/yh-game-records-web-source"
const maxIconBytes = 1 << 20

// defaultMaxEntries bounds the number of indexed icons; the byte budget in
// Options bounds their size. Unindexed files on disk are outside both budgets.
const defaultMaxEntries = 4096

var avatarID = regexp.MustCompile(`^[0-9]{1,10}$`)
var forkID = regexp.MustCompile(`^fork_[a-z0-9_]{1,48}$`)

// Key is a validated icon identity. Kind is "avatar" or "fork".
type Key struct {
	Kind string
	ID   string
}

// ErrUnknownItem means an item does not have a supported CDN ID shape.
var ErrUnknownItem = errors.New("gamemedia: unknown item")

// ErrNotFound means the CDN returned 404, including a cached 404.
var ErrNotFound = errors.New("gamemedia: icon not found")

// KeyFor classifies an ID without making a request or accepting URL syntax.
func KeyFor(itemID string) (Key, error) {
	switch {
	case avatarID.MatchString(itemID):
		return Key{Kind: "avatar", ID: itemID}, nil
	case forkID.MatchString(itemID):
		return Key{Kind: "fork", ID: itemID}, nil
	}
	return Key{}, fmt.Errorf("%w: unsupported item ID", ErrUnknownItem)
}
func (k Key) filename() string { return k.Kind + "-" + k.ID + ".png" }
func (k Key) remotePath() string {
	if k.Kind == "avatar" {
		return "/avatar/square/" + k.ID + ".PNG"
	}
	return "/character/fork/" + k.ID + ".png"
}

// Options selects transport, origin, allowlist, clock and total disk byte budget.
// HTTP is copied before use; redirects are always checked by this package.
type Options struct {
	HTTP     *http.Client
	BaseURL  string
	Hosts    []string
	Now      func() time.Time
	MaxBytes int64
}

// Cache owns its directory's icon files and their asset_cache rows. Path guards
// are conservative snapshots, not a defense against hostile concurrent path
// swaps.
type Cache struct {
	dir        string
	base       string
	hosts      []string
	http       *http.Client
	now        func() time.Time
	maxBytes   int64
	maxEntries int
	db         *database.DB

	dirMu    sync.Mutex
	dirReady bool

	evictMu sync.Mutex

	flightsMu sync.Mutex
	flights   map[string]*flight

	// The hit path records uses here and flushUsage writes them as one batch.
	// usageBatches and usageUpdates count committed writes; tests use them to
	// prove that a screenful of hits is batched and never rewritten wholesale.
	usageMu      sync.Mutex
	usage        map[Key]time.Time
	usageTimer   *time.Timer
	usageBatches atomic.Int64
	usageUpdates atomic.Int64

	// Expired entries are revalidated in the background. One flight per file and
	// a bounded retry keep a dead source from being asked once per frame.
	revalidateMu    sync.Mutex
	revalidating    map[string]bool
	revalidateAfter map[string]time.Time
}

type flight struct {
	done chan struct{}
	data []byte
	err  error
}

// New selects an absolute local Windows cache directory and its index database.
// It creates no files; directory checks also run on the first Get.
func New(db *database.DB, dir string, o Options) (*Cache, error) {
	if db == nil {
		return nil, errors.New("gamemedia: nil database")
	}
	if err := validPathSyntax(dir); err != nil {
		return nil, err
	}
	dir = filepath.Clean(filepath.FromSlash(dir))
	if filepath.Dir(dir) == dir {
		return nil, errors.New("gamemedia: cache cannot be a volume root")
	}
	if o.BaseURL == "" {
		o.BaseURL = defaultBaseURL
	}
	if o.Hosts == nil {
		o.Hosts = []string{"webstatic.tajiduo.com"}
	}
	hosts := append([]string(nil), o.Hosts...)
	if err := allowedURL(o.BaseURL, hosts); err != nil {
		return nil, err
	}
	if strings.ContainsAny(o.BaseURL, "?#") {
		return nil, errors.New("gamemedia: base URL cannot have query or fragment")
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.MaxBytes == 0 {
		o.MaxBytes = 20 << 20
	}
	if o.MaxBytes < 0 {
		return nil, errors.New("gamemedia: negative disk budget")
	}
	client := http.Client{Timeout: 30 * time.Second}
	if o.HTTP != nil {
		client = *o.HTTP
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client.Jar = nil
	return &Cache{
		dir:             dir,
		base:            strings.TrimRight(o.BaseURL, "/"),
		hosts:           hosts,
		http:            &client,
		now:             o.Now,
		maxBytes:        o.MaxBytes,
		maxEntries:      defaultMaxEntries,
		db:              db,
		flights:         make(map[string]*flight),
		usage:           make(map[Key]time.Time),
		revalidating:    make(map[string]bool),
		revalidateAfter: make(map[string]time.Time),
	}, nil
}

// Get returns a private copy of validated PNG bytes. Concurrent calls for the
// same key share one fetch; a validated file on disk is delivered without
// waiting for the network.
func (c *Cache) Get(ctx context.Context, itemID string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key, err := KeyFor(itemID)
	if err != nil {
		return nil, err
	}
	if err := c.ensureDir(ctx); err != nil {
		return nil, err
	}
	data, handled, err := c.serve(ctx, key)
	if handled {
		return data, err
	}
	if err != nil {
		return nil, err
	}
	return c.fetch(ctx, key)
}

// serve answers from disk when it can. handled reports that no request is
// needed: either valid bytes are returned or err is ErrNotFound from the
// negative cache.
func (c *Cache) serve(ctx context.Context, key Key) (data []byte, handled bool, err error) {
	now := c.now().UTC()
	row, found := c.lookup(ctx, key)
	if found && row.missing && now.Before(row.expiresAt) {
		c.touch(key, now)
		return nil, true, ErrNotFound
	}
	name := key.filename()
	data, err = c.readIcon(ctx, name)
	switch {
	case errors.Is(err, os.ErrNotExist):
		// The row outlived its file. Drop the row; the caller refetches and a
		// missing file never becomes a caller-visible failure.
		if found {
			_ = c.deleteRow(ctx, key)
		}
		return nil, false, nil
	case errors.Is(err, errInvalidIcon):
		// The bytes cannot be trusted. Forget both the file and its row; a failed
		// cleanup must not turn a refetch into an error.
		_ = c.removeIcon(ctx, name)
		if found {
			_ = c.deleteRow(ctx, key)
		}
		return nil, false, nil
	case err != nil:
		return nil, false, err
	}
	switch {
	case found && !row.missing && now.Before(row.expiresAt):
		c.touch(key, now)
		return data, true, nil
	case found && !row.missing:
		// Expired but still verified: deliver now and confirm freshness in the
		// background. Delivery never waits for the network.
		c.touch(key, now)
		c.revalidate(key, row)
		return data, true, nil
	}
	// A valid file without a row (an interrupted commit, a lost or replaced
	// database) is adopted instead of deleted. It starts with the default
	// freshness and no validators, so it revalidates like any other entry.
	adopted := cachedIcon{size: int64(len(data)), fetchedAt: now, expiresAt: now.Add(freshness(nil)), lastUsed: now}
	// Delivery must not depend on the index; the next use adopts it again.
	_ = c.store(ctx, key, adopted)
	return data, true, nil
}

// fetch joins or starts the single flight for one key. The flight runs on its
// own 30-second context so one caller's cancellation does not cancel the shared
// work; each caller still observes its own cancellation.
func (c *Cache) fetch(ctx context.Context, key Key) ([]byte, error) {
	name := key.filename()
	c.flightsMu.Lock()
	f, ok := c.flights[name]
	if !ok {
		f = &flight{done: make(chan struct{})}
		c.flights[name] = f
		go func() {
			shared, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			f.data, f.err = c.fetchOnce(shared, key)
			c.flightsMu.Lock()
			delete(c.flights, name)
			close(f.done)
			c.flightsMu.Unlock()
		}()
	}
	c.flightsMu.Unlock()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-f.done:
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return append([]byte(nil), f.data...), f.err
}

func (c *Cache) fetchOnce(ctx context.Context, key Key) ([]byte, error) {
	now := c.now().UTC()
	previous, found := c.lookup(ctx, key)
	var old []byte
	hasOld := false
	if found && !previous.missing {
		data, err := c.readIcon(ctx, key.filename())
		switch {
		case err == nil:
			old, hasOld = data, true
		case errors.Is(err, os.ErrNotExist):
		case errors.Is(err, errInvalidIcon):
			_ = c.removeIcon(ctx, key.filename())
		default:
			return nil, err
		}
	}
	result, networkError, err := c.download(ctx, key, previous, hasOld)
	if err != nil {
		if networkError && hasOld {
			// The bytes on disk are still valid, only freshness is unknown. Serve
			// them and leave the row alone so a later attempt retries.
			c.touch(key, now)
			return old, nil
		}
		return nil, err
	}
	return c.commit(ctx, key, previous, result, old, hasOld)
}

// commit applies one download result to the file and the index. The bytes are
// returned even when the index write fails: a database problem degrades the
// cache, never the page.
func (c *Cache) commit(ctx context.Context, key Key, previous cachedIcon, result downloaded, old []byte, hasOld bool) ([]byte, error) {
	now := c.now().UTC()
	next := cachedIcon{
		etag:         validValidator(result.header.Get("ETag")),
		lastModified: validValidator(result.header.Get("Last-Modified")),
		fetchedAt:    now,
		expiresAt:    now.Add(freshness(result.header)),
		lastUsed:     now,
	}
	var data []byte
	switch result.status {
	case http.StatusNotFound:
		next.missing, next.size, next.expiresAt = true, 0, now.Add(24*time.Hour)
		if err := c.removeIcon(ctx, key.filename()); err != nil {
			return nil, err
		}
	case http.StatusNotModified:
		if !hasOld {
			return nil, errors.New("gamemedia: 304 without a cached image")
		}
		data = old
		next.size = int64(len(old))
		if next.etag == "" {
			next.etag = previous.etag
		}
		if next.lastModified == "" {
			next.lastModified = previous.lastModified
		}
	default:
		data = result.data
		next.size = int64(len(data))
		if err := c.atomicWrite(ctx, key.filename(), data); err != nil {
			return nil, err
		}
	}
	if err := c.store(ctx, key, next); err != nil {
		// The bytes are valid and already on disk; the next use adopts the file.
		if result.status == http.StatusNotFound {
			return nil, ErrNotFound
		}
		return data, nil
	}
	_ = c.evict(ctx)
	if result.status == http.StatusNotFound {
		return nil, ErrNotFound
	}
	return data, nil
}

// revalidate confirms an expired entry in the background. One flight per file
// and a five-minute backoff after a failure keep a dead source off the frame
// budget; the stale bytes stay available meanwhile.
func (c *Cache) revalidate(key Key, previous cachedIcon) {
	name := key.filename()
	now := c.now().UTC()
	c.revalidateMu.Lock()
	if c.revalidating[name] {
		c.revalidateMu.Unlock()
		return
	}
	if until, ok := c.revalidateAfter[name]; ok && now.Before(until) {
		c.revalidateMu.Unlock()
		return
	}
	c.revalidating[name] = true
	c.revalidateMu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		err := c.refresh(ctx, key, previous)
		c.revalidateMu.Lock()
		delete(c.revalidating, name)
		if err != nil {
			c.revalidateAfter[name] = c.now().UTC().Add(revalidateBackoff)
		} else {
			delete(c.revalidateAfter, name)
		}
		c.revalidateMu.Unlock()
	}()
}

func (c *Cache) refresh(ctx context.Context, key Key, previous cachedIcon) error {
	old, err := c.readIcon(ctx, key.filename())
	if err != nil {
		return err
	}
	result, _, err := c.download(ctx, key, previous, true)
	if err != nil {
		return err
	}
	_, err = c.commit(ctx, key, previous, result, old, true)
	return err
}
