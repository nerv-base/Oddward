// SPDX-License-Identifier: GPL-3.0-or-later

package news

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/database"
	"github.com/nerv-base/Oddward/internal/database/dbtest"
)

// newCached builds a cached client over a fresh database and dir.
func newCached(t *testing.T, p *probe, dir string) (*CachedClient, *database.DB) {
	t.Helper()
	db := dbtest.Open(t)
	return NewCachedClient(db, p.client, dir), db
}

// fixtureFeed is a valid board the cache rules accept: allow-listed article URLs
// in the categories the channels publish, plus one banner.
func fixtureFeed(t *testing.T, p *probe) Feed {
	t.Helper()
	return Feed{
		Source: SourceLauncher,
		Items: []Item{
			{
				Title:    "Maintenance notice",
				URL:      p.server.URL + "/news/gamebroad/20261119/maintenance.html",
				Date:     time.Date(2026, 11, 19, 0, 0, 0, 0, time.UTC),
				Category: Announcements,
				Channel:  Announcements.Channel(),
			},
			{
				Title:    "Autumn event",
				URL:      p.server.URL + "/news/gameevent/20261118/autumn.html",
				Date:     time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC),
				Category: Events,
				Channel:  Events.Channel(),
			},
		},
		Banners: []Banner{
			{Title: "Official site", URL: p.server.URL + "/", ImageURL: p.server.URL + "/rms/yh/banner.png"},
		},
	}
}

func writeFeedFile(t *testing.T, dir string, feed Feed) []byte {
	t.Helper()
	data, err := json.Marshal(feed)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, feedMirror), data, 0o600); err != nil {
		t.Fatal(err)
	}
	return data
}

// storeSnapshot puts a payload straight into the snapshot row, which is how a row
// written by a build with different rules would come back.
func storeSnapshot(t *testing.T, db *database.DB, payload string) {
	t.Helper()
	if _, err := db.Exec(context.Background(), `
		INSERT INTO news_feed(id, payload, fetched_at) VALUES(?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			payload = excluded.payload, fetched_at = excluded.fetched_at`,
		feedID, payload, database.Stamp(time.Now())); err != nil {
		t.Fatal(err)
	}
}

func feedRow(t *testing.T, db *database.DB) (string, bool) {
	t.Helper()
	var payload string
	err := db.QueryRow(context.Background(), `SELECT payload FROM news_feed WHERE id = ?`, feedID).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false
	}
	if err != nil {
		t.Fatal(err)
	}
	return payload, true
}

func bodyRows(t *testing.T, db *database.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRow(context.Background(),
		`SELECT (SELECT COUNT(*) FROM asset_cache WHERE kind = ?) + (SELECT COUNT(*) FROM asset_cache WHERE kind = ?)`,
		kindArticle, kindImage).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

// countingWriter stands in for the database on the hit path and counts how many
// statements a burst of hits costs.
type countingWriter struct {
	db    *database.DB
	mu    sync.Mutex
	calls int
}

func (w *countingWriter) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	w.mu.Lock()
	w.calls++
	w.mu.Unlock()
	return w.db.Exec(ctx, query, args...)
}

func (w *countingWriter) count() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.calls
}

