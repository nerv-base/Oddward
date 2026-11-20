// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/database/dbtest"
	"github.com/nerv-base/Oddward/internal/gamemedia"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// benchIconSource adapts the real game-media cache to the view's icon source.
// The benchmark lives in package launcher, which may not import
// internal/launcher/system, so it wraps the cache directly.
type benchIconSource struct {
	cache *gamemedia.Cache
}

func (s benchIconSource) Image(ctx context.Context, id string) ([]byte, error) {
	return s.cache.Get(ctx, id)
}

// offlineTransport makes any network use fail immediately: the benchmark claims
// the disk cache is warm, so no request may leave the process.
type offlineTransport struct{}

func (offlineTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("benchmark: network disabled")
}

func benchWishPNG(b *testing.B) []byte {
	b.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, 128, 128))
	for y := 0; y < 128; y++ {
		for x := 0; x < 128; x++ {
			picture.Set(x, y, color.RGBA{R: uint8(x * 2), G: uint8(y * 2), B: 128, A: 255})
		}
	}
	var buffer bytes.Buffer
	if err := png.Encode(&buffer, picture); err != nil {
		b.Fatal(err)
	}
	return buffer.Bytes()
}

// benchIconCache warms twenty indexed icons on disk through a TLS server, then
// returns a cache over the same directory whose transport always fails.
func benchIconCache(b *testing.B) (*gamemedia.Cache, []string) {
	b.Helper()
	data := benchWishPNG(b)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(data) }))
	b.Cleanup(server.Close)
	db := dbtest.Open(b)
	dir := b.TempDir()
	options := gamemedia.Options{BaseURL: server.URL + "/root", Hosts: []string{strings.TrimPrefix(server.URL, "https://")}}
	warm, err := gamemedia.New(db, dir, gamemedia.Options{HTTP: server.Client(), BaseURL: options.BaseURL, Hosts: options.Hosts})
	if err != nil {
		b.Fatal(err)
	}
	ids := make([]string, 20)
	for i := range ids {
		ids[i] = strconv.Itoa(1000 + i)
	}
	for _, id := range ids {
		if _, err := warm.Get(context.Background(), id); err != nil {
			b.Fatalf("warm %s: %v", id, err)
		}
	}
	offline, err := gamemedia.New(db, dir, gamemedia.Options{HTTP: &http.Client{Transport: offlineTransport{}}, BaseURL: options.BaseURL, Hosts: options.Hosts})
	if err != nil {
		b.Fatal(err)
	}
	return offline, ids
}

func benchIconShell(b *testing.B, cache *gamemedia.Cache) *Shell {
	b.Helper()
	s := NewShell(state.Model{Page: state.WishAnalysis})
	b.Cleanup(s.Dispose)
	s.WishImages = benchIconSource{cache}
	return s
}

// iconStageRecorder sums the per-icon stages the workers report, so the
// benchmark can print queue, disk, decode and delivery apart from the wall time.
type iconStageRecorder struct {
	mu       sync.Mutex
	icons    int
	thumbs   int
	queue    time.Duration
	disk     time.Duration
	decode   time.Duration
	delivery time.Duration
}

func (r *iconStageRecorder) record(t wishIconTrace) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.icons++
	if t.ThumbHit {
		r.thumbs++
	}
	r.queue += t.StartedAt.Sub(t.QueuedAt)
	r.disk += t.LoadedAt.Sub(t.StartedAt)
	r.decode += t.DecodedAt.Sub(t.LoadedAt)
	r.delivery += t.DeliveredAt.Sub(t.DecodedAt)
}

func (r *iconStageRecorder) take() (queue, disk, decode, delivery time.Duration, icons int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	queue, disk, decode, delivery, icons = r.queue, r.disk, r.decode, r.delivery, r.icons
	r.icons, r.thumbs, r.queue, r.disk, r.decode, r.delivery = 0, 0, 0, 0, 0, 0
	return queue, disk, decode, delivery, icons
}

// thumbsHit reports how many of the last recorded deliveries came from the
// decoded-thumbnail cache instead of the source.
func (r *iconStageRecorder) thumbsHit() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.thumbs
}

func wishIconCached(s *Shell, ids []string) int {
	count := 0
	for _, id := range ids {
		if s.wishIcons.cache[id] != nil {
			count++
		}
	}
	return count
}

// frameWishIcons runs one view frame: the shell syncs, then the paint asks for
// every id in order. It returns how many icons are ready.
func frameWishIcons(s *Shell, ids []string) int {
	s.syncWishImages()
	for _, id := range ids {
		s.wishIcon(id)
	}
	return wishIconCached(s, ids)
}

// BenchmarkIconFirstScreen20 measures one first screen: twenty distinct icons,
// cold bitmap cache, warm disk cache, no network, frames at 60 Hz because the
// view can only show a bitmap on a frame.
func BenchmarkIconFirstScreen20(b *testing.B) {
	cache, ids := benchIconCache(b)
	s := benchIconShell(b, cache)
	var recorder iconStageRecorder
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		s.wishIcons = wishIconMedia{trace: recorder.record}
		start := time.Now()
		ready := frameWishIcons(s, ids)
		next := time.Now()
		for ready < len(ids) {
			next = next.Add(16 * time.Millisecond)
			if wait := time.Until(next); wait > 0 {
				time.Sleep(wait)
			}
			ready = frameWishIcons(s, ids)
			if time.Since(start) > 5*time.Second {
				b.Fatalf("first screen stalled with %d of %d icons", ready, len(ids))
			}
		}
		wall := time.Since(start)
		queue, disk, decode, delivery, icons := recorder.take()
		if icons != len(ids) {
			b.Fatalf("recorded %d of %d deliveries", icons, len(ids))
		}
		b.ReportMetric(float64(wall)/float64(time.Millisecond), "wall-ms/screen")
		b.ReportMetric(float64(queue)/float64(icons), "queue-ns/icon")
		b.ReportMetric(float64(disk)/float64(icons), "disk-ns/icon")
		b.ReportMetric(float64(decode)/float64(icons), "decode-ns/icon")
		b.ReportMetric(float64(delivery)/float64(icons), "deliver-ns/icon")
	}
}

