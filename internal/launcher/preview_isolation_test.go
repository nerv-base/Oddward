// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"fmt"
	"image"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func TestNativeMediaMockDoesNotControlRealPlayer(t *testing.T) {
	for _, playing := range []bool{false, true} {
		for media := state.MediaLoading; media <= state.MediaUnavailable; media++ {
			t.Run(fmt.Sprintf("playing-%v-preview-%d", playing, media), func(t *testing.T) {
				m := settled(state.Ready)
				m.Media, m.MediaPreview = media, true
				s := NewShell(m)
				t.Cleanup(s.Dispose)
				p, err := ui.NewVideoPlayer(&posterProbeDecoder{frame: image.NewRGBA(image.Rect(0, 0, 16, 9))})
				if err != nil {
					t.Fatal(err)
				}
				s.Player = p
				if playing {
					p.Play()
				} else {
					p.Pause() // NewVideoPlayer starts playing; make both fixtures explicit.
				}
				tt := ui.NewTester(s.View, 1280, 720)
				if p.Playing() != playing {
					t.Fatalf("mock media %d changed actual player from %v to %v", media, playing, p.Playing())
				}
				if _, mounted := tt.Find("背景视频"); mounted {
					t.Fatal("media mock mounted the real decoder surface")
				}
			})
		}
	}
}

func TestNativeF8IgnoresAutoRepeat(t *testing.T) {
	s := NewShell(settled(state.Ready))
	t.Cleanup(s.Dispose)
	tt := ui.NewTester(s.View, 1024, 631)
	tt.KeyDown(ui.KeyF8)
	if s.Model.Overlay != state.Scenarios {
		t.Fatal("initial F8 must open preview")
	}
	tt.KeyRepeat(0, ui.KeyF8)
	if s.Model.Overlay != state.Scenarios {
		t.Fatal("holding F8 must not close preview on auto-repeat")
	}
	tt.KeyUp(ui.KeyF8)
	tt.KeyDown(ui.KeyF8)
	if s.Model.Overlay != state.NoOverlay {
		t.Fatal("a new F8 press must close preview")
	}
	tt.KeyRepeat(0, ui.KeyF8)
	if s.Model.Overlay != state.NoOverlay {
		t.Fatal("holding the closing press must not reopen preview")
	}
	tt.KeyUp(ui.KeyF8)
}

func TestNativeNewsSelectionDoesNotAddFilledAccentBlocks(t *testing.T) {
	s := NewShell(settled(state.Ready))
	t.Cleanup(s.Dispose)
	s.Model.News, s.Model.NewsBanners = state.NewsPresent, projectedBannerFixture()
	tt := ui.NewTester(s.View, 1280, 720)
	for _, label := range []string{"公告", "官方轮播封面 1"} {
		r, ok := tt.Find(label)
		if !ok {
			t.Fatalf("missing selector %s", label)
		}
		rr, gg, bb, _ := tt.Image().At(int(r.X+2), int(r.Y+2)).RGBA()
		if (uint64(rr)+uint64(gg)+uint64(bb))/3 > 100*257 {
			t.Fatalf("%s inherited a filled accent block instead of the quiet selection mark", label)
		}
	}
}