func TestCachedClientPersistsLastGoodFeedWithoutHidingUpstreamErrors(t *testing.T) {
	var offline atomic.Bool
	p := launcherProbe(t, func(p *probe, w http.ResponseWriter, r *http.Request) {
		if offline.Load() {
			http.Error(w, "outage", http.StatusServiceUnavailable)
			return
		}
		switch r.URL.Path {
		case "/launcher/launcher_ob2.html":
			fmt.Fprint(w, p.launcherPageBody())
		case "/yh-gameSwiper.js":
			fmt.Fprint(w, p.carouselScript())
		default:
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()
	dir := t.TempDir()
	db := dbtest.Open(t)
	live, err := NewCachedClient(db, p.client, dir).Feed(ctx)
	if err != nil || len(live.Items) == 0 || len(live.Banners) == 0 {
		t.Fatalf("live feed = %+v, %v", live, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, feedMirror)); err != nil {
		t.Fatalf("the refresh must leave the mirror a rolled-back build reads: %v", err)
	}

	offline.Store(true)
	reopened := NewCachedClient(db, p.client, dir)
	before := p.hits.Load()
	cached, err := reopened.CachedFeed(ctx)
	if err != nil || !reflect.DeepEqual(cached, live) {
		t.Fatalf("reopened snapshot = %+v, %v; want %+v", cached, err, live)
	}
	if p.hits.Load() != before {
		t.Fatal("CachedFeed contacted the network")
	}
	if _, err := reopened.Feed(ctx); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("Feed should report the upstream outage, got %v", err)
	}
	cached, err = reopened.CachedFeed(ctx)
	if err != nil || !reflect.DeepEqual(cached, live) {
		t.Fatalf("failed refresh changed the saved snapshot: %+v, %v", cached, err)
	}
}

func TestCachedClientMergesPartialFeedAndKeepsSnapshotOnEmptyRefresh(t *testing.T) {
	var phase atomic.Int32
	p := launcherProbe(t, func(p *probe, w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/launcher/launcher_ob2.html":
			switch phase.Load() {
			case 0:
				fmt.Fprint(w, p.launcherPageBody())
			case 1:
				fmt.Fprint(w, `<div class="news-cont"><ul><li><a href="/news/gamebroad/20261119/revised.html">Updated story</a><span>11/19</span></li></ul></div>`)
			default:
				fmt.Fprint(w, `<div class="news-cont"><ul></ul></div>`)
			}
		case "/yh-gameSwiper.js":
			if phase.Load() == 0 {
				fmt.Fprint(w, p.carouselScript())
				return
			}
			http.Error(w, "script offline", http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()
	dir := t.TempDir()
	db := dbtest.Open(t)
	initial, err := NewCachedClient(db, p.client, dir).Feed(ctx)
	if err != nil || len(initial.Banners) == 0 {
		t.Fatalf("initial feed = %+v, %v", initial, err)
	}
	phase.Store(1)
	partial, err := NewCachedClient(db, p.client, dir).Feed(ctx)
	if err != nil || len(partial.Items) != 1 || partial.Items[0].Title != "Updated story" || !reflect.DeepEqual(partial.Banners, initial.Banners) {
		t.Fatalf("partial refresh should show fresh items with saved banners, got %+v, %v", partial, err)
	}
	phase.Store(2)
	empty, err := NewCachedClient(db, p.client, dir).Feed(ctx)
	if err != nil || len(empty.Items) != 0 || len(empty.Banners) != 0 {
		t.Fatalf("empty refresh should reach the caller as received, got %+v, %v", empty, err)
	}
	saved, err := NewCachedClient(db, p.client, dir).CachedFeed(ctx)
	if err != nil || !reflect.DeepEqual(saved, partial) {
		t.Fatalf("snapshot after empty refresh = %+v, %v; want %+v", saved, err, partial)
	}
}

func TestCachedClientRejectsInvalidSnapshots(t *testing.T) {
	const good = `{"Source":"launcher","Banners":[{"Title":"Official","URL":"https://yh.wanmei.com/","ImageURL":"https://yhvmg.wmupd.com/one.png"}]}`
	cases := map[string]string{
		"trailing data":      good + `{}`,
		"external image":     strings.Replace(good, "yhvmg.wmupd.com", "elsewhere.example", 1),
		"external link":      strings.Replace(good, "yh.wanmei.com/", "elsewhere.example/", 1),
		"credentialled link": strings.Replace(good, "yh.wanmei.com/", "user:secret@yh.wanmei.com/", 1),
		"relative link":      strings.Replace(good, "https://yh.wanmei.com/", "/", 1),
		"unknown source":     strings.Replace(good, `"launcher"`, `"elsewhere"`, 1),
		"empty board":        `{"Source":"launcher","Items":[]}`,
	}
	for label, data := range cases {
		t.Run(label, func(t *testing.T) {
			db := dbtest.Open(t)
			c := NewCachedClient(db, NewClient(), t.TempDir())
			storeSnapshot(t, db, good)
			if _, err := c.CachedFeed(context.Background()); err != nil {
				t.Fatalf("fixture must be readable before corruption: %v", err)
			}
			storeSnapshot(t, db, data)
			if _, err := c.CachedFeed(context.Background()); !errors.Is(err, errInvalidNewsCache) {
				t.Fatalf("invalid snapshot was accepted: %v", err)
			}
		})
	}
}

// TestCachedFeedRejectsAnOversizedSnapshot keeps the reader's size limit: a row
// this build did not write must not cost more memory than a fetched feed.
func TestCachedFeedRejectsAnOversizedSnapshot(t *testing.T) {
	db := dbtest.Open(t)
	c := NewCachedClient(db, NewClient(), t.TempDir())
	payload := `{"Source":"launcher","Banners":[{"Title":"` + strings.Repeat("x", maxCachedFeed) + `"}]}`
	storeSnapshot(t, db, payload)
	if _, err := c.CachedFeed(context.Background()); !errors.Is(err, errInvalidNewsCache) {
		t.Fatalf("oversized snapshot was accepted: %v", err)
	}
}

func TestCacheImportAdoptsTheLegacyFeed(t *testing.T) {
	p := newProbe(t, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "news")
	db := dbtest.Open(t)
	want := fixtureFeed(t, p)
	writeFeedFile(t, dir, want)
	c := NewCachedClient(db, p.client, dir)

	ran, err := db.RunImport(ctx, Domain, ImportVersion, c.ImportLegacy)
	if err != nil || !ran {
		t.Fatalf("first import = %v, %v", ran, err)
	}
	got, err := c.CachedFeed(ctx)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("imported snapshot = %+v, %v; want %+v", got, err, want)
	}
	if stale, err := db.MirrorStale(ctx, Domain); err != nil || stale {
		t.Fatalf("the adopted mirror should be recorded as written: stale=%v, %v", stale, err)
	}

	// A second start must not rewrite anything.
	if ran, err := db.RunImport(ctx, Domain, ImportVersion, c.ImportLegacy); err != nil || ran {
		t.Fatalf("second import = %v, %v; want a no-op", ran, err)
	}
	if got, err := c.CachedFeed(ctx); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("second import changed the snapshot: %+v, %v", got, err)
	}

	// A rolled-back build changes the mirror it can still read; the next start has
	// to reconcile with it.
	revised := want
	revised.Banners = []Banner{{Title: "Revised banner", URL: p.server.URL + "/", ImageURL: p.server.URL + "/rms/yh/banner2.png"}}
	writeFeedFile(t, dir, revised)
	if stale, err := db.MirrorStale(ctx, Domain); err != nil || !stale {
		t.Fatalf("a mirror changed under the database must be stale: stale=%v, %v", stale, err)
	}
	if ran, err := db.RunImport(ctx, Domain, ImportVersion, c.ImportLegacy); err != nil || !ran {
		t.Fatalf("reconciling import = %v, %v", ran, err)
	}
	if got, err := c.CachedFeed(ctx); err != nil || !reflect.DeepEqual(got, revised) {
		t.Fatalf("reconciled snapshot = %+v, %v; want %+v", got, err, revised)
	}
}

func TestCacheImportAdoptsLegacyBucketBodies(t *testing.T) {
	png := syntheticPNG(t, 8, 8)
	p := newProbe(t, func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "offline", http.StatusServiceUnavailable)
	})
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "news")
	db := dbtest.Open(t)
	target := p.server.URL + "/rms/yh/legacy.png"
	id := assetIDFor(target)
	bucket := binary.LittleEndian.Uint32(id.sum[:4]) % imageCache.buckets
	name := fmt.Sprintf("%s-%02d-%d", imageCache.prefix, bucket, 0)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := append(append([]byte{}, id.sum[:]...), png...)
	if err := os.WriteFile(filepath.Join(dir, name), body, 0o600); err != nil {
		t.Fatal(err)
	}
	c := NewCachedClient(db, p.client, dir)
	if ran, err := db.RunImport(ctx, Domain, ImportVersion, c.ImportLegacy); err != nil || !ran {
		t.Fatalf("import = %v, %v", ran, err)
	}
	if got, err := c.Image(ctx, target); err != nil || !bytes.Equal(got, png) {
		t.Fatalf("adopted image = %d bytes, %v; want %d bytes", len(got), err, len(png))
	}
}

