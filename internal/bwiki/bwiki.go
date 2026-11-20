// SPDX-License-Identifier: GPL-3.0-or-later

// Package bwiki reads the public 异环 BWIKI arc list and keeps a small local
// cache of the fields Oddward displays: name, rarity and icon. It never sends
// credentials and never reads account data.
package bwiki

import (
	"context"
	"database/sql"
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

	"github.com/nerv-base/Oddward/internal/database"
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

	// Domain is this package's migration_state and mirror domain. The arc
	// catalog is one legacy file, so one domain row describes it.
	Domain = "bwiki"

	// ImportVersion is the importer generation recorded for Domain.
	ImportVersion = 1
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

// Store keeps the arc catalog in the database and its icon files under one
// launcher-owned directory. The legacy arcs.json is written as a mirror because
// the updater can roll the launcher back to a build that reads only files. Icon
// bytes stay plain files: a lookup already costs one os.ReadFile, so an
// asset_cache row would only duplicate size and digest without saving a read.
type Store struct {
	db      *database.DB
	dir     string
	fetcher *Fetcher
	now     func() time.Time
	mu      sync.Mutex
}

// Open binds the catalog to db and the icon cache to dir, which must be
// absolute.
func Open(db *database.DB, dir string, client *http.Client) (*Store, error) {
	if db == nil {
		return nil, errors.New("bwiki cache requires an open database")
	}
	if !filepath.IsAbs(dir) {
		return nil, errors.New("bwiki cache directory must be absolute")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{db: db, dir: dir, fetcher: NewFetcher(client), now: time.Now}, nil
}

// ImportLegacy imports the legacy cache file once and leaves the rows alone when
// the file is absent. A later start re-runs the import only when that mirror no
// longer matches the digest this build recorded.
func (s *Store) ImportLegacy(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := s.db.RunImport(ctx, Domain, ImportVersion, s.importLegacy)
	return err
}

func (s *Store) Catalog(ctx context.Context) (Catalog, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.catalog(ctx)
}

func (s *Store) catalog(ctx context.Context) (Catalog, error) {
	cached, cacheErr := s.load(ctx)
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
	if err := s.save(ctx, fresh); err != nil {
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
	if err := s.save(ctx, fresh); err != nil {
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

// load reads the catalog and the time it was fetched from the database. A
// database without a cache row is a cache miss, not an empty catalog, because
// the caller must not treat an unfetched catalog as fresh.
func (s *Store) load(ctx context.Context) (Catalog, error) {
	var fetchedAt int64
	if err := s.db.QueryRow(ctx, `SELECT fetched_at FROM gacha_arcs_meta WHERE id = 1`).Scan(&fetchedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Catalog{}, fmt.Errorf("%w: bwiki arc cache", os.ErrNotExist)
		}
		return Catalog{}, err
	}
	c := Catalog{FetchedAt: database.StampInt(fetchedAt), Items: make(map[string]Entry)}
	rows, err := s.db.Query(ctx, `SELECT item_id, name, rarity FROM gacha_arcs`)
	if err != nil {
		return Catalog{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name, rarity string
		if err := rows.Scan(&id, &name, &rarity); err != nil {
			return Catalog{}, err
		}
		// The table stores the game identifier; the catalog is keyed by the
		// effect key after the fork_ prefix, which is how the wiki names it.
		if !validID(id) || len(name) > maxName || len(rarity) > maxRarity {
			return Catalog{}, errors.New("bwiki: invalid cache entry")
		}
		c.Items[id[5:]] = Entry{Name: name, Rarity: rarity}
	}
	if err := rows.Err(); err != nil {
		return Catalog{}, err
	}
	return c, nil
}

// save replaces the catalog rows and their fetch time in one transaction and
// writes the legacy cache file as a mirror afterwards.
func (s *Store) save(ctx context.Context, c Catalog) error {
	if len(c.Items) == 0 || len(c.Items) > maxItems || c.FetchedAt.IsZero() {
		return errors.New("bwiki: refusing to save an empty cache")
	}
	data, err := encodeCache(c)
	if err != nil {
		return err
	}
	path := filepath.Join(s.dir, cacheFile)
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := putArcs(ctx, tx, c); err != nil {
			return err
		}
		return s.db.RecordMirror(ctx, tx, Domain, path, database.Digest(data))
	})
	if err != nil {
		return err
	}
	if err := database.WriteMirror(path, data); err != nil {
		return fmt.Errorf("bwiki: arc cache is committed, legacy mirror not written: %w", err)
	}
	return nil
}

// putArcs replaces the stored catalog and its fetch time inside tx.
func putArcs(ctx context.Context, tx *sql.Tx, c Catalog) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM gacha_arcs`); err != nil {
		return fmt.Errorf("bwiki: replace arc cache: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM gacha_arcs_meta`); err != nil {
		return fmt.Errorf("bwiki: replace arc cache time: %w", err)
	}
	for key, item := range c.Items {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO gacha_arcs(item_id, name, rarity, updated_at) VALUES(?, ?, ?, ?)`,
			"fork_"+key, item.Name, item.Rarity, database.Stamp(c.FetchedAt)); err != nil {
			return fmt.Errorf("bwiki: store arc: %w", err)
		}
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO gacha_arcs_meta(id, fetched_at) VALUES(1, ?)`,
		database.Stamp(c.FetchedAt)); err != nil {
		return fmt.Errorf("bwiki: store arc cache time: %w", err)
	}
	return nil
}

// encodeCache validates the catalog and returns the legacy file bytes.
func encodeCache(c Catalog) ([]byte, error) {
	d := diskCatalog{Format: cacheFormat, Version: cacheVersion, FetchedAt: c.FetchedAt.UnixMilli(), Items: make(map[string]diskEntry, len(c.Items))}
	for key, item := range c.Items {
		if key == "" || len(key) > maxKey || item.Name == "" || len(item.Name) > maxName || len(item.Rarity) > maxRarity {
			return nil, errors.New("bwiki: invalid cache entry")
		}
		d.Items[key] = diskEntry(item)
	}
	data, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	if len(data) > maxCatalogSize {
		return nil, errors.New("bwiki: cache exceeds its size limit")
	}
	return data, nil
}

func decodeCache(data []byte) (Catalog, error) {
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
		c.Items[key] = Entry(item)
	}
	return c, nil
}

// importLegacy replaces the stored catalog with the legacy cache file, which is
// the database.ImportFunc for Domain. The file is never rewritten, and one
// unreadable file leaves the rows untouched.
func (s *Store) importLegacy(ctx context.Context) (source, detail string, err error) {
	path := filepath.Join(s.dir, cacheFile)
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return path, "no legacy arc cache", nil
	}
	if err != nil {
		return path, "", err
	}
	if !info.Mode().IsRegular() || info.Size() > maxCatalogSize {
		return path, "", fmt.Errorf("%s: bwiki cache has an unusable shape", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return path, "", err
	}
	c, err := decodeCache(data)
	if err != nil {
		return path, "", fmt.Errorf("%s: %w", path, err)
	}
	if err := ctx.Err(); err != nil {
		return path, "", err
	}
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := putArcs(ctx, tx, c); err != nil {
			return err
		}
		return s.db.RecordMirror(ctx, tx, Domain, path, database.Digest(data))
	})
	if err != nil {
		return path, "", err
	}
	return path, fmt.Sprintf("%d arcs", len(c.Items)), nil
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
