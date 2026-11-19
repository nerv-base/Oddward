// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"

	"github.com/egoist/mygo/ui"
	"golang.org/x/image/draw"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// bannerImageSource is optional: announcement-only Sources remain usable. The
// production news.Client owns HTTPS/redirect/host policy; this projection does
// not implement an alternative downloader.
type bannerImageSource interface {
	Image(context.Context, string) ([]byte, error)
}

type bannerImageResult struct {
	id, generation uint64
	url            string
	pixels         *image.RGBA
	err            error
}

// bannerMedia is owned exclusively by the UI thread. A worker receives only
// copied request values and its output channel, never this struct or the Shell.
// A nil cache value records a failed image, without failing the news feed.
type bannerMedia struct {
	cache            map[string]*ui.Bitmap
	recent           []string
	generation       uint64
	initialized      bool
	out              chan bannerImageResult
	nextID, activeID uint64
	activeGeneration uint64
	activeURL        string
	cancel           context.CancelFunc
}

// syncBannerMedia is a UI-frame operation. One outstanding worker bounds decode
// memory and prevents rapid selection changes from launching unlimited decoders.
// A superseded request is cancelled; its completion clears the slot but cannot
// populate the cache. A source ignoring cancellation delays the next request,
// never blocks a frame or increases concurrency.
func (s *Shell) syncBannerMedia() {
	b := &s.bannerMedia
	if s.ctx == nil || s.ctx.Err() != nil || s.Model.Closed {
		if b.cancel != nil {
			b.cancel()
			b.cancel = nil
		}
		b.cache, b.recent = nil, nil
		return
	}
	url := s.selectedBannerImage()
	if !b.initialized || b.generation != s.Model.NewsRequest {
		b.cache, b.recent = make(map[string]*ui.Bitmap), nil
		b.generation, b.initialized = s.Model.NewsRequest, true
	}
	if b.activeID != 0 && (b.activeURL != url || b.activeGeneration != b.generation) && b.cancel != nil {
		b.cancel()
	}
	for {
		select {
		case result := <-b.out:
			if result.id != b.activeID {
				continue
			}
			b.activeID = 0
			if b.cancel != nil {
				b.cancel()
				b.cancel = nil
			}
			if result.generation != b.generation || result.url != url || url == "" || errors.Is(result.err, context.Canceled) {
				continue
			}
			var bitmap *ui.Bitmap
			if result.err == nil && result.pixels != nil {
				// Fixed MyGo bitmap.go:42/152 and scene.go:491-498: bitmap
				// levels belong to the UI thread; contiguous RGBA transfers
				// without another full-sized conversion or retained raw bytes.
				bitmap = ui.NewBitmap(result.pixels)
			}
			b.remember(result.url, bitmap)
		default:
			goto drained
		}
	}

drained:
	if url == "" {
		return
	}
	if bitmap, found := b.cache[url]; found {
		b.remember(url, bitmap)
		return
	}
	if b.activeID != 0 {
		return
	}
	src, ok := s.Source.(bannerImageSource)
	if !ok {
		return
	}
	if b.out == nil {
		b.out = make(chan bannerImageResult, 1)
	}
	ctx, cancel := context.WithCancel(s.ctx)
	b.nextID++
	b.activeID, b.activeGeneration, b.activeURL, b.cancel = b.nextID, b.generation, url, cancel
	id, generation, out, life, wake := b.activeID, b.generation, b.out, s.ctx, s.RequestFrame
	go func() {
		data, err := src.Image(ctx, url)
		var pixels *image.RGBA
		if err == nil && ctx.Err() == nil {
			pixels, err = decodeBanner(data)
		}
		if ctx.Err() != nil {
			pixels, err = nil, ctx.Err()
		}
		result := bannerImageResult{id: id, generation: generation, url: url, pixels: pixels, err: err}
		if life.Err() != nil {
			return
		}
		select {
		case out <- result:
			if wake != nil && life.Err() == nil {
				wake()
			}
		case <-life.Done():
		}
	}()
}

func (s *Shell) selectedBannerImage() string {
	if s.Model.Page != state.Home || s.Model.Overlay != state.NoOverlay || s.Model.News != state.NewsPresent || s.Model.Closed {
		return ""
	}
	_, _, url, ok := s.Model.NewsBanners.At(s.Model.NewsIndex)
	if !ok {
		return ""
	}
	return url
}

// remember is UI-only, including negative entries. No more than eight bitmaps
// of at most 1024x512 remain reachable through this cache (plus renderer mips).
func (b *bannerMedia) remember(url string, bitmap *ui.Bitmap) {
	if b.cache == nil {
		b.cache = make(map[string]*ui.Bitmap)
	}
	for i, recent := range b.recent {
		if recent == url {
			b.recent = append(b.recent[:i], b.recent[i+1:]...)
			break
		}
	}
	b.cache[url] = bitmap
	b.recent = append(b.recent, url)
	for len(b.recent) > 8 {
		delete(b.cache, b.recent[0])
		b.recent = b.recent[1:]
	}
}

