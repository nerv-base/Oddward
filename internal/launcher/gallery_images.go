// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"context"
	"image"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

const (
	// galleryThumbWorkers bounds parallel off-UI-thread thumbnail decodes.
	galleryThumbWorkers = 4
	// galleryBitmapBudgetBytes caps resident GPU/CPU bitmap memory for grid
	// thumbnails at 64 MiB per PRD §14.
	galleryBitmapBudgetBytes int64 = 64 << 20
	// galleryBitmapMaxEntries bounds map/texture count alongside byte budget.
	galleryBitmapMaxEntries = 256
	// galleryThumbEdgeL1 is the main grid thumbnail edge (scaled up at high DPI).
	galleryThumbEdgeL1 = 384
	// galleryThumbEdgeL2 is the fast initial preview edge for the image viewer.
	galleryThumbEdgeL2 = 1024
	// galleryThumbRetryDelay backs off failed decodes so corrupt/unreadable
	// files do not spin CPU every frame.
	galleryThumbRetryDelay = 15 * time.Second
)

type galleryBitmapEntry struct {
	bitmap *ui.Bitmap
	bytes  int64
}

type galleryThumbKey struct {
	assetID int64
	edge    int
}

type galleryThumbResult struct {
	key        galleryThumbKey
	generation uint64
	pixels     *image.RGBA
	err        error
	cancelled  bool
}

type galleryViewerImageResult struct {
	assetID    int64
	generation uint64
	full       bool
	pixels     *image.RGBA
	err        error
	cancelled  bool
}

// galleryImageCache manages the byte-budgeted thumbnail Bitmap LRU, bounded
// decode workers and single-slot viewer loader. All map/LRU/bitmap mutations
// happen strictly on the UI thread.
type galleryImageCache struct {
	cache       map[galleryThumbKey]galleryBitmapEntry
	recent      []galleryThumbKey
	cachedBytes int64
	budgetBytes int64

	wanted   []galleryThumbReq
	queued   map[galleryThumbKey]bool
	inflight map[galleryThumbKey]context.CancelFunc
	painted  map[galleryThumbKey]bool
	pinned   map[galleryThumbKey]bool
	failures map[galleryThumbKey]time.Time

	generation uint64
	active     bool
	out        chan galleryThumbResult

	// Viewer image loader: keeps at most one full-resolution bitmap resident.
	viewerAssetID int64
	viewerGen     uint64
	viewerBitmap  *ui.Bitmap
	viewerIsFull  bool
	viewerLoading bool
	viewerErr     string
	viewerCancel  context.CancelFunc
	viewerOut     chan galleryViewerImageResult
}

type galleryThumbReq struct {
	key   galleryThumbKey
	asset GalleryAsset
}

func (c *galleryImageCache) budget() int64 {
	if c.budgetBytes > 0 {
		return c.budgetBytes
	}
	return galleryBitmapBudgetBytes
}

func (c *galleryImageCache) remember(key galleryThumbKey, bitmap *ui.Bitmap) {
	if bitmap == nil {
		return
	}
	if c.cache == nil {
		c.cache = make(map[galleryThumbKey]galleryBitmapEntry)
	}
	if prev, ok := c.cache[key]; ok {
		c.cachedBytes -= prev.bytes
		if prev.bitmap != nil && prev.bitmap != bitmap {
			prev.bitmap.ReleasePixels()
		}
		c.removeRecent(key)
	}
	w, h := bitmap.Size()
	sz := max(int64(w)*int64(h)*4, 4096)
	c.cache[key] = galleryBitmapEntry{bitmap: bitmap, bytes: sz}
	c.cachedBytes += sz
	c.recent = append(c.recent, key)

	limit := c.budget()
	for len(c.recent) > 1 && (c.cachedBytes > limit || len(c.recent) > galleryBitmapMaxEntries) {
		oldest := c.recent[0]
		c.recent = c.recent[1:]
		if entry, ok := c.cache[oldest]; ok {
			c.cachedBytes -= entry.bytes
			if entry.bitmap != nil {
				entry.bitmap.ReleasePixels()
			}
			delete(c.cache, oldest)
		}
	}
}

func (c *galleryImageCache) touch(key galleryThumbKey) *ui.Bitmap {
	entry, ok := c.cache[key]
	if !ok || entry.bitmap == nil {
		return nil
	}
	c.removeRecent(key)
	c.recent = append(c.recent, key)
	return entry.bitmap
}

func (c *galleryImageCache) removeRecent(key galleryThumbKey) {
	for i, k := range c.recent {
		if k == key {
			c.recent = append(c.recent[:i], c.recent[i+1:]...)
			return
		}
	}
}

func (c *galleryImageCache) cancelAllThumbs() {
	for _, cancel := range c.inflight {
		if cancel != nil {
			cancel()
		}
	}
}

func (c *galleryImageCache) releaseAllBitmaps() {
	c.cancelAllThumbs()
	for k, entry := range c.cache {
		if entry.bitmap != nil {
			entry.bitmap.ReleasePixels()
		}
		delete(c.cache, k)
	}
	c.recent = nil
	c.cachedBytes = 0
	c.wanted = nil
	c.queued = nil
	c.painted = nil
	c.pinned = nil
	c.releaseViewerBitmap()
}

