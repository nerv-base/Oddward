// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"context"
	"image"
	"log"

	"github.com/egoist/mygo/ui"
	"github.com/egoist/mygo/ui/video"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// BackgroundAssets is the official artwork already available on disk.
type BackgroundAssets struct {
	Video  []byte
	Poster image.Image
}

// BackgroundRefresh carries only the assets a refresh replaced.
type BackgroundRefresh struct {
	Assets        BackgroundAssets
	VideoChanged  bool
	PosterChanged bool
}

// BackgroundSource reads cached artwork without touching the UI thread and
// checks the official manifest separately.
type BackgroundSource interface {
	Cached() (BackgroundAssets, error)
	Refresh(context.Context) (BackgroundRefresh, error)
}

type backgroundResult struct {
	assets        BackgroundAssets
	videoChanged  bool
	posterChanged bool
	tone          ui.Color
	err           error
	final         bool
}

// syncBackground starts one cache read followed by one refresh. The cached
// result is applied as soon as it is available; the refresh result follows and
// only replaces the assets it actually changed.
func (s *Shell) syncBackground() {
	if s.ctx.Err() != nil || s.Model.Closed {
		return
	}
	if s.BackgroundSource != nil {
		source, ctx, wake := s.BackgroundSource, s.ctx, s.RequestFrame
		s.BackgroundSource = nil
		out := make(chan backgroundResult, 2)
		s.backgroundOut = out
		go func() {
			cached, err := source.Cached()
			if ctx.Err() != nil {
				return
			}
			out <- backgroundResult{
				assets:        cached,
				videoChanged:  len(cached.Video) > 0,
				posterChanged: cached.Poster != nil,
				tone:          artworkAccent(cached.Poster),
				err:           err,
			}
			if wake != nil {
				wake()
			}
			update, err := source.Refresh(ctx)
			if ctx.Err() != nil {
				return
			}
			out <- backgroundResult{
				assets:        update.Assets,
				videoChanged:  update.VideoChanged,
				posterChanged: update.PosterChanged,
				tone:          artworkAccent(update.Assets.Poster),
				err:           err,
				final:         true,
			}
			if wake != nil {
				wake()
			}
		}()
	}
	for {
		select {
		case result := <-s.backgroundOut:
			if result.final {
				s.backgroundOut = nil
			}
			if s.Model.CustomBackground {
				continue
			}
			if result.err != nil {
				log.Printf("official background: %v", result.err)
			}
			if result.posterChanged && result.assets.Poster != nil {
				s.Poster = ui.NewBitmap(result.assets.Poster)
				if result.tone.A != 0 {
					s.Accent = result.tone
				}
			}
			if result.videoChanged && len(result.assets.Video) > 0 {
				s.openBackgroundVideo(result.assets.Video)
			}
		default:
			return
		}
	}
}

// applyCustomStaticBackground switches the live launcher backdrop to a
// validated gallery photo and updates the derived accent color on the UI thread.
func (s *Shell) applyCustomStaticBackground(img image.Image) {
	if img == nil {
		return
	}
	if s.Player != nil {
		s.Player.Close()
		s.Player = nil
	}
	if s.nextPlayer != nil {
		s.nextPlayer.Close()
		s.nextPlayer = nil
	}
	s.BackgroundSource = nil
	s.Poster = ui.NewBitmap(img)
	if tone, ok := ArtAccent(img); ok {
		s.Accent = tone
	}
	if !s.Model.MediaPreview {
		s.Dispatch(state.Event{Kind: state.MediaFailed})
	}
	s.Dispatch(state.Event{Kind: state.GalleryCustomBackgroundLoaded, Enabled: true})
}

// restoreOfficialBackground clears a custom gallery background and restarts the
// official background source live without requiring a launcher restart.
func (s *Shell) restoreOfficialBackground() {
	s.Dispatch(state.Event{Kind: state.GalleryCustomBackgroundLoaded, Enabled: false})
	if s.NewBackgroundSource != nil {
		s.BackgroundSource = s.NewBackgroundSource()
		if !s.Model.MediaPreview {
			s.Dispatch(state.Event{Kind: state.MediaRetry})
		}
		return
	}
	s.Poster = nil
	s.Accent = ui.Color{}
}

func artworkAccent(poster image.Image) ui.Color {
	if poster == nil {
		return ui.Color{}
	}
	tone, _ := ArtAccent(poster)
	return tone
}

// openBackgroundVideo queues a replacement player. The new player must be
// mounted before it can produce its first frame, so the old one stays visible
// until tick observes Ready; only then is it closed.
func (s *Shell) openBackgroundVideo(data []byte) {
	player, err := video.OpenData(data)
	if err != nil {
		log.Printf("official background video unavailable: %v", err)
		return
	}
	player.SetLoop(true).SetMuted(true)
	if s.Player == nil {
		s.Player = player
		if !s.Model.MediaPreview && s.Model.Media == state.MediaUnavailable {
			s.Dispatch(state.Event{Kind: state.MediaRetry})
		}
		return
	}
	if s.nextPlayer != nil {
		s.nextPlayer.Close()
	}
	s.nextPlayer = player
}
