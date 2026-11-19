// SPDX-License-Identifier: GPL-3.0-or-later

// Package bwiki reads the public 异环 BWIKI arc list and keeps a small local
// cache of the fields Oddward displays: name, rarity and icon. It never sends
// credentials and never reads account data.
package bwiki

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/nerv-base/Oddward/internal/pinyin"
)

const (
	defaultAPI      = "https://wiki.biligame.com/yh/api.php"
	defaultIconHost = "patchwiki.biligame.com"
	cacheFile       = "arcs.json"
	cacheFormat     = "oddward-bwiki-cache"
	cacheVersion    = 1
	cacheTTL        = 7 * 24 * time.Hour
	maxCatalogSize  = 512 << 10
	maxIconSize     = 1 << 20
	maxItems        = 256
	maxName         = 64
	maxRarity       = 8
	maxKey          = 64
)

// Entry is the only metadata Oddward caches for an arc.
type Entry struct {
	Name   string
	Rarity string
}

// Catalog maps a pinyin effect key (the part after fork_) to an entry.
type Catalog struct {
	FetchedAt time.Time
	Items     map[string]Entry
}

// Lookup resolves a game identifier such as fork_wushoutieyu. Only the
// lowercase identifier shape used by the game is accepted, so the value is
// also safe to use as a cache file name.
func (c Catalog) Lookup(id string) (Entry, bool) {
	if !validID(id) {
		return Entry{}, false
	}
	e, ok := c.Items[id[5:]]
	return e, ok
}

func validID(id string) bool {
	if !strings.HasPrefix(id, "fork_") || len(id) == 5 || len(id) > 5+maxKey {
		return false
	}
	for _, r := range id[5:] {
		if r != '_' && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return false
		}
	}
	return true
}

type askResponse struct {
	Query struct {
		Results map[string]struct {
			FullText  string              `json:"fulltext"`
			Printouts map[string][]string `json:"printouts"`
		} `json:"results"`
	} `json:"query"`
}

type parseResponse struct {
	Parse struct {
		Text map[string]string `json:"text"`
	} `json:"parse"`
}

// Fetcher reads the live wiki. Tests can point Base at an httptest server.
type Fetcher struct {
	Base     string
	HTTP     *http.Client
	IconHost string
	Now      func() time.Time
}

func NewFetcher(client *http.Client) *Fetcher {
	return &Fetcher{Base: defaultAPI, HTTP: client, IconHost: defaultIconHost, Now: time.Now}
}

func (f *Fetcher) get(ctx context.Context, raw string) ([]byte, error) {
	if f.HTTP == nil {
		return nil, errors.New("bwiki: no HTTP client")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return nil, errors.New("bwiki: request must be HTTPS")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Oddward/1 (non-official launcher; metadata cache)")
	resp, err := f.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bwiki: HTTP %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxIconSize+1))
}

func (f *Fetcher) Fetch(ctx context.Context) (Catalog, error) {
	q := url.Values{}
	q.Set("action", "ask")
	q.Set("query", "[[分类:弧盘]]|?弧盘名|?稀有度|?效果")
	q.Set("format", "json")
	data, err := f.get(ctx, f.Base+"?"+q.Encode())
	if err != nil {
		return Catalog{}, err
	}
	var body askResponse
	if err := json.Unmarshal(data, &body); err != nil {
		return Catalog{}, fmt.Errorf("bwiki: decode arc list: %w", err)
	}
	out := Catalog{FetchedAt: f.Now().UTC(), Items: make(map[string]Entry)}
	for _, row := range body.Query.Results {
		name := first(row.Printouts["弧盘名"])
		effect := first(row.Printouts["效果"])
		rarity := first(row.Printouts["稀有度"])
		key := pinyin.Key(effect)
		if name == "" || key == "" || len(key) > maxKey || len(out.Items) >= maxItems {
			continue
		}
		if _, exists := out.Items[key]; exists {
			continue
		}
		out.Items[key] = Entry{Name: name, Rarity: normalRarity(rarity)}
	}
	if len(out.Items) == 0 {
		return Catalog{}, errors.New("bwiki: arc list was empty")
	}
	return out, nil
}

func first(v []string) string {
	if len(v) == 0 {
		return ""
	}
	return strings.TrimSpace(v[0])
}

func normalRarity(v string) string {
	v = strings.TrimSpace(v)
	if len(v) > maxRarity {
		return ""
	}
	return v
}

// Store keeps the cache under one launcher-owned directory.
type Store struct {
	dir     string
	fetcher *Fetcher
	now     func() time.Time
	mu      sync.Mutex
}

