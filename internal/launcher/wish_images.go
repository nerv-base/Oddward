// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"bytes"
	"context"
	"errors"
	"image"
	"path/filepath"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nerv-base/Oddward/internal/gamemedia"
	"github.com/nerv-base/Oddward/internal/launcher/state"
	"golang.org/x/image/draw"
)

// wishIconWorkers bounds in-flight icon loads. The game-media cache already
// allows four concurrent fetches, and one worker per slot keeps every download
// paired with exactly one decode of at most a 96-pixel RGBA. Four saturates the
// first screen without letting a scroll pile up work; see
// BenchmarkIconFirstScreen20.
const wishIconWorkers = 4

// Transient icon failures back off, so a flaky source is retried later in the
// same session instead of being cached as failed forever. A 404 is a permanent
// answer from the source and waits for the cache's own negative TTL.
const (
	wishIconRetryBase      = time.Second
	wishIconRetryMax       = 30 * time.Second
	wishIconNotFoundRetry  = 24 * time.Hour
	wishIconRetryDoublings = 5 // attempts whose delay doubles before clamping
)

// wishIconTrace reports one delivered load to the benchmark. The shell leaves
// trace nil; production code pays one nil check per icon. ThumbHit means the
// pixels came from the decoded-thumbnail cache, so StartedAt and LoadedAt
// bracket one small read instead of a source fetch.
type wishIconTrace struct {
	ID          string
	ThumbHit    bool
	QueuedAt    time.Time
	StartedAt   time.Time
	LoadedAt    time.Time
	DecodedAt   time.Time
	DeliveredAt time.Time
}

type wishIconResult struct {
	id         string
	generation uint64
	pixels     *image.RGBA
	err        error
	cancelled  bool
	thumbHit   bool
	queuedAt   time.Time
	startedAt  time.Time
	loadedAt   time.Time
	decodedAt  time.Time
}

type wishIconLoad struct {
	cancel context.CancelFunc
}

type wishIconFailure struct {
	attempts int
	retryAt  time.Time
}

// All maps, bitmaps, queue, retry and LRU entries belong to the UI thread.
// Workers only read their arguments and write their own result; at most
// wishIconWorkers of them exist, so decode memory stays bounded even if a
// source ignores cancellation.
type wishIconMedia struct {
	cache    map[string]*ui.Bitmap
	recent   []string
	wanted   []string
	queued   map[string]bool
	failures map[string]wishIconFailure
	inflight map[string]*wishIconLoad
	painted  map[string]bool // ids the view asked for during the last paint
	// pinned holds prefetched ids the view has not painted yet. The first paint
	// after a load runs one frame later, so without the pin those loads would be
	// cancelled as offscreen before they were ever shown.
	pinned   map[string]bool
	queuedAt map[string]time.Time

	generation uint64
	active     bool // any load in flight; the page reads this for invalidation
	out        chan wishIconResult

	// thumbs caches decoded icons across restarts; nil when DataDir is unset.
	thumbs *gamemedia.ThumbCache

	trace func(wishIconTrace)
}

func (s *Shell) wishIconsEnabled() bool {
	return s.ctx != nil && s.ctx.Err() == nil && !s.Model.Closed && s.Model.Page == state.WishAnalysis && s.Model.Overlay == state.NoOverlay && s.WishImages != nil
}

// wishThumbCache returns the decoded-icon store, creating it on first use. It
// is nil when the shell has no data directory, which turns the disk cache off.
func (s *Shell) wishThumbCache() *gamemedia.ThumbCache {
	b := &s.wishIcons
	if b.thumbs == nil && s.DataDir != "" {
		b.thumbs = gamemedia.NewThumbCache(filepath.Join(s.DataDir, "cache", "wish-thumbs"))
	}
	return b.thumbs
}

// syncWishImages runs once per frame, before painting. It drains finished loads
// on the UI thread, cancels loads whose icon has left the view, and starts new
// loads in paint order. No I/O, decode or SQL runs here.
func (s *Shell) syncWishImages() {
	b := &s.wishIcons
	enabled := s.wishIconsEnabled()
	generation := s.Model.Wish.Token
	if b.generation != generation {
		// Switching roles invalidates queued work and every result still in
		// flight; their slots stay occupied until they report, so decode
		// concurrency never exceeds wishIconWorkers.
		b.generation = generation
		b.cancelAll()
		b.wanted = nil
		b.queued = nil
		b.pinned = nil
	}
	b.drain(enabled)
	if !enabled {
		b.cancelAll()
		b.wanted = nil
		b.queued = nil
		b.painted = nil
		b.pinned = nil
		return
	}
	if b.painted != nil {
		// The previous paint did not ask for this icon and no prefetch pinned
		// it: it left the viewport.
		for id, load := range b.inflight {
			if !b.painted[id] && !b.pinned[id] && load.cancel != nil {
				load.cancel()
			}
		}
	}
	b.painted = make(map[string]bool)
	wanted := b.wanted
	b.wanted = nil
	b.queued = nil
	for _, id := range wanted {
		if len(b.inflight) >= wishIconWorkers {
			return
		}
		if b.cache[id] != nil || b.inflight[id] != nil {
			continue
		}
		if failure, ok := b.failures[id]; ok && time.Now().Before(failure.retryAt) {
			continue
		}
		s.startWishIcon(id)
	}
}