func TestCacheImportRejectsACorruptFeed(t *testing.T) {
	p := newProbe(t, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "news")
	db := dbtest.Open(t)
	broken := fixtureFeed(t, p)
	broken.Items[0].URL = "https://elsewhere.example/news/gamebroad/20261119/maintenance.html"
	data := writeFeedFile(t, dir, broken)
	c := NewCachedClient(db, p.client, dir)

	if _, err := db.RunImport(ctx, Domain, ImportVersion, c.ImportLegacy); err == nil || !strings.Contains(err.Error(), feedMirror) {
		t.Fatalf("corrupt feed error = %v, want one naming %s", err, feedMirror)
	}
	if _, ok := feedRow(t, db); ok {
		t.Fatal("a rejected import must not write a snapshot row")
	}
	if rows := bodyRows(t, db); rows != 0 {
		t.Fatalf("a rejected import wrote %d body rows", rows)
	}
	if after, err := os.ReadFile(filepath.Join(dir, feedMirror)); err != nil || !bytes.Equal(after, data) {
		t.Fatalf("a rejected import changed the file: %v", err)
	}
}

func TestCacheImportWithoutALegacySourceIsANoOp(t *testing.T) {
	p := newProbe(t, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	ctx := context.Background()
	for _, dir := range []string{filepath.Join(t.TempDir(), "absent"), t.TempDir()} {
		db := dbtest.Open(t)
		c := NewCachedClient(db, p.client, dir)
		ran, err := db.RunImport(ctx, Domain, ImportVersion, c.ImportLegacy)
		if err != nil || !ran {
			t.Fatalf("import of %s = %v, %v", dir, ran, err)
		}
		if _, ok := feedRow(t, db); ok {
			t.Fatal("an absent source must not write a snapshot row")
		}
		if _, err := c.CachedFeed(ctx); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("no cache should report os.ErrNotExist, got %v", err)
		}
	}
	// A build with no database cannot keep anything; startup must not fail on it.
	disabled := NewCachedClient(nil, p.client, t.TempDir())
	if source, detail, err := disabled.ImportLegacy(ctx); err != nil || source != "" || detail == "" {
		t.Fatalf("disabled import = %q, %q, %v", source, detail, err)
	}
}