func (c *galleryImageCache) releaseViewerBitmap() {
	if c.viewerCancel != nil {
		c.viewerCancel()
		c.viewerCancel = nil
	}
	if c.viewerBitmap != nil {
		c.viewerBitmap.ReleasePixels()
		c.viewerBitmap = nil
	}
	c.viewerAssetID = 0
	c.viewerIsFull = false
	c.viewerLoading = false
	c.viewerErr = ""
}

func (s *Shell) galleryImagesEnabled() bool {
	return s.ctx != nil && s.ctx.Err() == nil && !s.Model.Closed && s.Model.Page == state.Gallery && s.Model.Overlay == state.NoOverlay && s.GalleryBackend != nil
}

// galleryThumbEdge picks the thumbnail tier based on display scale.
func galleryThumbEdge(scale float32) int {
	if scale >= 1.75 {
		return 512
	}
	return galleryThumbEdgeL1
}

func (s *Shell) galleryThumb(asset GalleryAsset, edge int) *ui.Bitmap {
	key := galleryThumbKey{assetID: asset.ID, edge: edge}
	delete(s.gallery.images.pinned, key)
	return s.requestGalleryThumb(asset, edge, false)
}

func (s *Shell) prefetchGalleryThumb(asset GalleryAsset, edge int) {
	_ = s.requestGalleryThumb(asset, edge, true)
}

func (s *Shell) requestGalleryThumb(asset GalleryAsset, edge int, pinned bool) *ui.Bitmap {
	if asset.ID <= 0 || asset.Invalid || !asset.Available || asset.PrimaryPath == "" {
		return nil
	}
	if asset.IsVideo() && asset.PosterPath == "" {
		return nil
	}
	img := &s.gallery.images
	key := galleryThumbKey{assetID: asset.ID, edge: edge}
	if img.painted == nil {
		img.painted = make(map[galleryThumbKey]bool)
	}
	img.painted[key] = true
	if bmp := img.touch(key); bmp != nil {
		return bmp
	}
	if !s.galleryImagesEnabled() || img.queued[key] || img.inflight[key] != nil {
		if pinned && s.galleryImagesEnabled() && (img.queued[key] || img.inflight[key] != nil) {
			if img.pinned == nil {
				img.pinned = make(map[galleryThumbKey]bool)
			}
			img.pinned[key] = true
		}
		return nil
	}
	if retryAt, failed := img.failures[key]; failed && time.Now().Before(retryAt) {
		return nil
	}
	if img.queued == nil {
		img.queued = make(map[galleryThumbKey]bool)
	}
	img.queued[key] = true
	img.wanted = append(img.wanted, galleryThumbReq{key: key, asset: asset})
	if pinned {
		if img.pinned == nil {
			img.pinned = make(map[galleryThumbKey]bool)
		}
		img.pinned[key] = true
	}
	return nil
}

func (s *Shell) syncGalleryImages() {
	img := &s.gallery.images
	enabled := s.galleryImagesEnabled()

	// Drain finished thumbnail decodes.
	for img.out != nil {
		select {
		case r := <-img.out:
			delete(img.inflight, r.key)
			img.active = len(img.inflight) > 0
			if r.cancelled || r.generation != img.generation || !enabled {
				continue
			}
			if r.err != nil {
				if img.failures == nil {
					img.failures = make(map[galleryThumbKey]time.Time)
				}
				img.failures[r.key] = time.Now().Add(galleryThumbRetryDelay)
				continue
			}
			if r.pixels != nil {
				delete(img.failures, r.key)
				img.remember(r.key, ui.NewBitmap(r.pixels))
			}
		default:
			goto drainViewer
		}
	}

drainViewer:
	for img.viewerOut != nil {
		select {
		case r := <-img.viewerOut:
			if r.cancelled || r.generation != img.viewerGen || !enabled || !s.Model.Gallery.ViewerOpen || r.assetID != s.Model.Gallery.ViewerID {
				continue
			}
			if r.err != nil {
				img.viewerLoading = false
				img.viewerErr = state.BriefError(r.err)
				continue
			}
			if r.pixels != nil {
				if img.viewerBitmap != nil {
					img.viewerBitmap.ReleasePixels()
				}
				img.viewerBitmap = ui.NewBitmap(r.pixels)
				img.viewerIsFull = r.full
				img.viewerErr = ""
				if r.full {
					img.viewerLoading = false
				}
			}
		default:
			goto afterDrain
		}
	}

afterDrain:
	if !enabled {
		// Leaving the gallery page cancels pending prefetch work and releases
		// GPU bitmaps so background standby holds zero gallery graphics memory.
		if len(img.cache) > 0 || len(img.inflight) > 0 || img.viewerBitmap != nil {
			img.generation++
			img.releaseAllBitmaps()
		}
		return
	}

	// Cancel offscreen in-flight thumbnail decodes so fast scrolling stays responsive.
	if img.painted != nil {
		for k, cancel := range img.inflight {
			if !img.painted[k] && !img.pinned[k] && cancel != nil {
				cancel()
			}
		}
	}
	img.painted = make(map[galleryThumbKey]bool)
	wanted := img.wanted
	img.wanted = nil
	img.queued = nil

	for _, req := range wanted {
		if len(img.inflight) >= galleryThumbWorkers {
			break
		}
		if _, ok := img.cache[req.key]; ok || img.inflight[req.key] != nil {
			continue
		}
		if retryAt, failed := img.failures[req.key]; failed && time.Now().Before(retryAt) {
			continue
		}
		s.startGalleryThumb(req)
	}

	// Synchronize viewer image target.
	if !s.Model.Gallery.ViewerOpen || s.Model.Gallery.ViewerID <= 0 {
		if img.viewerAssetID != 0 || img.viewerBitmap != nil {
			img.viewerGen++
			img.releaseViewerBitmap()
		}
		return
	}
	if img.viewerAssetID != s.Model.Gallery.ViewerID {
		if asset, ok := s.galleryAssetByID(s.Model.Gallery.ViewerID); ok {
			s.startGalleryViewerLoad(asset)
		}
	}
}

