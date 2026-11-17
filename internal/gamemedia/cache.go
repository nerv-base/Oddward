// SPDX-License-Identifier: GPL-3.0-or-later

// Package gamemedia caches public game-record PNG icons. Use one Cache per
// directory in the launcher process; it does not provide a cross-process lock.
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
	"time"
)

const defaultBaseURL = "https://webstatic.tajiduo.com/bbs/yh-game-records-web-source"
const maxIconBytes = 1 << 20

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

// Cache owns its directory's index and known icon files. Path guards are
// conservative snapshots, not a defense against hostile concurrent path swaps.
type Cache struct {
	dir        string
	base       string
	hosts      []string
	http       *http.Client
	now        func() time.Time
	maxBytes   int64
	operations chan struct{}
	loaded     bool
	entries    map[string]entry
	flightsMu  sync.Mutex
	flights    map[string]*flight
}
type flight struct {
	done chan struct{}
	data []byte
	err  error
}

// New selects an absolute local Windows cache directory. It creates no files;
// directory checks and bounded index loading also run on the first Get.
func New(dir string, o Options) (*Cache, error) {
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
	c := &Cache{dir: dir, base: strings.TrimRight(o.BaseURL, "/"), hosts: hosts, http: &client, now: o.Now, maxBytes: o.MaxBytes, operations: make(chan struct{}, 1), entries: make(map[string]entry), flights: make(map[string]*flight)}
	c.operations <- struct{}{}
	return c, nil
}
func (c *Cache) acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.operations:
	}
	if err := ctx.Err(); err != nil {
		c.operations <- struct{}{}
		return nil, err
	}
	return func() { c.operations <- struct{}{} }, nil
}

// Get returns a private copy of validated PNG bytes. Calls for the same key
// share one 30-second operation independent of any caller's cancellation.
func (c *Cache) Get(ctx context.Context, itemID string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	key, err := KeyFor(itemID)
	if err != nil {
		return nil, err
	}
	name := key.filename()
	c.flightsMu.Lock()
	f, ok := c.flights[name]
	if !ok {
		f = &flight{done: make(chan struct{})}
		c.flights[name] = f
		go func() {
			shared, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			f.data, f.err = c.get(shared, key)
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

func (c *Cache) get(ctx context.Context, key Key) ([]byte, error) {
	release, err := c.acquire(ctx)
	if err != nil {
		return nil, err
	}
	if err = c.load(ctx); err != nil {
		release()
		return nil, err
	}
	name := key.filename()
	previous, exists := c.entries[name]
	now := c.now().UTC()
	if exists && previous.Size == 0 && now.Before(previous.ExpiresAt) {
		previous.LastUsed = now
		c.entries[name] = previous
		err = c.saveIndex(ctx)
		release()
		if err != nil {
			return nil, err
		}
		return nil, ErrNotFound
	}
	var old []byte
	if exists && previous.Size > 0 {
		old, err = c.readIcon(ctx, name)
		if err != nil {
			if !errors.Is(err, errInvalidIcon) && !errors.Is(err, os.ErrNotExist) {
				release()
				return nil, err
			}
			if errors.Is(err, errInvalidIcon) {
				if err = c.removeIcon(ctx, name); err != nil {
					release()
					return nil, err
				}
			}
			delete(c.entries, name)
			exists = false
			previous = entry{}
		}
	}
	if len(old) > 0 && now.Before(previous.ExpiresAt) {
		previous.LastUsed = now
		previous.Size = int64(len(old))
		c.entries[name] = previous
		err = c.evict(ctx)
		if err == nil {
			err = c.saveIndex(ctx)
		}
		release()
		return old, err
	}
	release()
	result, networkError, err := c.download(ctx, key, previous, len(old) > 0)
	if err != nil {
		if networkError && len(old) > 0 {
			// The offline hit is still a use for LRU, without extending its
			// freshness. Another key may have evicted the disk entry meanwhile.
			release, lockErr := c.acquire(ctx)
			if lockErr != nil {
				return nil, lockErr
			}
			if current, present := c.entries[name]; present && current.Size > 0 {
				current.LastUsed = c.now().UTC()
				current.Size = int64(len(old))
				c.entries[name] = current
				err = c.evict(ctx)
				if err == nil {
					err = c.saveIndex(ctx)
				}
			} else {
				err = nil
			}
			release()
			return old, err
		}
		return nil, err
	}
	release, err = c.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	now = c.now().UTC()
	next := entry{Key: name, ETag: result.header.Get("ETag"), LastModified: result.header.Get("Last-Modified"), FetchedAt: now, ExpiresAt: now.Add(freshness(result.header)), LastUsed: now}
	var data []byte
	switch result.status {
	case http.StatusNotFound:
		next.ExpiresAt = now.Add(24 * time.Hour)
		if err = c.removeIcon(ctx, name); err != nil {
			return nil, err
		}
	case http.StatusNotModified:
		data = old
		next.Size = int64(len(old))
		if next.ETag == "" {
			next.ETag = previous.ETag
		}
		if next.LastModified == "" {
			next.LastModified = previous.LastModified
		}
	default:
		data = result.data
		next.Size = int64(len(data))
		if err = c.atomicWrite(ctx, name, data); err != nil {
			return nil, err
		}
	}
	if len(next.ETag) > 4096 || len(next.LastModified) > 4096 {
		return nil, errors.New("gamemedia: oversized cache validators")
	}
	c.entries[name] = next
	if err = c.evict(ctx); err != nil {
		return nil, err
	}
	if err = c.saveIndex(ctx); err != nil {
		return nil, err
	}
	if result.status == http.StatusNotFound {
		return nil, ErrNotFound
	}
	return data, nil
}
