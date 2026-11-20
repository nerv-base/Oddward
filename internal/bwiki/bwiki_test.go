// SPDX-License-Identifier: GPL-3.0-or-later

package bwiki

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/database"
	"github.com/nerv-base/Oddward/internal/database/dbtest"
)

const askBody = `{"query":{"results":{
  "焰魂狂飙":{"fulltext":"焰魂狂飙","printouts":{"弧盘名":["焰魂狂飙"],"稀有度":["S"],"效果":["「无首铁驭」"]}},
  "坠入深渊":{"fulltext":"坠入深渊","printouts":{"弧盘名":["坠入深渊"],"稀有度":["A"],"效果":["深海回响"]}}
}}}`

func testServer(t *testing.T, counts *atomic.Int32) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api.php", func(w http.ResponseWriter, r *http.Request) {
		counts.Add(1)
		switch r.URL.Query().Get("action") {
		case "ask":
			fmt.Fprint(w, askBody)
		case "parse":
			base := "https://" + r.Host
			fmt.Fprintf(w, `{"parse":{"text":{"*":"<div class=\"img-container\"><img src=\"%s/icon.png\"></div>"}}}`, base)
		default:
			http.NotFound(w, r)
		}
	})
	mux.HandleFunc("/icon.png", func(w http.ResponseWriter, r *http.Request) {
		counts.Add(1)
		w.Header().Set("Content-Type", "image/png")
		fmt.Fprint(w, "\x89PNG\r\n\x1a\nfake")
	})
	srv := httptest.NewTLSServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func newTestStore(t *testing.T, srv *httptest.Server) *Store {
	t.Helper()
	s, err := Open(dbtest.Open(t), t.TempDir(), srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(srv.URL)
	s.fetcher.Base = srv.URL + "/api.php"
	s.fetcher.IconHost = u.Host
	return s
}

func TestFetcherFetch(t *testing.T) {
	var count atomic.Int32
	srv := testServer(t, &count)
	f := NewFetcher(srv.Client())
	f.Base = srv.URL + "/api.php"
	c, err := f.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(c.Items))
	}
	if got := c.Items["wushoutieyu"]; got.Name != "焰魂狂飙" || got.Rarity != "S" {
		t.Errorf("entry = %+v", got)
	}
	if got := c.Items["shenhaihuixiang"]; got.Name != "坠入深渊" || got.Rarity != "A" {
		t.Errorf("entry = %+v", got)
	}
}

func TestStoreCatalogCaches(t *testing.T) {
	var count atomic.Int32
	srv := testServer(t, &count)
	s := newTestStore(t, srv)
	now := time.Now()
	s.now = func() time.Time { return now }
	s.fetcher.Now = func() time.Time { return now }

	if _, err := s.Catalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Catalog(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := count.Load(); got != 1 {
		t.Fatalf("requests = %d, want 1 cache hit", got)
	}
}

func TestStoreCatalogUsesStaleCacheOnFailure(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	}))
	t.Cleanup(srv.Close)
	s := newTestStore(t, srv)
	stale := Catalog{FetchedAt: time.Now().Add(-30 * 24 * time.Hour), Items: map[string]Entry{
		"wushoutieyu": {Name: "焰魂狂飙", Rarity: "S"},
	}}
	if err := s.save(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	c, err := s.Catalog(context.Background())
	if err == nil {
		t.Fatal("expected refresh error")
	}
	if got, ok := c.Lookup("fork_wushoutieyu"); !ok || got != (Entry{Name: "焰魂狂飙", Rarity: "S"}) {
		t.Fatalf("stale cache not returned: %+v", c)
	}
}

func TestStoreIconFetchesAndCaches(t *testing.T) {
	var count atomic.Int32
	srv := testServer(t, &count)
	s := newTestStore(t, srv)
	now := time.Now()
	s.now = func() time.Time { return now }
	s.fetcher.Now = func() time.Time { return now }

	icon, err := s.Icon(context.Background(), "fork_wushoutieyu")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(icon), "\x89PNG") {
		t.Fatalf("icon = %q", icon)
	}
	if _, err := os.Stat(filepath.Join(s.dir, "icons", "fork_wushoutieyu.img")); err != nil {
		t.Fatal(err)
	}
	before := count.Load()
	if _, err := s.Icon(context.Background(), "fork_wushoutieyu"); err != nil {
		t.Fatal(err)
	}
	if got := count.Load(); got != before {
		t.Fatalf("second icon fetch made requests: %d -> %d", before, got)
	}
}

func TestLookup(t *testing.T) {
	c := Catalog{Items: map[string]Entry{"wushoutieyu": {Name: "焰魂狂飙"}}}
	for _, id := range []string{"fork_wushoutieyu", "FORK_wushoutieyu", "fork_", "wushoutieyu", "fork_missing", "fork_../x", "fork_abC", "fork_abc/def"} {
		_, ok := c.Lookup(id)
		if want := id == "fork_wushoutieyu"; ok != want {
			t.Errorf("Lookup(%q) = %v, want %v", id, ok, want)
		}
	}
}

