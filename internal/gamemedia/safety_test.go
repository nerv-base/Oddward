// SPDX-License-Identifier: GPL-3.0-or-later
package gamemedia

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCorruptIndexOrphanImagesStillRespectBudget(t *testing.T) {
	data := pngFixture(t, 2, 2)
	c, _ := newTestCache(t, func(w http.ResponseWriter, r *http.Request) { w.Write(data) }, nil, int64(len(data)))
	if err := os.WriteFile(filepath.Join(c.dir, "avatar-9.png"), data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.dir, "keep.txt"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(c.dir, "index.json"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	get(t, c, "1")
	if _, err := os.Stat(filepath.Join(c.dir, "avatar-9.png")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("orphan ignored by disk budget: %v", err)
	}
	if got, err := os.ReadFile(filepath.Join(c.dir, "keep.txt")); err != nil || string(got) != "keep" {
		t.Fatal("unrelated file modified")
	}
}
func TestTruncatedRevalidationFallsBack(t *testing.T) {
	data := pngFixture(t, 2, 2)
	var clock atomic.Int64
	clock.Store(1800000000)
	var hits atomic.Int32
	c, _ := newTestCache(t, func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.Write(data)
			return
		}
		w.Header().Set("Content-Length", "1000")
		w.Write([]byte("short"))
	}, func() time.Time { return time.Unix(clock.Load(), 0) }, 0)
	get(t, c, "1")
	clock.Add(3601)
	got, err := c.Get(context.Background(), "1")
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("truncated network response should retain valid PNG: %v", err)
	}
}
func TestFreshness(t *testing.T) {
	for _, tc := range []struct {
		header string
		want   time.Duration
	}{{"", time.Hour}, {"max-age=0", time.Hour}, {"MAX-AGE=7200", 2 * time.Hour}, {"max-age=999999999999", 7 * 24 * time.Hour}, {"public, max-age=\"86400\"", 24 * time.Hour}, {"max-age=invalid", time.Hour}} {
		h := http.Header{}
		h.Set("Cache-Control", tc.header)
		if got := freshness(h); got != tc.want {
			t.Errorf("%q: %v", tc.header, got)
		}
	}
}
func TestDiskGuardsRefuseHardlinks(t *testing.T) {
	data := pngFixture(t, 2, 2)
	for _, target := range []string{"index.json", "avatar-1.png"} {
		t.Run(target, func(t *testing.T) {
			c, _ := newTestCache(t, func(w http.ResponseWriter, r *http.Request) { w.Write(data) }, nil, 0)
			get(t, c, "1")
			source := filepath.Join(t.TempDir(), "source")
			if err := os.WriteFile(source, []byte("retained"), 0600); err != nil {
				t.Fatal(err)
			}
			destination := filepath.Join(c.dir, target)
			if err := os.Remove(destination); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(source, destination); err != nil {
				t.Fatal(err)
			}
			if _, err := c.Get(context.Background(), "1"); err == nil {
				t.Fatal("hardlink accepted")
			}
			if got, err := os.ReadFile(source); err != nil || string(got) != "retained" {
				t.Fatal("hardlink target modified")
			}
		})
	}
}
func TestDiskGuardsRefuseSymlinkDirectory(t *testing.T) {
	dir := t.TempDir()
	linked := filepath.Join(dir, "linked")
	target := t.TempDir()
	if err := os.Symlink(target, linked); err != nil {
		t.Skipf("Windows symlink privilege unavailable: %v", err)
	}
	c, err := New(linked, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Get(context.Background(), "1"); err == nil {
		t.Fatal("symlink directory accepted")
	}
}
func TestIndexRejectsValidLookingDuplicateUnknownAndTrailingFields(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	valid := `[{"key":"avatar-1.png","etag":"","last_modified":"","fetched_at":"2026-01-01T00:00:00Z","expires_at":"2026-01-01T01:00:00Z","size":64,"last_used":"2026-01-01T00:00:00Z"}]`
	if entries, err := decodeIndex([]byte(valid)); err != nil || !entries["avatar-1.png"].FetchedAt.Equal(now) {
		t.Fatalf("valid schema: %v", err)
	}
	for _, bad := range []string{strings.Replace(valid, `"etag":""`, `"etag":"","etag":"duplicate"`, 1), strings.Replace(valid, `"etag":""`, `"etag":"","unknown":true`, 1), valid + ` {}`, strings.Replace(valid, "avatar-1.png", "../unrelated", 1), "null"} {
		if _, err := decodeIndex([]byte(bad)); err == nil {
			t.Errorf("invalid schema accepted: %s", bad)
		}
	}
}
func TestCallerOwnsReturnedBytes(t *testing.T) {
	data := pngFixture(t, 2, 2)
	c, _ := newTestCache(t, func(w http.ResponseWriter, r *http.Request) { w.Write(data) }, nil, 0)
	b := get(t, c, "1")
	b[0] = 0
	if !bytes.Equal(get(t, c, "1"), data) {
		t.Fatal("caller modified cached bytes")
	}
}
func TestNegativeCacheSurvivesRestart(t *testing.T) {
	var hits atomic.Int32
	c, s := newTestCache(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1); http.NotFound(w, r) }, nil, 0)
	if _, err := c.Get(context.Background(), "1"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	fresh, err := New(c.dir, Options{HTTP: s.Client(), BaseURL: s.URL + "/root", Hosts: c.hosts})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fresh.Get(context.Background(), "1"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if hits.Load() != 1 {
		t.Fatal("persistent negative cache missed")
	}
}
func TestUnexpected304AndBadResponsesDoNotBecomeIcons(t *testing.T) {
	for _, status := range []int{304, 500} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			c, _ := newTestCache(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }, nil, 0)
			if _, err := c.Get(context.Background(), "1"); err == nil {
				t.Fatal("unexpected status accepted")
			}
		})
	}
}
func TestInjectedRedirectPolicyCannotBypassAllowlist(t *testing.T) {
	var outsideHits atomic.Int32
	outside := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { outsideHits.Add(1); io.WriteString(w, "outside") }))
	defer outside.Close()
	c, _ := newTestCache(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, outside.URL, 302) }, nil, 0)
	if _, err := c.Get(context.Background(), "1"); err == nil {
		t.Fatal("off-host redirect accepted")
	}
	if outsideHits.Load() != 0 {
		t.Fatal("forbidden host reached")
	}
}