// drain hands every finished result to the UI state. A stale, cancelled or
// failed load frees its slot without touching the bitmap cache.
func (b *wishIconMedia) drain(enabled bool) {
	for b.out != nil {
		select {
		case r := <-b.out:
			delete(b.inflight, r.id)
			b.active = len(b.inflight) > 0
			if r.cancelled || r.generation != b.generation || !enabled {
				delete(b.queuedAt, r.id)
				continue
			}
			if r.err != nil {
				b.fail(r.id, r.err)
				continue
			}
			if r.pixels == nil {
				continue
			}
			delete(b.failures, r.id)
			b.remember(r.id, ui.NewBitmap(r.pixels))
			if b.trace != nil {
				b.trace(wishIconTrace{ID: r.id, ThumbHit: r.thumbHit, QueuedAt: r.queuedAt, StartedAt: r.startedAt, LoadedAt: r.loadedAt, DecodedAt: r.decodedAt, DeliveredAt: time.Now()})
			}
		default:
			return
		}
	}
}

// fail schedules the next attempt. Attempts grow the backoff so a source that
// keeps failing is asked rarely, and a later session frame still retries it.
func (b *wishIconMedia) fail(id string, err error) {
	failure := b.failures[id]
	failure.attempts++
	delay := wishIconRetryBase
	for i := 1; i < failure.attempts && i < wishIconRetryDoublings; i++ {
		delay *= 2
	}
	if delay > wishIconRetryMax {
		delay = wishIconRetryMax
	}
	if errors.Is(err, gamemedia.ErrNotFound) || errors.Is(err, gamemedia.ErrUnknownItem) {
		delay = wishIconNotFoundRetry
	}
	failure.retryAt = time.Now().Add(delay)
	if b.failures == nil {
		b.failures = make(map[string]wishIconFailure)
	}
	b.failures[id] = failure
}

// startWishIcon launches one bounded load. The worker fetches and decodes; the
// UI thread never blocks on it because results arrive through b.out.
func (s *Shell) startWishIcon(id string) {
	b := &s.wishIcons
	if b.out == nil {
		b.out = make(chan wishIconResult, wishIconWorkers)
	}
	if b.inflight == nil {
		b.inflight = make(map[string]*wishIconLoad)
	}
	ctx, cancel := context.WithCancel(s.ctx)
	b.inflight[id] = &wishIconLoad{cancel: cancel}
	b.active = true
	queuedAt := time.Now()
	if b.trace != nil {
		if b.queuedAt == nil {
			b.queuedAt = make(map[string]time.Time)
		}
		if queued := b.queuedAt[id]; !queued.IsZero() {
			queuedAt = queued
		}
		delete(b.queuedAt, id)
	}
	source, thumbs, out, life, wake, generation := s.WishImages, s.wishThumbCache(), b.out, s.ctx, s.RequestFrame, b.generation
	go func() {
		startedAt := time.Now()
		var pixels *image.RGBA
		var err error
		// A saved thumbnail is the whole point of the disk cache: a restart
		// loads pixels the previous run already decoded instead of fetching and
		// resampling the source again. The file is validated by the cache, so a
		// miss here just falls through to the source path.
		thumbHit := false
		if thumbs != nil {
			pixels = thumbs.Load(id, wishIconDecodeEdge)
			thumbHit = pixels != nil
		}
		loadedAt := time.Now()
		decodedAt := loadedAt
		if !thumbHit {
			var data []byte
			data, err = source.Image(ctx, id)
			loadedAt = time.Now()
			if err == nil && ctx.Err() == nil {
				pixels, err = decodeWishIcon(data)
			}
			decodedAt = time.Now()
			if err == nil && thumbs != nil {
				_ = thumbs.Save(id, wishIconDecodeEdge, pixels)
			}
		}
		cancelled := ctx.Err() != nil
		if cancelled {
			pixels, err = nil, nil
		}
		if life.Err() != nil {
			return
		}
		select {
		case out <- wishIconResult{id: id, generation: generation, pixels: pixels, err: err, cancelled: cancelled, thumbHit: thumbHit, queuedAt: queuedAt, startedAt: startedAt, loadedAt: loadedAt, decodedAt: decodedAt}:
			if wake != nil && life.Err() == nil {
				wake()
			}
		case <-life.Done():
		}
	}()
}

