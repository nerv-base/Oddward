// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

const newsBannerInterval = 6 * time.Second

type bannerRotation struct {
	module   ui.Handle
	deadline time.Time
	request  uint64
	index    uint8
}

// Advance before building the cover so its image, title and click target all
// come from the same selected slide. Pausing starts a fresh viewing interval.
func (s *Shell) updateBannerRotation(c *ui.Context, module ui.Element) {
	r := &s.bannerRotation
	_, _, inside := module.PointerPosition()
	if s.Model.Page != state.Home || s.Model.Overlay != state.NoOverlay || s.Model.News != state.NewsPresent ||
		s.Model.NewsBanners.Count < 2 || !s.motion() || inside || r.module.FocusWithin(c) {
		r.deadline = time.Time{}
		return
	}
	now := c.Now()
	if r.deadline.IsZero() || r.request != s.Model.NewsRequest || r.index != s.Model.NewsIndex {
		r.deadline = now.Add(newsBannerInterval)
	} else if !now.Before(r.deadline) {
		s.Dispatch(state.Event{Kind: state.AdvanceNewsBanner})
		s.syncBannerMedia()
		r.deadline = now.Add(newsBannerInterval)
	}
	r.request, r.index = s.Model.NewsRequest, s.Model.NewsIndex
	c.After(r.deadline.Sub(now))
}
