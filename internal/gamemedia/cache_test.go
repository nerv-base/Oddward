// SPDX-License-Identifier: GPL-3.0-or-later
package gamemedia

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/database"
	"github.com/nerv-base/Oddward/internal/database/dbtest"
)

func pngFixture(t testing.TB, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func dbtestOpen(t testing.TB) *database.DB {
	t.Helper()
	return dbtest.Open(t)
}

// testCache bundles the cache with the database and source server a test drives.
type testCache struct {
	*Cache
	db     *database.DB
	server *httptest.Server
}

func newTestCache(t *testing.T, h http.HandlerFunc, now func() time.Time, budget int64) *testCache {
	t.Helper()
	server := httptest.NewTLSServer(h)
	t.Cleanup(server.Close)
	return newTestCacheAt(t, server, server.Client(), now, budget)
}

func newTestCacheAt(t *testing.T, server *httptest.Server, client *http.Client, now func() time.Time, budget int64) *testCache {
	t.Helper()
	db := dbtest.Open(t)
	c, err := New(db, t.TempDir(), Options{HTTP: client, BaseURL: server.URL + "/root", Hosts: []string{strings.TrimPrefix(server.URL, "https://")}, Now: now, MaxBytes: budget})
	if err != nil {
		t.Fatal(err)
	}
	return &testCache{Cache: c, db: db, server: server}
}

func get(t *testing.T, c *testCache, id string) []byte {
	t.Helper()
	b, err := c.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// waitFor polls a condition that depends on background work (revalidation) that
// the caller must not block on.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestKeyFor(t *testing.T) {
	for _, id := range []string{"0", "1003", "0123456789", "fork_a", "fork_a_b0"} {
		k, err := KeyFor(id)
		if err != nil || k.ID != id {
			t.Fatalf("%q: %+v %v", id, k, err)
		}
	}
	for _, id := range []string{"", "12345678901", "１２３", "a", "fork_", "fork_A", "fork_a.png", "fork_../a", strings.Repeat("1", 11), "fork_" + strings.Repeat("a", 49)} {
		if _, err := KeyFor(id); !errors.Is(err, ErrUnknownItem) {
			t.Fatalf("%q: %v", id, err)
		}
	}
	if _, err := New(dbtest.Open(t), "relative", Options{}); err == nil {
		t.Fatal("relative dir accepted")
	}
	if _, err := New(nil, t.TempDir(), Options{}); err == nil {
		t.Fatal("nil database accepted")
	}
}

func TestURLAndCacheHit(t *testing.T) {
	data := pngFixture(t, 4, 4)
	var hits atomic.Int32
	c := newTestCache(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/root/avatar/square/1003.PNG" && r.URL.Path != "/root/character/fork/fork_x.png" {
			t.Errorf("path %s", r.URL.Path)
		}
		if r.UserAgent() != "oddward-launcher (game record icons)" || r.Header.Get("Accept") != "image/png" {
			t.Errorf("headers %v", r.Header)
		}
		w.Write(data)
	}, nil, 0)
	for _, id := range []string{"1003", "1003", "fork_x", "fork_x"} {
		if b := get(t, c, id); !bytes.Equal(b, data) {
			t.Fatal("bytes changed")
		}
	}
	if hits.Load() != 2 {
		t.Fatalf("requests %d", hits.Load())
	}
	for _, name := range []string{"avatar-1003.png", "fork-fork_x.png"} {
		if _, err := os.Stat(filepath.Join(c.dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"1003", "fork_x"} {
		key, _ := KeyFor(id)
		row, ok := c.lookup(context.Background(), key)
		if !ok || row.missing || row.size != int64(len(data)) {
			t.Fatalf("%s row: %+v %v", id, row, ok)
		}
	}
}

func TestValidationAndRedirects(t *testing.T) {
	data := pngFixture(t, 2, 2)
	for _, mode := range []string{"offhost", "http", "userinfo", "loop", "oversize", "jpeg", "bigside", "empty", "broken"} {
		t.Run(mode, func(t *testing.T) {
			var hits atomic.Int32
			c := newTestCache(t, func(w http.ResponseWriter, r *http.Request) {
				hits.Add(1)
				switch mode {
				case "offhost":
					http.Redirect(w, r, "https://evil.example/x", 302)
				case "http":
					http.Redirect(w, r, "http://"+r.Host+"/x", 302)
				case "userinfo":
					http.Redirect(w, r, "https://u:p@"+r.Host+"/x", 302)
				case "loop":
					http.Redirect(w, r, "/loop", 302)
				case "oversize":
					w.Write(bytes.Repeat([]byte("x"), (1<<20)+1))
				case "jpeg":
					var b bytes.Buffer
					jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, 1, 1)), nil)
					w.Write(b.Bytes())
				case "bigside":
					w.Write(pngFixture(t, 1025, 1))
				case "empty":
				case "broken":
					w.Write(data[:20])
				}
			}, nil, 0)
			if _, err := c.Get(context.Background(), "1"); err == nil {
				t.Fatal("unsafe response accepted")
			}
			if mode == "loop" && hits.Load() != 4 {
				t.Fatalf("redirect hits %d", hits.Load())
			}
		})
	}
	t.Run("base host", func(t *testing.T) {
		c, err := New(dbtest.Open(t), t.TempDir(), Options{BaseURL: "https://evil.example/root"})
		if err == nil {
			_, err = c.Get(context.Background(), "1")
		}
		if err == nil {
			t.Fatal("off-host base accepted")
		}
	})
	t.Run("same host redirect", func(t *testing.T) {
		c := newTestCache(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/final" {
				http.Redirect(w, r, "/final", 302)
				return
			}
			w.Write(data)
		}, nil, 0)
		if !bytes.Equal(get(t, c, "1"), data) {
			t.Fatal("redirect bytes")
		}
	})
}