func (b *wishIconMedia) cancelAll() {
	for _, load := range b.inflight {
		if load.cancel != nil {
			load.cancel()
		}
	}
}

// resetWishIcons drops queued and in-flight icons after the visible archive
// changed. It records the new generation first, so the syncWishImages in the
// same frame keeps the prefetch that follows instead of wiping it.
func (s *Shell) resetWishIcons() {
	b := &s.wishIcons
	b.generation = s.Model.Wish.Token
	b.cancelAll()
	b.wanted = nil
	b.queued = nil
	b.pinned = nil
}

// wishIcon is called by the paint in viewport order. It returns a cached bitmap
// or queues the id; repeated calls for one id in a frame are free.
func (s *Shell) wishIcon(id string) *ui.Bitmap {
	delete(s.wishIcons.pinned, id)
	return s.requestWishIcon(id, false)
}

// prefetchWishIcon warms an icon the first screen is about to paint. The pin
// keeps its load alive until the paint has asked for it once; see wishIconMedia.
func (s *Shell) prefetchWishIcon(id string) *ui.Bitmap {
	return s.requestWishIcon(id, true)
}

func (s *Shell) requestWishIcon(id string, pinned bool) *ui.Bitmap {
	b := &s.wishIcons
	if b.painted == nil {
		b.painted = make(map[string]bool)
	}
	b.painted[id] = true
	if bitmap := b.cache[id]; bitmap != nil {
		b.remember(id, bitmap)
		return bitmap
	}
	if !s.wishIconsEnabled() || b.queued[id] || b.inflight[id] != nil {
		if pinned && s.wishIconsEnabled() && (b.queued[id] || b.inflight[id] != nil) {
			b.pin(id)
		}
		return nil
	}
	if failure, ok := b.failures[id]; ok && time.Now().Before(failure.retryAt) {
		return nil
	}
	if b.queued == nil {
		b.queued = make(map[string]bool)
	}
	b.queued[id] = true
	b.wanted = append(b.wanted, id)
	if pinned {
		b.pin(id)
	}
	if b.trace != nil {
		if b.queuedAt == nil {
			b.queuedAt = make(map[string]time.Time)
		}
		// The first request keeps the clock: an icon that has to wait several
		// frames for a free worker reports the whole wait, not the last frame.
		if _, waiting := b.queuedAt[id]; !waiting {
			b.queuedAt[id] = time.Now()
		}
	}
	return nil
}

func (b *wishIconMedia) pin(id string) {
	if b.pinned == nil {
		b.pinned = make(map[string]bool)
	}
	b.pinned[id] = true
}

func (b *wishIconMedia) remember(id string, bitmap *ui.Bitmap) {
	if b.cache == nil {
		b.cache = make(map[string]*ui.Bitmap)
	}
	for i, old := range b.recent {
		if old == id {
			b.recent = append(b.recent[:i], b.recent[i+1:]...)
			break
		}
	}
	b.cache[id] = bitmap
	b.recent = append(b.recent, id)
	for len(b.recent) > wishIconCacheSize {
		delete(b.cache, b.recent[0])
		b.recent = b.recent[1:]
	}
}

// Decode only on the worker, with byte, header and pixel budgets before allocating
// the source image. Preserve aspect ratio; the view places the result with contain.
func decodeWishIcon(data []byte) (*image.RGBA, error) {
	if len(data) == 0 || len(data) > 2<<20 {
		return nil, errors.New("wish icon exceeds byte budget or is empty")
	}
	config, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > 4096 || config.Height > 4096 || int64(config.Width)*int64(config.Height) > 8<<20 {
		return nil, errors.New("wish icon exceeds pixel budget")
	}
	picture, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	w, h := config.Width, config.Height
	if w > wishIconDecodeEdge || h > wishIconDecodeEdge {
		if w >= h {
			h = max(1, h*wishIconDecodeEdge/w)
			w = wishIconDecodeEdge
		} else {
			w = max(1, w*wishIconDecodeEdge/h)
			h = wishIconDecodeEdge
		}
	}
	pixels := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.CatmullRom.Scale(pixels, pixels.Bounds(), picture, picture.Bounds(), draw.Src, nil)
	return pixels, nil
}
