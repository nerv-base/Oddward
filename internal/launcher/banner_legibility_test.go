// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"image"
	"image/color"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func TestBannerTextMaskRemainsOverLoadedArtwork(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	im := image.NewRGBA(image.Rect(0, 0, 32, 16))
	for y := 0; y < 16; y++ {
		for x := 0; x < 32; x++ {
			im.SetRGBA(x, y, color.RGBA{255, 255, 255, 255})
		}
	}
	s.News = ui.NewBitmap(im)
	s.Model.News, s.Model.NewsBanners = state.NewsPresent, projectedBannerFixture()
	tt := ui.NewTester(s.View, 1280, 720)
	tt.Frame()
	r, ok := tt.Find("查看官方公告")
	if !ok {
		t.Fatal("cover missing")
	}
	// At the left foot of the cover, away from type and icons, a loaded bright
	// image must remain under the text-legibility mask, not paint over it.
	rr, gg, bb, _ := tt.Image().At(int(r.X+10), int(r.Y+r.H-12)).RGBA()
	if max(rr, gg, bb) > 55*257 {
		t.Fatalf("loaded artwork escaped the text mask: %d/%d/%d", rr/257, gg/257, bb/257)
	}
}