func Open(dir string, client *http.Client) (*Store, error) {
	if !filepath.IsAbs(dir) {
		return nil, errors.New("bwiki cache directory must be absolute")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{dir: dir, fetcher: NewFetcher(client), now: time.Now}, nil
}

func (s *Store) Catalog(ctx context.Context) (Catalog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.catalog(ctx)
}

func (s *Store) catalog(ctx context.Context) (Catalog, error) {
	cached, cacheErr := s.load()
	if cacheErr == nil && s.now().Sub(cached.FetchedAt) < cacheTTL {
		return cached, nil
	}
	fresh, err := s.fetcher.Fetch(ctx)
	if err != nil {
		if cacheErr == nil {
			return cached, err
		}
		return Catalog{}, err
	}
	if err := s.save(fresh); err != nil {
		return fresh, err
	}
	return fresh, nil
}

func (s *Store) Refresh(ctx context.Context) (Catalog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fresh, err := s.fetcher.Fetch(ctx)
	if err != nil {
		return Catalog{}, err
	}
	if err := s.save(fresh); err != nil {
		return fresh, err
	}
	return fresh, nil
}

var imagePattern = regexp.MustCompile(`(?s)<div class="img-container">.*?<img[^>]*src="([^"]+)"`)

// Icon returns the cached icon for id, fetching and caching it on first use.
// Only the identifier's entry needs to exist in the arc cache.
func (s *Store) Icon(ctx context.Context, id string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, err := s.catalog(ctx)
	if err != nil && len(c.Items) == 0 {
		return nil, err
	}
	entry, ok := c.Lookup(id)
	if !ok {
		return nil, errors.New("bwiki: no arc for identifier")
	}
	file := filepath.Join(s.dir, "icons", id+".img")
	if data, err := os.ReadFile(file); err == nil && len(data) > 0 && len(data) <= maxIconSize {
		return data, nil
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return nil, err
	}
	iconURL, err := s.iconURL(ctx, entry.Name)
	if err != nil {
		return nil, err
	}
	data, err := s.fetcher.get(ctx, iconURL)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > maxIconSize {
		return nil, errors.New("bwiki: icon has an unusable size")
	}
	if err := writeAtomic(file, data); err != nil {
		return nil, err
	}
	return data, nil
}

func (s *Store) iconURL(ctx context.Context, page string) (string, error) {
	q := url.Values{}
	q.Set("action", "parse")
	q.Set("page", page)
	q.Set("prop", "text")
	q.Set("format", "json")
	data, err := s.fetcher.get(ctx, s.fetcher.Base+"?"+q.Encode())
	if err != nil {
		return "", err
	}
	var body parseResponse
	if err := json.Unmarshal(data, &body); err != nil {
		return "", fmt.Errorf("bwiki: decode arc page: %w", err)
	}
	html := body.Parse.Text["*"]
	m := imagePattern.FindStringSubmatch(html)
	if len(m) != 2 {
		return "", errors.New("bwiki: arc page has no icon")
	}
	raw := strings.ReplaceAll(m[1], "&amp;", "&")
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != s.fetcher.IconHost {
		return "", errors.New("bwiki: unexpected icon host")
	}
	return raw, nil
}

type diskEntry struct {
	Name   string `json:"name"`
	Rarity string `json:"rarity"`
}

type diskCatalog struct {
	Format    string               `json:"format"`
	Version   int                  `json:"version"`
	FetchedAt int64                `json:"fetched_at"`
	Items     map[string]diskEntry `json:"items"`
}

func (s *Store) load() (Catalog, error) {
	file := filepath.Join(s.dir, cacheFile)
	info, err := os.Stat(file)
	if err != nil {
		return Catalog{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxCatalogSize {
		return Catalog{}, errors.New("bwiki: cache has an unusable shape")
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return Catalog{}, err
	}
	var d diskCatalog
	if err := json.Unmarshal(data, &d); err != nil {
		return Catalog{}, err
	}
	if d.Format != cacheFormat || d.Version != cacheVersion || d.FetchedAt < 0 {
		return Catalog{}, errors.New("bwiki: unknown cache format")
	}
	c := Catalog{FetchedAt: time.UnixMilli(d.FetchedAt).UTC(), Items: make(map[string]Entry, len(d.Items))}
	for key, item := range d.Items {
		if key == "" || len(key) > maxKey || item.Name == "" || len(item.Name) > maxName || len(item.Rarity) > maxRarity {
			return Catalog{}, errors.New("bwiki: invalid cache entry")
		}
		c.Items[key] = Entry{Name: item.Name, Rarity: item.Rarity}
	}
	return c, nil
}

func (s *Store) save(c Catalog) error {
	if len(c.Items) == 0 || len(c.Items) > maxItems || c.FetchedAt.IsZero() {
		return errors.New("bwiki: refusing to save an empty cache")
	}
	d := diskCatalog{Format: cacheFormat, Version: cacheVersion, FetchedAt: c.FetchedAt.UnixMilli(), Items: make(map[string]diskEntry, len(c.Items))}
	for key, item := range c.Items {
		if key == "" || len(key) > maxKey || item.Name == "" || len(item.Name) > maxName || len(item.Rarity) > maxRarity {
			return errors.New("bwiki: invalid cache entry")
		}
		d.Items[key] = diskEntry{Name: item.Name, Rarity: item.Rarity}
	}
	data, err := json.Marshal(d)
	if err != nil {
		return err
	}
	if len(data) > maxCatalogSize {
		return errors.New("bwiki: cache exceeds its size limit")
	}
	return writeAtomic(filepath.Join(s.dir, cacheFile), data)
}

func writeAtomic(name string, data []byte) (err error) {
	stage, err := os.CreateTemp(filepath.Dir(name), ".bwiki-*.tmp")
	if err != nil {
		return err
	}
	staged := stage.Name()
	committed := false
	defer func() {
		_ = stage.Close()
		if !committed {
			_ = os.Remove(staged)
		}
	}()
	if _, err := stage.Write(data); err != nil {
		return err
	}
	if err := stage.Sync(); err != nil {
		return err
	}
	if err := stage.Close(); err != nil {
		return err
	}
	if err := os.Rename(staged, name); err != nil {
		return err
	}
	committed = true
	return nil
}
