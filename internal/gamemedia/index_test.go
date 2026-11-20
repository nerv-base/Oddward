// SPDX-License-Identifier: GPL-3.0-or-later
package gamemedia

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/database"
)

// TestHitsBatchLastUsedWrites proves the hit path is a batched single-column
// update: one write per flush, never a rewrite of the table or of untouched rows.
func TestHitsBatchLastUsedWrites(t *testing.T) {
	data := pngFixture(t, 4, 4)
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var clock atomic.Int64
	clock.Store(base.Unix())
	c := newTestCache(t, func(w http.ResponseWriter, r *http.Request) { w.Write(data) }, func() time.Time { return time.Unix(clock.Load(), 0) }, 0)
	ids := make([]string, 0, 21)
	for i := 1; i <= 21; i++ {
		ids = append(ids, strconv.Itoa(i))
		get(t, c, ids[i-1])
	}
	if c.usageUpdates.Load() != 0 || c.usageBatches.Load() != 0 {
		t.Fatalf("cold fetches wrote usage: batches=%d updates=%d", c.usageBatches.Load(), c.usageUpdates.Load())
	}
	clock.Add(60)
	for _, id := range ids[:20] {
		get(t, c, id)
	}
	if c.usageBatches.Load() != 0 {
		t.Fatalf("a hit flushed synchronously: %d batches", c.usageBatches.Load())
	}
	key, _ := KeyFor("1")
	before, ok := c.lookup(context.Background(), key)
	if !ok || !before.lastUsed.Equal(base) {
		t.Fatalf("row changed before a flush: %+v %v", before, ok)
	}
	if err := c.flushUsage(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := c.usageBatches.Load(); got != 1 {
		t.Fatalf("flush batches %d", got)
	}
	if got := c.usageUpdates.Load(); got != 20 {
		t.Fatalf("row updates %d", got)
	}
	hitAt := base.Add(60 * time.Second)
	for _, id := range ids[:20] {
		row, ok := c.lookup(context.Background(), mustKey(t, id))
		if !ok || !row.lastUsed.Equal(hitAt) {
			t.Fatalf("%s last_used %v", id, row.lastUsed)
		}
	}
	untouched, _ := c.lookup(context.Background(), mustKey(t, "21"))
	if !untouched.lastUsed.Equal(base) {
		t.Fatalf("untouched row rewritten: %v", untouched.lastUsed)
	}
	after, _ := c.lookup(context.Background(), key)
	if after.etag != before.etag || after.size != before.size || after.missing != before.missing || !after.expiresAt.Equal(before.expiresAt) {
		t.Fatalf("hit rewrote more than last_used: %+v", after)
	}
	var rows int
	if err := c.db.QueryRow(context.Background(), `SELECT COUNT(*) FROM asset_cache WHERE kind = ?`, assetKind).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 21 {
		t.Fatalf("rows %d", rows)
	}
	clock.Add(60)
	get(t, c, "1")
	if err := c.flushUsage(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := c.usageBatches.Load(); got != 2 {
		t.Fatalf("second flush batches %d", got)
	}
	if got := c.usageUpdates.Load(); got != 21 {
		t.Fatalf("second flush updates %d", got)
	}
}

func mustKey(t *testing.T, id string) Key {
	t.Helper()
	key, err := KeyFor(id)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

// TestEvictionStaysWithinBudgets checks both budgets and that only files this
// cache owns are deleted: an unindexed icon-shaped file and an unrelated file
// must survive.
func TestEvictionStaysWithinBudgets(t *testing.T) {
	data := pngFixture(t, 2, 2)
	var clock atomic.Int64
	clock.Store(1800000000)
	c := newTestCache(t, func(w http.ResponseWriter, r *http.Request) { w.Write(data) }, func() time.Time { return time.Unix(clock.Add(1), 0) }, int64(2*len(data)))
	if err := os.MkdirAll(c.dir, 0700); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(c.dir, "avatar-9.png")
	if err := os.WriteFile(orphan, data, 0600); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(c.dir, "keep.txt")
	if err := os.WriteFile(keep, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"1", "2", "3"} {
		get(t, c, id)
	}
	var rows, total int64
	if err := c.db.QueryRow(context.Background(), `SELECT COUNT(*), COALESCE(SUM(size), 0) FROM asset_cache WHERE kind = ?`, assetKind).Scan(&rows, &total); err != nil {
		t.Fatal(err)
	}
	if rows > 2 {
		t.Fatalf("rows %d exceed budget", rows)
	}
	if total > int64(2*len(data)) {
		t.Fatalf("bytes %d exceed budget", total)
	}
	if _, ok := c.lookup(context.Background(), mustKey(t, "1")); ok {
		t.Fatal("oldest row survived the budget")
	}
	if _, err := os.Stat(filepath.Join(c.dir, "avatar-1.png")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("oldest file survived the budget: %v", err)
	}
	for _, name := range []string{"avatar-2.png", "avatar-3.png", "avatar-9.png", "keep.txt"} {
		if _, err := os.Stat(filepath.Join(c.dir, name)); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if got, err := os.ReadFile(keep); err != nil || string(got) != "keep" {
		t.Fatal("unrelated file modified")
	}
}

func TestEvictionRespectsEntryLimit(t *testing.T) {
	data := pngFixture(t, 2, 2)
	var clock atomic.Int64
	clock.Store(1800000000)
	c := newTestCache(t, func(w http.ResponseWriter, r *http.Request) { w.Write(data) }, func() time.Time { return time.Unix(clock.Add(1), 0) }, 0)
	c.maxEntries = 2
	for _, id := range []string{"1", "2", "3", "4"} {
		get(t, c, id)
	}
	var rows int64
	if err := c.db.QueryRow(context.Background(), `SELECT COUNT(*) FROM asset_cache WHERE kind = ?`, assetKind).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Fatalf("rows %d", rows)
	}
	for _, id := range []string{"3", "4"} {
		if _, ok := c.lookup(context.Background(), mustKey(t, id)); !ok {
			t.Fatalf("%s missing", id)
		}
	}
	for _, id := range []string{"1", "2"} {
		if _, ok := c.lookup(context.Background(), mustKey(t, id)); ok {
			t.Fatalf("%s survived the entry limit", id)
		}
		if _, err := os.Stat(filepath.Join(c.dir, "avatar-"+id+".png")); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("avatar-%s.png survived the entry limit", id)
		}
	}
}

func TestRowWithoutFileIsRefetched(t *testing.T) {
	data := pngFixture(t, 2, 2)
	var hits atomic.Int32
	c := newTestCache(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write(data) }, nil, 0)
	get(t, c, "1")
	if err := os.Remove(filepath.Join(c.dir, "avatar-1.png")); err != nil {
		t.Fatal(err)
	}
	if got := get(t, c, "1"); !bytes.Equal(got, data) {
		t.Fatal("refetch changed bytes")
	}
	if hits.Load() != 2 {
		t.Fatalf("requests %d", hits.Load())
	}
	row, ok := c.lookup(context.Background(), mustKey(t, "1"))
	if !ok || row.missing || row.size != int64(len(data)) {
		t.Fatalf("row not repaired: %+v %v", row, ok)
	}
	if _, err := os.Stat(filepath.Join(c.dir, "avatar-1.png")); err != nil {
		t.Fatal(err)
	}
}

func TestUnindexedFileIsAdopted(t *testing.T) {
	data := pngFixture(t, 2, 2)
	var hits atomic.Int32
	c := newTestCache(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write(data) }, nil, 0)
	if err := os.MkdirAll(c.dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.dir, "avatar-7.png"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if got := get(t, c, "7"); !bytes.Equal(got, data) {
		t.Fatal("unindexed file not served")
	}
	if hits.Load() != 0 {
		t.Fatal("unindexed file went to the network")
	}
	row, ok := c.lookup(context.Background(), mustKey(t, "7"))
	if !ok || row.missing || row.size != int64(len(data)) {
		t.Fatalf("file not adopted: %+v %v", row, ok)
	}
	if !row.expiresAt.After(time.Now().Add(30 * time.Minute)) {
		t.Fatalf("adopted file already stale: %v", row.expiresAt)
	}
	if _, err := os.Stat(filepath.Join(c.dir, "avatar-7.png")); err != nil {
		t.Fatal(err)
	}
}

// A database that is gone must degrade to "no index": valid bytes on disk are
// still served.
func TestBrokenDatabaseStillServesDiskFile(t *testing.T) {
	data := pngFixture(t, 2, 2)
	var hits atomic.Int32
	c := newTestCache(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write(data) }, nil, 0)
	if err := os.MkdirAll(c.dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.dir, "avatar-5.png"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := c.db.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := c.Get(context.Background(), "5")
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("closed database broke delivery: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatal("closed database caused a fetch")
	}
}

// TestCorruptRowCannotRedirectReads stores a row whose path points outside the
// cache. The row's file columns are advisory: reads only use the validated key.
func TestCorruptRowCannotRedirectReads(t *testing.T) {
	data := pngFixture(t, 2, 2)
	var hits atomic.Int32
	c := newTestCache(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write(data) }, nil, 0)
	if err := os.MkdirAll(c.dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.dir, "avatar-1.png"), data, 0600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.png")
	if err := os.WriteFile(outside, []byte("not an icon"), 0600); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	_, err := c.db.Exec(context.Background(), `INSERT INTO asset_cache (kind, key, path, size, digest, source_url, etag, last_modified, fetched_at, expires_at, last_used, status, detail) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'ok', '')`,
		assetKind, "avatar-1.png", outside, int64(len(data)), "", "", "", "", database.Stamp(now), database.Stamp(now.Add(time.Hour)), database.Stamp(now))
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.Get(context.Background(), "1")
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("row path redirected the read: %v", err)
	}
	if hits.Load() != 0 {
		t.Fatal("corrupt row caused a fetch")
	}
	if b, err := os.ReadFile(outside); err != nil || string(b) != "not an icon" {
		t.Fatal("outside file changed")
	}
}