func TestCachedClientImageReusesValidatedBytesByURL(t *testing.T) {
	want := syntheticPNG(t, 8, 8)
	var offline atomic.Bool
	p := newProbe(t, func(w http.ResponseWriter, r *http.Request) {
		if offline.Load() {
			http.Error(w, "outage", http.StatusServiceUnavailable)
			return
		}
		w.Write(want)
	})
	ctx := context.Background()
	dir := t.TempDir()
	target := p.server.URL + "/rms/yh/banner.png"
	client, db := newCached(t, p, dir)
	if got, err := client.Image(ctx, target); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("online image = %d bytes, %v", len(got), err)
	}
	var (
		name, digest, source string
		size                 int64
	)
	id := assetIDFor(target)
	if err := db.QueryRow(ctx, `SELECT path, size, digest, source_url FROM asset_cache WHERE kind = ? AND key = ?`,
		kindImage, id.key).Scan(&name, &size, &digest, &source); err != nil {
		t.Fatalf("the stored image must be indexed: %v", err)
	}
	if size != int64(len(want)) || digest != database.Digest(want) || source != target || !strings.HasPrefix(name, imageCache.prefix+"-") {
		t.Fatalf("row = %q %d %q %q", name, size, digest, source)
	}

	offline.Store(true)
	reopened := NewCachedClient(db, p.client, dir)
	before := p.hits.Load()
	if got, err := reopened.Image(ctx, target); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("reopened image = %d bytes, %v; want %d bytes", len(got), err, len(want))
	}
	if p.hits.Load() != before {
		t.Fatal("Image should use the cached URL without reaching the upstream")
	}
	if _, err := reopened.Image(ctx, target+"?version=2"); !errors.Is(err, ErrImage) {
		t.Fatalf("uncached URL should return the upstream error, got %v", err)
	}
	p.client.ImageHosts = nil
	if _, err := reopened.Image(ctx, target); !errors.Is(err, ErrImage) {
		t.Fatalf("image host no longer allowed, got %v", err)
	}
}