func (s *Shell) bannerArtwork(c *ui.Context) {
	if s.ctx == nil || s.ctx.Err() != nil || s.Model.Closed || s.Model.Page != state.Home {
		return
	}
	bitmap := s.News // Existing fallback is read-only and never replaced.
	if s.bannerMedia.generation == s.Model.NewsRequest && s.Model.News == state.NewsPresent {
		// A modal hides actions and prevents new reads, not an already loaded
		// cover. Its image remains behind the modal without a fallback flash.
		previous, current, progress, sliding := s.bannerSlideFrame(c)
		if sliding {
			ui.Box(c.Key("banner-base")).Fill().Background(ui.Hex("#11151A")).PassThrough()
			width := float32(coverWidth)
			if s.bannerSlide.dir < 0 {
				ui.Image(c.Key("banner-artwork-from"), previous).Absolute().Left(0).Right(0).Top(0).Bottom(0).Fit(ui.Cover).PassThrough()
				reveal := max(progress*width, 0.5)
				ui.Box(c.Key("banner-artwork-to-clip")).Absolute().Left(0).Top(0).Bottom(0).Width(reveal).Clip().PassThrough().Children(func() {
					ui.Image(c.Key("banner-artwork-to"), current).Absolute().Left(0).Right(0).Top(0).Bottom(0).Fit(ui.Cover).PassThrough()
				})
			} else {
				ui.Image(c.Key("banner-artwork-to"), current).Absolute().Left(0).Right(0).Top(0).Bottom(0).Fit(ui.Cover).PassThrough()
				reveal := max((1-progress)*width, 0.5)
				ui.Box(c.Key("banner-artwork-from-clip")).Absolute().Left(0).Top(0).Bottom(0).Width(reveal).Clip().PassThrough().Children(func() {
					ui.Image(c.Key("banner-artwork-from"), previous).Absolute().Left(0).Right(0).Top(0).Bottom(0).Fit(ui.Cover).PassThrough()
				})
			}
			return
		}
		if current != nil {
			bitmap = current
		}
	}
	ui.Box(c.Key("banner-base")).Fill().Background(ui.Hex("#11151A")).PassThrough()
	if bitmap != nil {
		ui.Image(c.Key("banner-artwork"), bitmap).Absolute().Left(0).Right(0).Top(0).Bottom(0).Fit(ui.Cover).PassThrough()
	}
}

func (s *Shell) bannerSelector(c *ui.Context) {
	if s.ctx == nil || s.ctx.Err() != nil || s.Model.Closed || s.Model.Page != state.Home || s.Model.Overlay != state.NoOverlay || s.Model.News != state.NewsPresent {
		return
	}
	count := min(s.Model.NewsBanners.Count, uint8(8))
	if count == 0 {
		return
	}
	ui.Row(c.Key("banner-selector")).Absolute().Left(10).Top(10).Height(16).Gap(2).Children(func() {
		for index := uint8(0); index < count; index++ {
			index := index
			title, _, _, _ := s.Model.NewsBanners.At(index)
			selected := s.Model.NewsIndex == index
			b := ui.ButtonBase(c.Key(fmt.Sprintf("banner-%d", index))).Label(fmt.Sprintf("官方轮播封面 %d", index+1)).Tooltip(title).Size(20, 16).Justify(ui.Center).
				Role(ui.RoleTab).Selected(selected).Background(ui.Transparent)
			b.Children(func() {
				color, width := ui.RGBA(245, 246, 247, .55), float32(6)
				if selected {
					color, width = s.tone(), 16
				}
				ui.Box(c).Size(width, 4).Radius(2).Background(color).PassThrough()
			}).OnClick(func() { s.Dispatch(state.Event{Kind: state.SelectNewsStory, Index: index}) })
		}
	})
}

// decodeBanner runs only on a worker. Recheck byte/header budgets even for an
// optional non-Client source, then discard the large decode and compressed data
// after producing a compact, aspect-preserving premultiplied RGBA transfer.
func decodeBanner(data []byte) (*image.RGBA, error) {
	if len(data) == 0 || len(data) > 4<<20 {
		return nil, errors.New("banner image exceeds the 4 MiB byte budget or is empty")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if (format != "jpeg" && format != "png") || config.Width <= 0 || config.Height <= 0 || config.Width > 4096 || config.Height > 4096 || int64(config.Width)*int64(config.Height) > 16<<20 {
		return nil, errors.New("banner image format or dimensions exceed the JPEG/PNG budget")
	}
	picture, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	width, height := config.Width, config.Height
	if width > 1024 {
		height, width = max(1, height*1024/width), 1024
	}
	if height > 512 {
		width, height = max(1, width*512/height), 512
	}
	compact := image.NewRGBA(image.Rect(0, 0, width, height))
	// x/image v0.46.0 is already pinned by this repository; no new dependency.
	draw.ApproxBiLinear.Scale(compact, compact.Bounds(), picture, picture.Bounds(), draw.Src, nil)
	return compact, nil
}
