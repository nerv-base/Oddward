// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"bytes"
	"context"
	"errors"
	"github.com/egoist/mygo/ui"
	"github.com/nerv-base/Oddward/internal/launcher/state"
	"golang.org/x/image/draw"
	"image"
)

type wishIconResult struct {
	id         string
	generation uint64
	pixels     *image.RGBA
	err        error
}

// All maps, bitmaps, queue and LRU entries belong to the UI thread. A single
// outstanding worker bounds decode memory, even if a source ignores cancel.
type wishIconMedia struct {
	cache            map[string]*ui.Bitmap
	failed           map[string]bool
	recent           []string
	wanted           []string
	queued           map[string]bool
	generation       uint64
	active           bool
	activeID         string
	activeGeneration uint64
	cancel           context.CancelFunc
	out              chan wishIconResult
}

func (s *Shell) wishIconsEnabled() bool {
	return s.ctx != nil && s.ctx.Err() == nil && !s.Model.Closed && s.Model.Page == state.WishAnalysis && s.Model.Overlay == state.NoOverlay && s.WishImages != nil
}

func (s *Shell) syncWishImages() {
	b := &s.wishIcons
	enabled := s.wishIconsEnabled()
	b.generation = s.Model.Wish.Token
	if b.active && (!enabled || b.activeGeneration != b.generation) && b.cancel != nil {
		b.cancel()
	}
	for b.out != nil {
		select {
		case r := <-b.out:
			if !b.active || r.id != b.activeID || r.generation != b.activeGeneration {
				continue
			}
			b.active = false
			if b.cancel != nil {
				b.cancel()
				b.cancel = nil
			}
			if !enabled || r.generation != b.generation || errors.Is(r.err, context.Canceled) {
				continue
			}
			if r.err != nil || r.pixels == nil {
				if b.failed == nil {
					b.failed = make(map[string]bool)
				}
				b.failed[r.id] = true
				continue
			}
			b.remember(r.id, ui.NewBitmap(r.pixels))
		default:
			goto drained
		}
	}
drained:
	wanted := b.wanted
	b.wanted = nil
	b.queued = nil
	if !enabled || b.active {
		return
	}
	for _, id := range wanted {
		if b.failed[id] || b.cache[id] != nil {
			continue
		}
		if b.out == nil {
			b.out = make(chan wishIconResult, 1)
		}
		ctx, cancel := context.WithCancel(s.ctx)
		b.active, b.activeID, b.activeGeneration, b.cancel = true, id, b.generation, cancel
		source, out, life, wake, generation := s.WishImages, b.out, s.ctx, s.RequestFrame, b.generation
		go func() {
			data, err := source.Image(ctx, id)
			var pixels *image.RGBA
			if err == nil && ctx.Err() == nil {
				pixels, err = decodeWishIcon(data)
			}
			if ctx.Err() != nil {
				pixels, err = nil, ctx.Err()
			}
			if life.Err() != nil {
				return
			}
			select {
			case out <- wishIconResult{id: id, generation: generation, pixels: pixels, err: err}:
				if wake != nil && life.Err() == nil {
					wake()
				}
			case <-life.Done():
			}
		}()
		return
	}
}

func (s *Shell) wishIcon(id string) *ui.Bitmap {
	b := &s.wishIcons
	if bitmap := b.cache[id]; bitmap != nil {
		b.remember(id, bitmap)
		return bitmap
	}
	if !s.wishIconsEnabled() || b.failed[id] || (b.active && b.activeID == id) || b.queued[id] {
		return nil
	}
	if b.queued == nil {
		b.queued = make(map[string]bool)
	}
	b.queued[id] = true
	b.wanted = append(b.wanted, id)
	return nil
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
