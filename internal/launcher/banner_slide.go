package launcher

import (
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

const (
	bannerSlideDuration = 320 * time.Millisecond
	bannerSlideFrame    = 16 * time.Millisecond
	bannerWheelInterval = 160 * time.Millisecond
)

// bannerSlide remembers the previous cover while the carousel transitions to
// the next one. It is animation state only; Model.NewsIndex stays the single
// source of truth for which banner is selected.
type bannerSlide struct {
	from, to   uint8
	dir        int8
	pending    bool
	active     bool
	valid      bool
	start      time.Time
	last       uint8
	count      uint8
	wheelAt    time.Time
	fromBitmap *ui.Bitmap
	toBitmap   *ui.Bitmap
}

func easeBannerSlide(t float32) float32 {
	if t <= 0 {
		return 0
	}
	if t >= 1 {
		return 1
	}
	// Smooth ease-out: fast start, gentle stop, no overshoot.
	return 1 - (1-t)*(1-t)*(1-t)
}

func (s *Shell) bannerBitmap(index uint8) *ui.Bitmap {
	if s.bannerMedia.generation != s.Model.NewsRequest || s.Model.News != state.NewsPresent {
		return nil
	}
	_, _, url, ok := s.Model.NewsBanners.At(index)
	if !ok {
		return nil
	}
	return s.bannerMedia.cache[url]
}

func (s *Shell) resetBannerSlide() {
	s.bannerSlide.pending = false
	s.bannerSlide.active = false
	s.bannerSlide.valid = false
}

// bannerSlideFrame returns the two bitmaps to draw and the eased progress. A
// missing bitmap or reduced motion falls back to the current cover, never to a
// fabricated frame.
func (s *Shell) bannerSlideFrame(c *ui.Context) (previous, current *ui.Bitmap, progress float32, sliding bool) {
	sl := &s.bannerSlide
	index := s.Model.NewsIndex
	count := min(s.Model.NewsBanners.Count, uint8(8))
	if s.bannerMedia.generation != s.Model.NewsRequest {
		s.resetBannerSlide()
	}
	if !s.motion() || count < 2 {
		s.resetBannerSlide()
		return nil, s.bannerBitmap(index), 0, false
	}
	if !sl.valid || sl.count != count {
		sl.valid, sl.count, sl.last = true, count, index
		sl.pending, sl.active = false, false
		return nil, s.bannerBitmap(index), 0, false
	}
	if index != sl.last {
		switch {
		case index == 0 && sl.last == count-1:
			sl.dir = 1
		case index == count-1 && sl.last == 0:
			sl.dir = -1
		case index > sl.last:
			sl.dir = 1
		default:
			sl.dir = -1
		}
		sl.from, sl.to = sl.last, index
		sl.fromBitmap = s.bannerBitmap(sl.from)
		sl.toBitmap = nil
		sl.pending, sl.active = true, false
		sl.start = time.Time{}
		sl.last = index
	}
	if sl.pending {
		sl.toBitmap = s.bannerBitmap(sl.to)
		if sl.toBitmap == nil {
			// The new cover is not decoded yet: hold the old one for this
			// frame, then slide as soon as it arrives.
			return sl.fromBitmap, nil, 0, false
		}
		if sl.fromBitmap == nil {
			sl.pending = false
			return nil, sl.toBitmap, 0, false
		}
		sl.pending, sl.active = false, true
		sl.start = c.Now()
	}
	if !sl.active {
		return nil, s.bannerBitmap(index), 0, false
	}
	previous, current = sl.fromBitmap, sl.toBitmap
	if previous == nil || current == nil {
		sl.active = false
		return nil, current, 0, false
	}
	elapsed := c.Now().Sub(sl.start)
	if elapsed < 0 {
		elapsed = 0
	}
	p := float32(elapsed) / float32(bannerSlideDuration)
	if p >= 1 {
		sl.active = false
		return nil, current, 0, false
	}
	c.After(bannerSlideFrame)
	return previous, current, easeBannerSlide(p), true
}

// stepNewsBanner switches the carousel from the wheel over its cover. It keeps
// the selected tab visible in the selector and delegates the actual switch to
// the state event, so the model remains authoritative.
func (s *Shell) stepNewsBanner(dy float32) bool {
	if s.Model.News != state.NewsPresent || s.Model.Overlay != state.NoOverlay || s.Model.Page != state.Home {
		return false
	}
	count := int(min(s.Model.NewsBanners.Count, uint8(8)))
	if count < 2 {
		return false
	}
	if !s.bannerSlide.wheelAt.IsZero() && s.now.Sub(s.bannerSlide.wheelAt) < bannerWheelInterval {
		return true
	}
	s.bannerSlide.wheelAt = s.now
	delta := 1
	if dy < 0 {
		delta = -1
	}
	next := (int(s.Model.NewsIndex) + delta + count) % count
	s.Dispatch(state.Event{Kind: state.SelectNewsStory, Index: uint8(next)})
	return true
}