func (s *Shell) startGalleryThumb(req galleryThumbReq) {
	img := &s.gallery.images
	if img.out == nil {
		img.out = make(chan galleryThumbResult, galleryThumbWorkers*2)
	}
	if img.inflight == nil {
		img.inflight = make(map[galleryThumbKey]context.CancelFunc)
	}
	ctx, cancel := context.WithCancel(s.ctx)
	img.inflight[req.key] = cancel
	img.active = true
	backend, out, life, wake, gen := s.GalleryBackend, img.out, s.ctx, s.RequestFrame, img.generation
	go func() {
		defer cancel()
		pixels, _, err := backend.DecodeThumbnail(ctx, req.asset, req.key.edge)
		cancelled := ctx.Err() != nil
		if cancelled {
			pixels, err = nil, nil
		}
		if life.Err() != nil {
			return
		}
		select {
		case out <- galleryThumbResult{key: req.key, generation: gen, pixels: pixels, err: err, cancelled: cancelled}:
			if wake != nil && life.Err() == nil {
				wake()
			}
		case <-life.Done():
		}
	}()
}

func (s *Shell) startGalleryViewerLoad(asset GalleryAsset) {
	img := &s.gallery.images
	img.viewerGen++
	img.releaseViewerBitmap()
	img.viewerAssetID = asset.ID
	if asset.IsVideo() {
		// Videos play via VideoPlayer on the UI thread rather than decoding full-res RGBA stills.
		return
	}
	if asset.Invalid || !asset.Available || asset.PrimaryPath == "" {
		img.viewerErr = "源文件不可用或已损坏"
		return
	}
	if s.GalleryBackend == nil {
		return
	}
	if img.viewerOut == nil {
		img.viewerOut = make(chan galleryViewerImageResult, 4)
	}
	ctx, cancel := context.WithCancel(s.ctx)
	img.viewerCancel = cancel
	img.viewerLoading = true
	gen := img.viewerGen
	backend, out, life, wake := s.GalleryBackend, img.viewerOut, s.ctx, s.RequestFrame

	go func() {
		defer cancel()
		send := func(px *image.RGBA, full bool, err error) {
			cancelled := ctx.Err() != nil
			if cancelled {
				px, err = nil, nil
			}
			if life.Err() != nil {
				return
			}
			select {
			case out <- galleryViewerImageResult{assetID: asset.ID, generation: gen, full: full, pixels: px, err: err, cancelled: cancelled}:
				if wake != nil && life.Err() == nil {
					wake()
				}
			case <-life.Done():
			}
		}
		// Fast L2 preview first when source is larger than L2 so viewer appears crisp immediately.
		if asset.Width > galleryThumbEdgeL2 || asset.Height > galleryThumbEdgeL2 {
			if px, _, err := backend.DecodeThumbnail(ctx, asset, galleryThumbEdgeL2); err == nil && ctx.Err() == nil {
				send(px, false, nil)
			}
		}
		if ctx.Err() != nil {
			return
		}
		fullPx, err := backend.DecodeOriginal(ctx, asset)
		send(fullPx, true, err)
	}()
}

// galleryViewerBitmap returns the sharpest available bitmap for the viewer:
// full resolution -> L2 -> cached L1 thumbnail -> nil.
func (s *Shell) galleryViewerBitmap(assetID int64) (*ui.Bitmap, bool, bool, string) {
	img := &s.gallery.images
	if img.viewerAssetID == assetID && img.viewerBitmap != nil {
		return img.viewerBitmap, img.viewerIsFull, img.viewerLoading, img.viewerErr
	}
	// Fall back to any cached grid thumbnail while full-res decodes.
	for k, entry := range img.cache {
		if k.assetID == assetID && entry.bitmap != nil {
			return entry.bitmap, false, img.viewerLoading, img.viewerErr
		}
	}
	return nil, false, img.viewerLoading, img.viewerErr
}