func TestCachedClientServesAStaleImageButRevalidatesIt(t *testing.T) {
	first := syntheticPNG(t, 8, 8)
	second := syntheticPNG(t, 4, 4)
	var current atomic.Pointer[[]byte]
	current.Store(&first)
	var offline atomic.Bool
	p := newProbe(t, func(w http.ResponseWriter, r *http.Request) {
		if offline.Load() {
			http.Error(w, "outage", http.StatusServiceUnavailable)
			return
		}
		w.Write(*current.Load())
	})
	ctx := context.Background()
	client, db := newCached(t, p, t.TempDir())
	target := p.server.URL + "/rms/yh/banner.png"
	if _, err := client.Image(ctx, target); err != nil {
		t.Fatal(err)
	}
	expire := func() {
		t.Helper()
		if _, err := db.Exec(ctx, `UPDATE asset_cache SET expires_at = ? WHERE kind = ? AND key = ?`,
			database.Stamp(time.Now().Add(-time.Hour)), kindImage, assetIDFor(target).key); err != nil {
			t.Fatal(err)
		}
	}

	expire()
	before := p.hits.Load()
	got, err := client.Image(ctx, target)
	if err != nil || !bytes.Equal(got, first) {
		t.Fatalf("stale image = %d bytes, %v", len(got), err)
	}
	if p.hits.Load() != before+1 {
		t.Fatal("an expired body must be revalidated before it is served")
	}

	// The refresh replaces the pixels and the row that names them.
	current.Store(&second)
	expire()
	if got, err := client.Image(ctx, target); err != nil || !bytes.Equal(got, second) {
		t.Fatalf("refreshed image = %d bytes, %v; want the new pixels", len(got), err)
	}
	var expiresAt int64
	if err := db.QueryRow(ctx, `SELECT expires_at FROM asset_cache WHERE kind = ? AND key = ?`,
		kindImage, assetIDFor(target).key).Scan(&expiresAt); err != nil {
		t.Fatal(err)
	}
	if expiresAt <= time.Now().UnixNano() {
		t.Fatal("a successful refresh must extend the expiry")
	}

	// An expired body is still the better answer when the refresh fails.
	expire()
	offline.Store(true)
	if got, err := client.Image(ctx, target); err != nil || !bytes.Equal(got, second) {
		t.Fatalf("offline stale image = %d bytes, %v; the stored copy must still be served", len(got), err)
	}
}