func TestRevalidateAndFallback(t *testing.T) {
	data := pngFixture(t, 2, 2)
	var clock atomic.Int64
	clock.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix())
	now := func() time.Time { return time.Unix(clock.Load(), 0) }
	var hits atomic.Int32
	c := newTestCache(t, func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		w.Header().Set("ETag", `"v1"`)
		w.Header().Set("Last-Modified", "Wed, 01 Jan 2025 00:00:00 GMT")
		w.Header().Set("Cache-Control", "public, max-age=1")
		if n > 1 {
			if r.Header.Get("If-None-Match") != `"v1"` || r.Header.Get("If-Modified-Since") == "" {
				t.Errorf("missing validators: %v", r.Header)
			}
			w.WriteHeader(304)
			return
		}
		w.Write(data)
	}, now, 0)
	get(t, c, "1")
	clock.Add(3599)
	get(t, c, "1")
	if hits.Load() != 1 {
		t.Fatal("min freshness not clamped")
	}
	clock.Add(2)
	if !bytes.Equal(get(t, c, "1"), data) {
		t.Fatal("expired entry stopped serving")
	}
	waitFor(t, "background revalidation", func() bool { return hits.Load() == 2 })
	key, _ := KeyFor("1")
	revalidatedAt := time.Unix(clock.Load(), 0)
	waitFor(t, "revalidation commit", func() bool {
		row, ok := c.lookup(context.Background(), key)
		return ok && row.expiresAt.After(revalidatedAt.Add(time.Minute))
	})
	clock.Add(3599)
	get(t, c, "1")
	if hits.Load() != 2 {
		t.Fatal("revalidated entry was not fresh")
	}
	clock.Add(2)
	c.server.Close()
	if !bytes.Equal(get(t, c, "1"), data) {
		t.Fatal("fallback changed bytes")
	}
	reopen, err := New(c.db, c.dir, Options{HTTP: c.server.Client(), BaseURL: c.server.URL + "/root", Hosts: c.hosts, Now: now})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(get(t, &testCache{Cache: reopen, db: c.db, server: c.server}, "1"), data) {
		t.Fatal("disk entry not reused")
	}
}

func TestExpiredEntryServedWithoutWaitingForRevalidation(t *testing.T) {
	data := pngFixture(t, 2, 2)
	var clock atomic.Int64
	clock.Store(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Unix())
	now := func() time.Time { return time.Unix(clock.Load(), 0) }
	started := make(chan struct{})
	release := make(chan struct{})
	var hits atomic.Int32
	c := newTestCache(t, func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			w.Header().Set("Cache-Control", "max-age=1")
			w.Write(data)
			return
		}
		close(started)
		<-release
		w.Write(data)
	}, now, 0)
	get(t, c, "1")
	clock.Add(3601)
	begin := time.Now()
	if got := get(t, c, "1"); !bytes.Equal(got, data) {
		t.Fatal("expired entry not served")
	}
	if elapsed := time.Since(begin); elapsed > 250*time.Millisecond {
		t.Fatalf("delivery waited for revalidation: %v", elapsed)
	}
	<-started
	close(release)
}

func TestNotFoundNegativeCache(t *testing.T) {
	var clock atomic.Int64
	clock.Store(1800000000)
	var hits atomic.Int32
	c := newTestCache(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1); http.NotFound(w, r) }, func() time.Time { return time.Unix(clock.Load(), 0) }, 0)
	for i := 0; i < 2; i++ {
		if _, err := c.Get(context.Background(), "1"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("%v", err)
		}
	}
	if hits.Load() != 1 {
		t.Fatal("negative cache missed")
	}
	clock.Add(86401)
	if _, err := c.Get(context.Background(), "1"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if hits.Load() != 2 {
		t.Fatal("negative cache did not expire")
	}
}

