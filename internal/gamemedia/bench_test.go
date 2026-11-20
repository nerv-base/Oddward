// SPDX-License-Identifier: GPL-3.0-or-later
package gamemedia

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/database/dbtest"
)

// benchCache seeds n icons with valid files on disk and an HTTP client that
// always reports 404, so a metadata lookup never touches the network.
func benchCache(b *testing.B, n int) *Cache {
	b.Helper()
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }))
	b.Cleanup(server.Close)
	c, err := New(dbtest.Open(b), b.TempDir(), Options{
		HTTP:    server.Client(),
		BaseURL: server.URL + "/root",
		Hosts:   []string{strings.TrimPrefix(server.URL, "https://")},
	})
	if err != nil {
		b.Fatal(err)
	}
	if err := os.MkdirAll(c.dir, 0700); err != nil {
		b.Fatal(err)
	}
	data := pngFixture(b, 64, 64)
	now := time.Now().UTC()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		key := Key{Kind: "avatar", ID: strconv.Itoa(i)}
		if err := os.WriteFile(filepath.Join(c.dir, key.filename()), data, 0600); err != nil {
			b.Fatal(err)
		}
		row := cachedIcon{size: int64(len(data)), fetchedAt: now, expiresAt: now.Add(time.Hour), lastUsed: now}
		if err := c.store(ctx, key, row); err != nil {
			b.Fatal(err)
		}
	}
	return c
}

func BenchmarkCacheLookup100(b *testing.B)  { benchmarkCacheLookup(b, 100) }
func BenchmarkCacheLookup1000(b *testing.B) { benchmarkCacheLookup(b, 1000) }

func BenchmarkCacheHit100(b *testing.B)  { benchmarkCacheHit(b, 100) }
func BenchmarkCacheHit1000(b *testing.B) { benchmarkCacheHit(b, 1000) }

// benchmarkCacheHit measures a whole cached read: metadata lookup, validated read
// of the icon and the batched access-time update. It is the like-for-like
// replacement for the pre-database index rewrite that dominated a hit.
func benchmarkCacheHit(b *testing.B, rows int) {
	c := benchCache(b, rows)
	key := Key{Kind: "avatar", ID: "0"}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		data, err := c.Get(ctx, key.ID)
		if err != nil {
			b.Fatal(err)
		}
		if len(data) == 0 {
			b.Fatal("empty icon")
		}
	}
}

// benchmarkCacheLookup measures the metadata read that every hit pays before
// any file is opened.
func benchmarkCacheLookup(b *testing.B, rows int) {
	c := benchCache(b, rows)
	key := Key{Kind: "avatar", ID: "0"}
	ctx := context.Background()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := c.lookup(ctx, key); !ok {
			b.Fatal("seeded row missing")
		}
	}
}