func TestCachedClientBoundsImageRetentionAcrossManyDistinctURLs(t *testing.T) {
	png := syntheticPNG(t, 2, 2)
	p := newProbe(t, func(w http.ResponseWriter, r *http.Request) { w.Write(png) })
	dir := t.TempDir()
	ctx := context.Background()
	client, db := newCached(t, p, dir)
	for i := range 96 {
		url := fmt.Sprintf("%s/banner-%d.png", p.server.URL, i)
		if _, err := client.Image(ctx, url); err != nil {
			t.Fatalf("image %d: %v", i, err)
		}
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 || len(files) > 32 {
		t.Fatalf("96 distinct images consumed %d cache files, want 1 through 32", len(files))
	}
	if rows := bodyRows(t, db); rows != len(files) {
		t.Fatalf("%d rows name %d files; the index and the bucket must agree", rows, len(files))
	}
	before := p.hits.Load()
	if got, err := NewCachedClient(db, p.client, dir).Image(ctx, p.server.URL+"/banner-95.png"); err != nil || !bytes.Equal(got, png) {
		t.Fatalf("most recently stored image = %d bytes, %v", len(got), err)
	}
	if p.hits.Load() != before {
		t.Fatal("most recently stored image should still be cached")
	}
}

func TestCachedClientTreatsAMissingFileAsAMiss(t *testing.T) {
	png := syntheticPNG(t, 8, 8)
	var offline atomic.Bool
	p := newProbe(t, func(w http.ResponseWriter, r *http.Request) {
		if offline.Load() {
			http.Error(w, "outage", http.StatusServiceUnavailable)
			return
		}
		w.Write(png)
	})
	ctx := context.Background()
	dir := t.TempDir()
	client, db := newCached(t, p, dir)
	target := p.server.URL + "/rms/yh/banner.png"
	if _, err := client.Image(ctx, target); err != nil {
		t.Fatal(err)
	}
	var name string
	if err := db.QueryRow(ctx, `SELECT path FROM asset_cache WHERE kind = ? AND key = ?`,
		kindImage, assetIDFor(target).key).Scan(&name); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, name)); err != nil {
		t.Fatal(err)
	}

	// The row still exists but cannot be served, so the cache must not claim a hit.
	offline.Store(true)
	if _, err := client.Image(ctx, target); !errors.Is(err, ErrImage) {
		t.Fatalf("a row whose file is gone must be a miss, got %v", err)
	}
	offline.Store(false)
	before := p.hits.Load()
	if got, err := client.Image(ctx, target); err != nil || !bytes.Equal(got, png) {
		t.Fatalf("refetched image = %d bytes, %v", len(got), err)
	}
	if p.hits.Load() != before+1 {
		t.Fatal("a missing file must be fetched again")
	}
	offline.Store(true)
	if got, err := client.Image(ctx, target); err != nil || !bytes.Equal(got, png) {
		t.Fatalf("restored image = %d bytes, %v", len(got), err)
	}
}

func TestCachedClientBatchesHitWrites(t *testing.T) {
	png := syntheticPNG(t, 8, 8)
	p := newProbe(t, func(w http.ResponseWriter, r *http.Request) { w.Write(png) })
	ctx := context.Background()
	client, db := newCached(t, p, t.TempDir())
	// One page of banners reads a whole row set; every one of these bodies is a
	// separate row, and three per bucket keeps them all in their slots.
	var targets []string
	for bucket := range imageCache.buckets {
		targets = append(targets, urlsForBucket(t, p.server.URL, "hit", imageCache.buckets, 3, bucket)...)
	}
	for _, target := range targets {
		if _, err := client.Image(ctx, target); err != nil {
			t.Fatal(err)
		}
	}

	counter := &countingWriter{db: db}
	client.lastUsed = counter
	// Open the batch window so the burst is measured as one page read, not as the
	// first hit of a fresh process.
	client.flushed = time.Now()
	hits := p.hits.Load()
	for _, target := range targets {
		if _, err := client.Image(ctx, target); err != nil {
			t.Fatal(err)
		}
	}
	if p.hits.Load() != hits {
		t.Fatal("the burst must be served from the cache")
	}
	if calls := counter.count(); calls == 0 || calls > 3 {
		t.Fatalf("%d cache hits cost %d statements; the hit path must batch", len(targets), calls)
	}
}

