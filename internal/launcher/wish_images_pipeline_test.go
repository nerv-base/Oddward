// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/gamemedia"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// pipelineSource is an instrumented icon source: it records every request and
// the highest number of concurrent calls it ever saw.
type pipelineSource struct {
	data    []byte
	err     error
	fail    atomic.Int32
	release chan struct{}

	mu       sync.Mutex
	requests []string
	active   atomic.Int32
	peak     atomic.Int32
}

func (p *pipelineSource) Image(ctx context.Context, id string) ([]byte, error) {
	current := p.active.Add(1)
	defer p.active.Add(-1)
	for {
		peak := p.peak.Load()
		if current <= peak || p.peak.CompareAndSwap(peak, current) {
			break
		}
	}
	p.mu.Lock()
	p.requests = append(p.requests, id)
	p.mu.Unlock()
	if p.release != nil {
		select {
		case <-p.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if p.fail.Load() > 0 {
		p.fail.Add(-1)
		return nil, p.err
	}
	return p.data, nil
}

func (p *pipelineSource) calls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.requests...)
}

type pipeHarness struct {
	s    *Shell
	src  *pipelineSource
	wake chan struct{}
}

func pipelinePNG(t *testing.T) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, 16, 16))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// newPipeHarness builds a shell on the wish page. Each paint runs the same
// order as the UI: sync first, then the view asks for icons.
func newPipeHarness(t *testing.T, src *pipelineSource) *pipeHarness {
	t.Helper()
	if src.data == nil {
		src.data = pipelinePNG(t)
	}
	s := NewShell(state.Model{Page: state.WishAnalysis})
	t.Cleanup(s.Dispose)
	h := &pipeHarness{s: s, src: src, wake: make(chan struct{}, 32)}
	s.WishImages = src
	s.RequestFrame = func() {
		select {
		case h.wake <- struct{}{}:
		default:
		}
	}
	return h
}

func (h *pipeHarness) paint(ids ...string) {
	h.s.syncWishImages()
	for _, id := range ids {
		h.s.wishIcon(id)
	}
}

func (h *pipeHarness) waitResult(t *testing.T) {
	t.Helper()
	select {
	case <-h.wake:
	case <-time.After(5 * time.Second):
		t.Fatal("icon worker did not report")
	}
}

func (h *pipeHarness) cached(ids []string) int {
	count := 0
	for _, id := range ids {
		if h.s.wishIcons.cache[id] != nil {
			count++
		}
	}
	return count
}