func TestIconRejectsForeignHost(t *testing.T) {
	var count atomic.Int32
	srv := testServer(t, &count)
	s := newTestStore(t, srv)
	iconURL, err := s.iconURL(context.Background(), "焰魂狂飙")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(iconURL, "https://") {
		t.Fatalf("icon URL = %q", iconURL)
	}
	s.fetcher.IconHost = "example.invalid"
	if _, err := s.Icon(context.Background(), "fork_wushoutieyu"); err == nil {
		t.Fatal("foreign icon host was accepted")
	}
}

func TestStoreCatalogRefreshesStaleRow(t *testing.T) {
	var count atomic.Int32
	srv := testServer(t, &count)
	s := newTestStore(t, srv)
	s.fetcher.Now = func() time.Time { return time.Now() }
	ctx := context.Background()
	stale := Catalog{FetchedAt: time.Now().Add(-8 * 24 * time.Hour), Items: map[string]Entry{
		"wushoutieyu": {Name: "旧名", Rarity: "B"},
	}}
	if err := s.save(ctx, stale); err != nil {
		t.Fatal(err)
	}
	c, err := s.Catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := count.Load(); got != 1 {
		t.Fatalf("requests = %d, want one refresh of an eight-day-old row", got)
	}
	if got, ok := c.Lookup("fork_wushoutieyu"); !ok || got.Name != "焰魂狂飙" {
		t.Fatalf("refreshed = %+v", c)
	}
	if fresh, err := s.load(ctx); err != nil || fresh.FetchedAt.Before(stale.FetchedAt) {
		t.Fatalf("stored refresh = %+v, %v", fresh, err)
	}
}

func TestStoreSaveWritesLegacyMirror(t *testing.T) {
	var count atomic.Int32
	s := newTestStore(t, testServer(t, &count))
	ctx := context.Background()
	want := Catalog{FetchedAt: time.Now().UTC(), Items: map[string]Entry{"wushoutieyu": {Name: "焰魂狂飙", Rarity: "S"}}}
	if err := s.save(ctx, want); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(s.dir, cacheFile))
	if err != nil {
		t.Fatalf("mirror: %v", err)
	}
	got, err := decodeCache(data)
	if err != nil {
		t.Fatalf("mirror decode: %v", err)
	}
	// The legacy file keeps the millisecond precision it always had.
	if got.FetchedAt.UnixMilli() != want.FetchedAt.UnixMilli() || len(got.Items) != 1 || got.Items["wushoutieyu"] != want.Items["wushoutieyu"] {
		t.Fatalf("mirror = %+v, want %+v", got, want)
	}
	var digest string
	if err := s.db.QueryRow(ctx, `SELECT digest FROM compat_mirrors WHERE domain = ?`, Domain).Scan(&digest); err != nil {
		t.Fatalf("mirror record: %v", err)
	}
	if wantDigest := database.Digest(data); digest != wantDigest {
		t.Fatalf("digest = %q, want %q", digest, wantDigest)
	}
}

func TestStoreImportLegacyAdoptsCache(t *testing.T) {
	var count atomic.Int32
	s := newTestStore(t, testServer(t, &count))
	ctx := context.Background()
	now := time.Now().UTC()
	s.now = func() time.Time { return now }
	want := Catalog{FetchedAt: now, Items: map[string]Entry{
		"wushoutieyu":     {Name: "焰魂狂飙", Rarity: "S"},
		"shenhaihuixiang": {Name: "坠入深渊", Rarity: "A"},
	}}
	data, err := encodeCache(want)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(s.dir, cacheFile)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.ImportLegacy(ctx); err != nil {
		t.Fatalf("ImportLegacy: %v", err)
	}
	c, err := s.Catalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := count.Load(); got != 0 {
		t.Fatalf("a fresh imported cache made %d requests", got)
	}
	if len(c.Items) != len(want.Items) {
		t.Fatalf("imported cache = %+v, want %+v", c, want)
	}
	for key, entry := range want.Items {
		if c.Items[key] != entry {
			t.Fatalf("imported %q = %+v, want %+v", key, c.Items[key], entry)
		}
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != string(data) {
		t.Fatalf("the import rewrote the legacy file: %v", err)
	}
	ran, err := s.db.RunImport(ctx, Domain, ImportVersion, s.importLegacy)
	if err != nil || ran {
		t.Fatalf("second import = %v, %v, want skipped", ran, err)
	}
}

func TestStoreImportLegacyRejectsUnknownFormat(t *testing.T) {
	var count atomic.Int32
	s := newTestStore(t, testServer(t, &count))
	path := filepath.Join(s.dir, cacheFile)
	broken := []byte(`{"format":"other"}`)
	if err := os.WriteFile(path, broken, 0o600); err != nil {
		t.Fatal(err)
	}
	err := s.ImportLegacy(context.Background())
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("ImportLegacy = %v, want an error naming %s", err, path)
	}
	if after, err := os.ReadFile(path); err != nil || string(after) != string(broken) {
		t.Fatalf("a failed import rewrote the file: %v", err)
	}
	if _, err := s.load(context.Background()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("load after a failed import = %v, want a cache miss", err)
	}
	// The network path is still usable and does not depend on the bad file.
	if _, err := s.Catalog(context.Background()); err != nil {
		t.Fatalf("Catalog after a failed import: %v", err)
	}
}
