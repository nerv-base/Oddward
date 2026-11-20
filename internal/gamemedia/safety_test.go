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
	"sync/atomic"
	"testing"
	"time"
)

func TestTruncatedRevalidationFallsBack(t *testing.T) {
	data := pngFixture(t, 2, 2)
	var clock atomic.Int64
	clock.Store(1800000000)
	var hits atomic.Int32
	c := newTestCache(t, func(w http.ResponseWriter, r *http.Request) {
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
	waitFor(t, "truncated revalidation", func() bool { return hits.Load() == 2 })
	if b, err := os.ReadFile(filepath.Join(c.dir, "avatar-1.png")); err != nil || !bytes.Equal(b, data) {
		t.Fatalf("valid file replaced by truncated response: %v", err)
	}
	// A failed revalidation is retried only after the backoff, never per frame.
	if _, err := c.Get(context.Background(), "1"); err != nil {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Fatalf("failed revalidation retried immediately: %d", hits.Load())
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
	c := newTestCache(t, func(w http.ResponseWriter, r *http.Request) { w.Write(data) }, nil, 0)
	get(t, c, "1")
	source := filepath.Join(t.TempDir(), "source")
	if err := os.WriteFile(source, []byte("retained"), 0600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(c.dir, "avatar-1.png")
	if err := os.Remove(destination); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(source, destination); err != nil {
		t.Fatalf("hardlink the icon: %v", err)
	}
	if _, err := c.Get(context.Background(), "1"); err == nil {
		t.Fatal("hardlink accepted")
	}
	if got, err := os.ReadFile(source); err != nil || string(got) != "retained" {
		t.Fatal("hardlink target modified")
	}
}

func TestDiskGuardsRefuseSymlinkDirectory(t *testing.T) {
	dir := t.TempDir()
	linked := filepath.Join(dir, "linked")
	target := t.TempDir()
	if err := os.Symlink(target, linked); err != nil {
		t.Skipf("Windows symlink privilege unavailable: %v", err)
	}
	c, err := New(dbtestOpen(t), linked, Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Get(context.Background(), "1"); err == nil {
		t.Fatal("symlink directory accepted")
	}
}

func TestCallerOwnsReturnedBytes(t *testing.T) {
	data := pngFixture(t, 2, 2)
	c := newTestCache(t, func(w http.ResponseWriter, r *http.Request) { w.Write(data) }, nil, 0)
	b := get(t, c, "1")
	b[0] = 0
	if !bytes.Equal(get(t, c, "1"), data) {
		t.Fatal("caller modified cached bytes")
	}
}

func TestNegativeCacheSurvivesRestart(t *testing.T) {
	var hits atomic.Int32
	c := newTestCache(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1); http.NotFound(w, r) }, nil, 0)
	if _, err := c.Get(context.Background(), "1"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	fresh, err := New(c.db, c.dir, Options{HTTP: c.server.Client(), BaseURL: c.server.URL + "/root", Hosts: c.hosts})
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
			c := newTestCache(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(status) }, nil, 0)
			if _, err := c.Get(context.Background(), "1"); err == nil {
				t.Fatal("unexpected status accepted")
			}
			if _, err := os.Stat(filepath.Join(c.dir, "avatar-1.png")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unexpected status produced a file: %v", err)
			}
		})
	}
}

func TestInjectedRedirectPolicyCannotBypassAllowlist(t *testing.T) {
	var outsideHits atomic.Int32
	outside := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { outsideHits.Add(1); io.WriteString(w, "outside") }))
	defer outside.Close()
	c := newTestCache(t, func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, outside.URL, 302) }, nil, 0)
	if _, err := c.Get(context.Background(), "1"); err == nil {
		t.Fatal("off-host redirect accepted")
	}
	if outsideHits.Load() != 0 {
		t.Fatal("forbidden host reached")
	}
}