// waitFor polls a condition without hanging a test on a broken worker.
func waitIconCondition(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestWishIconLoadsAtMostFourAtOnce proves the scheduler never exceeds its
// bounded worker set, even when the view asks for more icons than it can load.
func TestWishIconLoadsAtMostFourAtOnce(t *testing.T) {
	src := &pipelineSource{release: make(chan struct{})}
	h := newPipeHarness(t, src)
	ids := []string{"1", "2", "3", "4", "5", "6"}
	h.paint(ids...)
	h.paint(ids...)
	waitIconCondition(t, "four loads to start", func() bool { return len(src.calls()) == 4 })
	if got := src.peak.Load(); got != 4 {
		t.Fatalf("peak concurrent loads %d", got)
	}
	h.paint(ids...)
	if got := len(src.calls()); got != 4 {
		t.Fatalf("extra work started while slots were full: %d", got)
	}
	close(src.release)
	deadline := time.Now().Add(5 * time.Second)
	for h.cached(ids) < len(ids) && time.Now().Before(deadline) {
		select {
		case <-h.wake:
		case <-time.After(5 * time.Millisecond):
		}
		h.paint(ids...)
	}
	if got := h.cached(ids); got != len(ids) {
		t.Fatalf("delivered %d of %d icons", got, len(ids))
	}
	if got := src.peak.Load(); got > 4 {
		t.Fatalf("peak concurrent loads %d exceeds the bound", got)
	}
}

// TestWishIconRequestsShareOneLoad drives repeated asks for the same id across
// frames and expects the source to see it once.
func TestWishIconRequestsShareOneLoad(t *testing.T) {
	src := &pipelineSource{release: make(chan struct{})}
	h := newPipeHarness(t, src)
	h.paint("7")
	h.paint("7")
	waitIconCondition(t, "one load", func() bool { return len(src.calls()) == 1 })
	h.paint("7")
	h.paint("7")
	if got := len(src.calls()); got != 1 {
		t.Fatalf("source saw %d requests for one icon", got)
	}
	close(src.release)
	h.waitResult(t)
	h.s.syncWishImages()
	if h.s.wishIcons.cache["7"] == nil {
		t.Fatal("delivered icon missing from the bitmap cache")
	}
}

// TestWishIconGenerationChangeDiscardsCanceledLoad switches roles mid-load: the
// stale result must neither reach the cache nor count as a failure.
func TestWishIconGenerationChangeDiscardsCanceledLoad(t *testing.T) {
	src := &pipelineSource{release: make(chan struct{})}
	h := newPipeHarness(t, src)
	h.paint("first")
	h.paint("first")
	waitIconCondition(t, "first load to start", func() bool { return len(src.calls()) == 1 })
	h.s.Model.Wish.Token++
	h.paint()
	h.waitResult(t)
	h.s.syncWishImages()
	if h.s.wishIcons.cache["first"] != nil {
		t.Fatal("cancelled load populated the bitmap cache")
	}
	if len(h.s.wishIcons.failures) != 0 {
		t.Fatal("cancellation was recorded as a failure")
	}
	if len(h.s.wishIcons.inflight) != 0 {
		t.Fatal("cancelled load kept its slot")
	}
}

// TestWishIconRetriesAfterTransientFailure checks the backoff: the failed icon
// is not asked again immediately, but a later attempt loads it.
func TestWishIconRetriesAfterTransientFailure(t *testing.T) {
	src := &pipelineSource{err: errors.New("transient"), fail: atomic.Int32{}}
	src.fail.Store(1)
	h := newPipeHarness(t, src)
	h.paint("9")
	h.paint("9")
	h.waitResult(t)
	h.s.syncWishImages()
	failure, ok := h.s.wishIcons.failures["9"]
	if !ok || failure.attempts != 1 {
		t.Fatalf("failure not recorded: %+v %v", failure, ok)
	}
	if time.Until(failure.retryAt) <= 0 {
		t.Fatal("retry scheduled in the past")
	}
	h.paint("9")
	h.paint("9")
	if got := len(src.calls()); got != 1 {
		t.Fatalf("failed icon was retried inside its backoff: %d", got)
	}
	// Age the retry instead of sleeping for the real backoff.
	h.s.wishIcons.failures["9"] = wishIconFailure{attempts: 1, retryAt: time.Now().Add(-time.Millisecond)}
	h.paint("9")
	h.paint("9")
	waitIconCondition(t, "retry to start", func() bool { return len(src.calls()) == 2 })
	h.waitResult(t)
	h.s.syncWishImages()
	if h.s.wishIcons.cache["9"] == nil {
		t.Fatal("retried icon was not delivered")
	}
	if len(h.s.wishIcons.failures) != 0 {
		t.Fatal("success left a failure entry")
	}
}

// TestWishIconNotFoundWaitsForNegativeTTL keeps a permanent 404 on the cache's
// negative TTL instead of the transient backoff.
func TestWishIconNotFoundWaitsForNegativeTTL(t *testing.T) {
	src := &pipelineSource{err: gamemedia.ErrNotFound, fail: atomic.Int32{}}
	src.fail.Store(1)
	h := newPipeHarness(t, src)
	h.paint("404")
	h.paint("404")
	h.waitResult(t)
	h.s.syncWishImages()
	failure, ok := h.s.wishIcons.failures["404"]
	if !ok {
		t.Fatal("not-found failure not recorded")
	}
	if remaining := time.Until(failure.retryAt); remaining < 23*time.Hour {
		t.Fatalf("not-found retried after %v", remaining)
	}
	h.paint("404")
	if got := len(src.calls()); got != 1 {
		t.Fatalf("not-found icon retried before its TTL: %d", got)
	}
}
