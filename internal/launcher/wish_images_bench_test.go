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
	queue    time.Duration
	disk     time.Duration
	decode   time.Duration
	delivery time.Duration
}

func (r *iconStageRecorder) record(t wishIconTrace) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.icons++
	r.queue += t.StartedAt.Sub(t.QueuedAt)
	r.disk += t.LoadedAt.Sub(t.StartedAt)
	r.decode += t.DecodedAt.Sub(t.LoadedAt)
	r.delivery += t.DeliveredAt.Sub(t.DecodedAt)
}

func (r *iconStageRecorder) take() (queue, disk, decode, delivery time.Duration, icons int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	queue, disk, decode, delivery, icons = r.queue, r.disk, r.decode, r.delivery, r.icons
	r.icons, r.queue, r.disk, r.decode, r.delivery = 0, 0, 0, 0, 0
	return queue, disk, decode, delivery, icons
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