func TestCachedClientPrunesOnlyItsOwnFiles(t *testing.T) {
	png := syntheticPNG(t, 2, 2)
	p := newProbe(t, func(w http.ResponseWriter, r *http.Request) { w.Write(png) })
	ctx := context.Background()
	dir := t.TempDir()
	client, db := newCached(t, p, dir)

	// Four bodies fill one bucket; a fifth takes the least recently used slot and
	// is what lets the pruner run.
	same := urlsInOneBucket(t, p.server.URL, "same", imageCache.buckets, slotsPerBucket)
	for _, url := range same {
		if _, err := client.Image(ctx, url); err != nil {
			t.Fatal(err)
		}
	}
	bucket := bucketOf(same[0], imageCache.buckets)
	other := urlInAnotherBucket(t, p.server.URL, "other", imageCache.buckets, bucket)
	if _, err := client.Image(ctx, other); err != nil {
		t.Fatal(err)
	}
	abandoned := imageNameFor(t, db, other)
	if _, err := db.Exec(ctx, `UPDATE asset_cache SET last_used = ? WHERE kind = ? AND key = ?`,
		database.Stamp(time.Now().Add(-2*cachedBodyMaxAge)), kindImage, assetIDFor(other).key); err != nil {
		t.Fatal(err)
	}
	// A file no row names may be a body the importer is about to adopt, and a row
	// of another kind belongs to the icon cache.
	stray := filepath.Join(dir, "stray-body.bin")
	if err := os.WriteFile(stray, []byte("leave me"), 0o600); err != nil {
		t.Fatal(err)
	}
	iconFile := filepath.Join(dir, "icon-stray.png")
	if err := os.WriteFile(iconFile, []byte("icon"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `
		INSERT INTO asset_cache(
			kind, key, path, size, digest, source_url, etag, last_modified,
			fetched_at, expires_at, last_used, status, detail)
		VALUES('icon', 'icon-key', 'icon-stray.png', 4, ?, '', '', '', ?, ?, ?, 'ok', '')`,
		database.Digest([]byte("icon")),
		database.Stamp(time.Now().Add(-2*cachedBodyMaxAge)), database.Stamp(time.Now()),
		database.Stamp(time.Now().Add(-2*cachedBodyMaxAge))); err != nil {
		t.Fatal(err)
	}

	if _, err := client.Image(ctx, anotherURLInBucket(t, p.server.URL, "fifth", imageCache.buckets, bucket, same)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(dir, abandoned)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an abandoned body should be pruned with its row: %v", err)
	}
	var stale int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM asset_cache WHERE key = ?`, assetIDFor(other).key).Scan(&stale); err != nil || stale != 0 {
		t.Fatalf("abandoned row = %d, %v", stale, err)
	}
	for _, path := range []string{stray, iconFile} {
		if _, err := os.Lstat(path); err != nil {
			t.Fatalf("%s must survive pruning: %v", path, err)
		}
	}
	var owned int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM asset_cache WHERE kind = 'icon' AND key = 'icon-key'`).Scan(&owned); err != nil || owned != 1 {
		t.Fatalf("another kind's row = %d, %v", owned, err)
	}
}

// imageNameFor is the bucket file the row of rawURL names.
func imageNameFor(t *testing.T, db *database.DB, rawURL string) string {
	t.Helper()
	var name string
	if err := db.QueryRow(context.Background(),
		`SELECT path FROM asset_cache WHERE kind = ? AND key = ?`, kindImage, assetIDFor(rawURL).key).Scan(&name); err != nil {
		t.Fatal(err)
	}
	return name
}

func bucketOf(rawURL string, buckets uint32) uint32 {
	sum := sha256.Sum256([]byte(rawURL))
	return binary.LittleEndian.Uint32(sum[:4]) % buckets
}

// urlsInOneBucket returns want distinct URLs whose bodies share one bucket.
func urlsInOneBucket(t *testing.T, base, prefix string, buckets uint32, want int) []string {
	t.Helper()
	byBucket := map[uint32][]string{}
	for i := 0; i < 4096; i++ {
		url := fmt.Sprintf("%s/%s-%d.png", base, prefix, i)
		bucket := bucketOf(url, buckets)
		byBucket[bucket] = append(byBucket[bucket], url)
		if len(byBucket[bucket]) == want {
			return byBucket[bucket]
		}
	}
	t.Fatalf("could not gather %d urls in one bucket", want)
	return nil
}