func TestBadFileDownloadsAgain(t *testing.T) {
	data := pngFixture(t, 2, 2)
	var hits atomic.Int32
	c := newTestCache(t, func(w http.ResponseWriter, r *http.Request) { hits.Add(1); w.Write(data) }, nil, 0)
	get(t, c, "1")
	if err := os.WriteFile(filepath.Join(c.dir, "avatar-1.png"), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(get(t, c, "1"), data) || hits.Load() != 2 {
		t.Fatal("corrupt PNG reused")
	}
}

func TestLRU(t *testing.T) {
	data := pngFixture(t, 2, 2)
	var clock atomic.Int64
	clock.Store(1800000000)
	c := newTestCache(t, func(w http.ResponseWriter, r *http.Request) { w.Write(data) }, func() time.Time { return time.Unix(clock.Add(1), 0) }, int64(2*len(data)))
	get(t, c, "1")
	get(t, c, "2")
	get(t, c, "1")
	get(t, c, "3")
	if _, err := os.Stat(filepath.Join(c.dir, "avatar-2.png")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("LRU victim exists: %v", err)
	}
	for _, id := range []string{"1", "3"} {
		if _, err := os.Stat(filepath.Join(c.dir, "avatar-"+id+".png")); err != nil {
			t.Fatal(err)
		}
		key, _ := KeyFor(id)
		if _, ok := c.lookup(context.Background(), key); !ok {
			t.Fatalf("%s row dropped", id)
		}
	}
}

func TestSingleflightAndCancellation(t *testing.T) {
	data := pngFixture(t, 2, 2)
	entered := make(chan struct{})
	release := make(chan struct{})
	var hits atomic.Int32
	c := newTestCache(t, func(w http.ResponseWriter, r *http.Request) {
		if hits.Add(1) == 1 {
			close(entered)
		}
		<-release
		w.Write(data)
	}, nil, 0)
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	go func() { _, err := c.Get(ctx, "1"); first <- err }()
	<-entered
	cancel()
	if err := <-first; !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b, err := c.Get(context.Background(), "1")
			if err != nil || !bytes.Equal(b, data) {
				t.Errorf("shared: %v", err)
			}
		}()
	}
	close(release)
	wg.Wait()
	if hits.Load() != 1 {
		t.Fatalf("requests %d", hits.Load())
	}
	cancelled, stop := context.WithCancel(context.Background())
	stop()
	if _, err := c.Get(cancelled, "1"); !errors.Is(err, context.Canceled) {
		t.Fatalf("pre-cancelled: %v", err)
	}
}

func TestDownloadConcurrency(t *testing.T) {
	data := pngFixture(t, 2, 2)
	var active, max atomic.Int32
	arrived := make(chan struct{}, 8)
	release := make(chan struct{})
	c := newTestCache(t, func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		for old := max.Load(); n > old && !max.CompareAndSwap(old, n); old = max.Load() {
		}
		arrived <- struct{}{}
		<-release
		active.Add(-1)
		w.Write(data)
	}, nil, 0)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for _, id := range []string{"1", "2", "3", "4", "5", "6", "7", "8"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			if _, err := c.Get(context.Background(), id); err != nil {
				errs <- err
			}
		}(id)
	}
	for i := 0; i < 4; i++ {
		<-arrived
	}
	close(release)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if max.Load() > 4 {
		t.Fatalf("active downloads %d", max.Load())
	}
}

// flakyTransport fails the first failures requests and then delegates to the
// real test transport, so one attempt can fail for a non-HTTP reason.
type flakyTransport struct {
	failures atomic.Int32
	next     http.RoundTripper
}

func (f *flakyTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if f.failures.Add(-1) >= 0 {
		return nil, errors.New("connection reset")
	}
	return f.next.RoundTrip(r)
}

func TestTransientFailureRetriedOnNextAttempt(t *testing.T) {
	data := pngFixture(t, 2, 2)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(data) }))
	t.Cleanup(server.Close)
	transport := &flakyTransport{next: server.Client().Transport}
	transport.failures.Store(1)
	c := newTestCacheAt(t, server, &http.Client{Transport: transport}, nil, 0)

	if _, err := c.Get(context.Background(), "1"); err == nil {
		t.Fatal("first attempt should fail")
	}
	if _, err := c.Get(context.Background(), "1"); err != nil {
		t.Fatalf("retry after transient failure: %v", err)
	}
	key, _ := KeyFor("1")
	row, ok := c.lookup(context.Background(), key)
	if !ok || row.missing || row.fetchedAt.IsZero() {
		t.Fatalf("transient failure became a negative entry: %+v %v", row, ok)
	}
}
