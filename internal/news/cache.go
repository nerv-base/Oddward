// SPDX-License-Identifier: GPL-3.0-or-later

package news

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
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
)

const maxCachedFeed = 512 << 10

var errInvalidNewsCache = errors.New("news: invalid cache record")

// CachedClient adds a best-effort on-disk cache to the official news reader.
// An empty directory disables persistence. Cache failures leave live reads usable.
type CachedClient struct {
	*Client
	dir string
	mu  sync.Mutex
}

// NewCachedClient uses dir as the app-owned absolute cache directory.
func NewCachedClient(client *Client, dir string) *CachedClient {
	return &CachedClient{Client: client, dir: dir}
}

// Feed refreshes the official board. Errors and fully empty responses are
// returned to the caller without replacing the last usable snapshot.
func (c *CachedClient) Feed(ctx context.Context) (Feed, error) {
	feed, err := c.Client.Feed(ctx)
	if err != nil || len(feed.Items)+len(feed.Banners) == 0 || c.dir == "" {
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
	if c.validFeed(feed) {
		if data, err := json.Marshal(feed); err == nil && len(data) <= maxCachedFeed {
			_ = c.writeCache(ctx, "feed.json", data)
		}
	}
	return feed, nil
}

// CachedFeed reads the local snapshot without contacting either upstream.
func (c *CachedClient) CachedFeed(ctx context.Context) (Feed, error) {
	data, err := c.readCache(ctx, "feed.json", maxCachedFeed)
	if err != nil {
		return Feed{}, err
	}
	var feed Feed
	if !utf8.Valid(data) || json.Unmarshal(data, &feed) != nil || !c.validFeed(feed) {
		return Feed{}, errInvalidNewsCache
	}
	return feed, ctx.Err()
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
		if c.dir != "" && validCachedDetail(detail, rawURL) {
			if data, err := json.Marshal(detail); err == nil {
				c.storeEntry(ctx, articleCache, rawURL, data)
			}
		}
		return detail, nil
	}
	data := c.loadEntry(ctx, articleCache, rawURL)
	if err := ctx.Err(); err != nil {
		return Detail{}, err
	}
	if utf8.Valid(data) && json.Unmarshal(data, &detail) == nil && validCachedDetail(detail, rawURL) {
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
	if data := c.loadEntry(ctx, bannerImageCache, rawURL); data != nil && validateCachedImage(data) == nil {
		return data, ctx.Err()
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
	c.storeEntry(ctx, bannerImageCache, rawURL, data)
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

type cacheEntries struct {
	prefix  string
	buckets uint32
	limit   int64
}

var (
	articleCache     = cacheEntries{prefix: "article", buckets: 12, limit: 1 << 20}
	bannerImageCache = cacheEntries{prefix: "image", buckets: 8, limit: maxImage}
)

func (c *CachedClient) loadEntry(ctx context.Context, set cacheEntries, rawURL string) []byte {
	file, _ := c.findEntry(ctx, set, sha256.Sum256([]byte(rawURL)))
	if file == nil {
		return nil
	}
	defer file.Close()
	data, err := readBounded(file, set.limit)
	if err != nil {
		return nil
	}
	return data
}

func (c *CachedClient) storeEntry(ctx context.Context, set cacheEntries, rawURL string, data []byte) {
	if int64(len(data)) > set.limit {
		return
	}
	key := sha256.Sum256([]byte(rawURL))
	c.mu.Lock()
	defer c.mu.Unlock()
	file, name := c.findEntry(ctx, set, key)
	if file != nil {
		file.Close()
	}
	if name != "" {
		_ = c.writeCache(ctx, name, append(key[:], data...))
	}
}

// Each URL belongs to a fixed bucket. A bucket holds at most four entries and
// replaces its least recently written entry when full. Fixed names bound disk
// use without scanning attacker-supplied directory entries or pruning by path.
func (c *CachedClient) findEntry(ctx context.Context, set cacheEntries, key [sha256.Size]byte) (*os.File, string) {
	if ctx.Err() != nil {
		return nil, ""
	}
	dir, dirErr := c.cacheDir(false)
	if dirErr != nil && !errors.Is(dirErr, os.ErrNotExist) {
		return nil, ""
	}
	bucket := binary.LittleEndian.Uint32(key[:4]) % set.buckets
	var free, oldest string
	var oldestTime time.Time
	for slot := range slotsPerBucket {
		name := fmt.Sprintf("%s-%02d-%d", set.prefix, bucket, slot)
		if dirErr != nil {
			return nil, name
		}
		file, modified, err := openCacheFile(filepath.Join(dir, name), set.limit+sha256.Size)
		if err != nil {
			if free == "" {
				free = name
			}
			continue
		}
		var storedKey [sha256.Size]byte
		_, err = io.ReadFull(file, storedKey[:])
		if err == nil && bytes.Equal(storedKey[:], key[:]) {
			return file, name
		}
		file.Close()
		if oldest == "" || modified.Before(oldestTime) {
			oldest, oldestTime = name, modified
		}
	}
	if free != "" {
		return nil, free
	}
	return nil, oldest
}

func (c *CachedClient) writeCache(ctx context.Context, name string, data []byte) error {
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
	defer stage.Close()
	if _, err := stage.Write(data); err != nil {
		return err
	}
	if err := stage.Sync(); err != nil {
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