// BenchmarkIconCacheHit measures the repeat-hit path: bitmap LRU lookups, the
// cost the paint pays once every icon of the screen has been delivered.
func BenchmarkIconCacheHit(b *testing.B) {
	cache, ids := benchIconCache(b)
	s := benchIconShell(b, cache)
	s.wishIcons = wishIconMedia{}
	deadline := time.Now().Add(5 * time.Second)
	for wishIconCached(s, ids) < len(ids) {
		frameWishIcons(s, ids)
		if time.Now().After(deadline) {
			b.Fatal("warm-up screen did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for _, id := range ids {
			if s.wishIcon(id) == nil {
				b.Fatal("cache hit missing a bitmap")
			}
		}
	}
}

func benchIDs() []string {
	ids := make([]string, 20)
	for i := range ids {
		ids[i] = strconv.Itoa(1000 + i)
	}
	return ids
}

// benchNetworkCache returns a cache over a fresh directory and database, so the
// network benchmark pays for every icon instead of replaying an earlier run.
func benchNetworkCache(b *testing.B, server *httptest.Server) *gamemedia.Cache {
	b.Helper()
	db := dbtest.Open(b)
	cache, err := gamemedia.New(db, b.TempDir(), gamemedia.Options{
		HTTP:    server.Client(),
		BaseURL: server.URL + "/root",
		Hosts:   []string{strings.TrimPrefix(server.URL, "https://")},
	})
	if err != nil {
		b.Fatal(err)
	}
	return cache
}

// measureFirstScreen paints one screen until every icon is delivered, pacing
// frames at cadence like the view does. It replaces the shell's icon state so
// every call starts from an empty bitmap cache.
func measureFirstScreen(b *testing.B, s *Shell, ids []string, cadence time.Duration) (time.Duration, *iconStageRecorder) {
	b.Helper()
	recorder := &iconStageRecorder{}
	s.wishIcons = wishIconMedia{trace: recorder.record}
	start := time.Now()
	ready := frameWishIcons(s, ids)
	next := time.Now()
	for ready < len(ids) {
		next = next.Add(cadence)
		if wait := time.Until(next); wait > 0 {
			time.Sleep(wait)
		}
		ready = frameWishIcons(s, ids)
		if time.Since(start) > 10*time.Second {
			b.Fatalf("first screen stalled with %d of %d icons", ready, len(ids))
		}
	}
	return time.Since(start), recorder
}

// BenchmarkIconFirstScreen20Network measures the same screen with an empty disk
// cache served over loopback TLS. This is the network stage the other
// benchmarks deliberately avoid, timed apart from decode and delivery.
func BenchmarkIconFirstScreen20Network(b *testing.B) {
	data := benchWishPNG(b)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(data) }))
	b.Cleanup(server.Close)
	ids := benchIDs()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		s := benchIconShell(b, benchNetworkCache(b, server))
		wall, recorder := measureFirstScreen(b, s, ids, 16*time.Millisecond)
		queue, fetch, decode, _, icons := recorder.take()
		b.ReportMetric(float64(wall)/float64(time.Millisecond), "wall-ms/screen")
		b.ReportMetric(float64(queue)/float64(icons), "queue-ns/icon")
		b.ReportMetric(float64(fetch)/float64(icons), "fetch-ns/icon")
		b.ReportMetric(float64(decode)/float64(icons), "decode-ns/icon")
	}
}

// BenchmarkIconThumbHit measures the restart path: the bitmap cache is empty,
// but the thumbnails a previous run decoded are on disk, so the source is never
// asked and nothing is decoded again.
func BenchmarkIconThumbHit(b *testing.B) {
	cache, ids := benchIconCache(b)
	dataDir := b.TempDir()
	warm := benchIconShell(b, cache)
	warm.DataDir = dataDir
	deadline := time.Now().Add(10 * time.Second)
	for wishIconCached(warm, ids) < len(ids) {
		frameWishIcons(warm, ids)
		if time.Now().After(deadline) {
			b.Fatal("warm-up screen did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	entries, err := os.ReadDir(filepath.Join(dataDir, "cache", "wish-thumbs"))
	if err != nil || len(entries) != len(ids) {
		b.Fatalf("thumbnail directory holds %d entries (%v), want %d", len(entries), err, len(ids))
	}
	s := benchIconShell(b, cache)
	s.DataDir = dataDir
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		wall, recorder := measureFirstScreen(b, s, ids, 16*time.Millisecond)
		hits := recorder.thumbsHit()
		queue, thumb, decode, _, icons := recorder.take()
		if hits != icons {
			b.Fatalf("%d of %d icons did not come from the thumbnail cache", icons-hits, icons)
		}
		b.ReportMetric(float64(wall)/float64(time.Millisecond), "wall-ms/screen")
		b.ReportMetric(float64(queue)/float64(icons), "queue-ns/icon")
		b.ReportMetric(float64(thumb)/float64(icons), "thumb-ns/icon")
		b.ReportMetric(float64(decode)/float64(icons), "decode-ns/icon")
	}
}
