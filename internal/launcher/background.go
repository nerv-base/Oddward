// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"context"
	"image"
	"log"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/video"
)

// BackgroundSource loads public artwork without touching the UI thread.
type BackgroundSource interface {
	Load(context.Context) (video string, poster image.Image, err error)
}

type backgroundResult struct {
	video  string
	poster image.Image
	tone   ui.Color
	err    error
}

// syncBackground starts one refresh and applies its result on the UI thread.
// While the worker runs, the stage can render its normal asset-free fallback.
func (s *Shell) syncBackground() {
	if s.ctx.Err() != nil || s.Model.Closed {
		return
	}
	if s.BackgroundSource != nil {
		source, ctx, wake := s.BackgroundSource, s.ctx, s.RequestFrame
		s.BackgroundSource = nil
		out := make(chan backgroundResult, 1)
		s.backgroundOut = out
		go func() {
			path, poster, err := source.Load(ctx)
			var tone ui.Color
			if poster != nil {
				tone, _ = ArtAccent(poster)
			}
			if ctx.Err() != nil {
				return
			}
			out <- backgroundResult{video: path, poster: poster, tone: tone, err: err}
			if wake != nil {
				wake()
			}
		}()
	}
	select {
	case result := <-s.backgroundOut:
		s.backgroundOut = nil
		if result.err != nil {
			log.Printf("official background refresh: %v", result.err)
		}
		if result.poster != nil {
			s.Poster = ui.NewBitmap(result.poster)
			s.Accent = result.tone
		}
		if result.video != "" {
			if p, err := video.Open(result.video); err == nil {
				s.Player = p.SetLoop(true).SetMuted(true)
			} else {
				log.Printf("official background video unavailable: %v", err)
			}
		}
	default:
	}
}