// urlsForBucket returns want distinct URLs that share the given bucket.
func urlsForBucket(t *testing.T, base, prefix string, buckets uint32, want int, bucket uint32) []string {
	t.Helper()
	urls := make([]string, 0, want)
	for i := 0; i < 4096 && len(urls) < want; i++ {
		url := fmt.Sprintf("%s/%s-%d.png", base, prefix, i)
		if bucketOf(url, buckets) == bucket {
			urls = append(urls, url)
		}
	}
	if len(urls) < want {
		t.Fatalf("could not gather %d urls in bucket %d", want, bucket)
	}
	return urls
}

func urlInAnotherBucket(t *testing.T, base, prefix string, buckets, avoid uint32) string {
	t.Helper()
	for i := 0; i < 4096; i++ {
		url := fmt.Sprintf("%s/%s-%d.png", base, prefix, i)
		if bucketOf(url, buckets) != avoid {
			return url
		}
	}
	t.Fatalf("no url outside bucket %d", avoid)
	return ""
}

func anotherURLInBucket(t *testing.T, base, prefix string, buckets, want uint32, used []string) string {
	t.Helper()
	taken := map[string]struct{}{}
	for _, url := range used {
		taken[url] = struct{}{}
	}
	for i := 0; i < 4096; i++ {
		url := fmt.Sprintf("%s/%s-%d.png", base, prefix, i)
		if _, ok := taken[url]; ok {
			continue
		}
		if bucketOf(url, buckets) == want {
			return url
		}
	}
	t.Fatalf("no fifth url in bucket %d", want)
	return ""
}

func TestCachedClientImageRejectsTruncatedPixelData(t *testing.T) {
	png := syntheticPNG(t, 8, 8)
	truncated := png[:len(png)-12] // Config can be read even without the IEND chunk.
	p := newProbe(t, func(w http.ResponseWriter, r *http.Request) { w.Write(truncated) })
	client, _ := newCached(t, p, t.TempDir())
	if _, err := client.Image(context.Background(), p.server.URL+"/truncated.png"); !errors.Is(err, ErrImage) {
		t.Fatalf("truncated image should fail full decode, got %v", err)
	}
}

func TestCachedClientDetailFallsBackOnlyForAnAllowlistedURL(t *testing.T) {
	var offline atomic.Bool
	p := newProbe(t, func(w http.ResponseWriter, r *http.Request) {
		if offline.Load() {
			http.Error(w, "outage", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `<h1 class="articleTitle">Official notice</h1><div class="articleContent"><p>Read &amp; saved.</p></div>`)
	})
	ctx := context.Background()
	dir := t.TempDir()
	target := p.server.URL + "/news/gamebroad/20261119/notice.html"
	client, db := newCached(t, p, dir)
	first, err := client.Detail(ctx, target)
	if err != nil || !reflect.DeepEqual(first.Paragraphs, []string{"Read & saved."}) {
		t.Fatalf("online article = %+v, %v", first, err)
	}

	offline.Store(true)
	reopened := NewCachedClient(db, p.client, dir)
	before := p.hits.Load()
	if got, err := reopened.Detail(ctx, target); err != nil || !reflect.DeepEqual(got, first) {
		t.Fatalf("offline article = %+v, %v; want %+v", got, err, first)
	}
	if p.hits.Load() != before+1 {
		t.Fatal("Detail should check the upstream before falling back")
	}
	if _, err := reopened.Detail(ctx, target+"?different"); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("uncached URL should return the upstream error, got %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := reopened.Detail(canceled, target); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Detail = %v", err)
	}
	p.client.Hosts = nil
	if _, err := reopened.Detail(ctx, target); err == nil || !strings.Contains(err.Error(), "not an official source") {
		t.Fatalf("cached article whose host is no longer allowed = %v", err)
	}
}
