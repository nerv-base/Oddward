// SPDX-License-Identifier: GPL-3.0-or-later

package bwiki

import (
	"context"
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
	s, err := Open(t.TempDir(), srv.Client())
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
	if err := s.save(stale); err != nil {
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

func TestCacheRejectsUnknownFormat(t *testing.T) {
	s := newTestStore(t, testServer(t, new(atomic.Int32)))
	if err := os.WriteFile(filepath.Join(s.dir, cacheFile), []byte(`{"format":"other"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.load(); err == nil {
		t.Fatal("unknown cache format was accepted")
	}
}
