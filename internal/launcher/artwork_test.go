// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func TestLoadArtworkIsOptionalAndReadOnly(t *testing.T) {
	if art, err := LoadArtwork(""); art != nil || err != nil {
		t.Fatal("empty artwork must be optional")
	}
	fixture := image.NewRGBA(image.Rect(0, 0, 220, 100))
	fixture.Set(20, 30, color.RGBA{R: 100, G: 150, B: 240, A: 255})
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, fixture); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "slogan.png")
	if err := os.WriteFile(path, encoded.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	art, err := LoadArtwork(path)
	if err != nil || art == nil {
		t.Fatalf("configured artwork not decoded: %v", err)
	}
	w, h := ui.ImageSize(art)
	if w != 220 || h != 100 {
		t.Fatalf("aspect ratio changed: %v x %v", w, h)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(after, encoded.Bytes()) {
		t.Fatal("source artwork was modified")
	}
}

func TestLoadArtworkRejectsUnavailableOrInvalidFiles(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "invalid.png")
	if err := os.WriteFile(bad, []byte("not an image"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(dir, "missing.png"), bad, dir} {
		if art, err := LoadArtwork(path); art != nil || err == nil {
			t.Errorf("invalid artwork accepted: %q", path)
		}
	}
}

func TestPosterDefaultsToSiblingImageWithoutChangingFiles(t *testing.T) {
	dir := t.TempDir()
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 16, 9))); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "bg_0.png")
	if err := os.WriteFile(path, encoded.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	art, err := LoadPoster(filepath.Join(dir, "bg.mp4"), "")
	if art == nil || err != nil {
		t.Fatalf("video sibling poster not loaded: %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(after, encoded.Bytes()) {
		t.Fatal("poster modified")
	}
	if art, err = LoadPoster("", path); art == nil || err != nil {
		t.Fatal("explicit still background must work without video")
	}
	if art, err = LoadPoster("", ""); art != nil || err != nil {
		t.Fatal("no assets must be optional")
	}
	if art, err = LoadPoster(filepath.Join(dir, "bg.mp4"), filepath.Join(dir, "wrong.png")); art != nil || err == nil {
		t.Fatal("explicit bad poster must not silently use another asset")
	}
}

type posterFixture struct {
	cached BackgroundAssets
	update BackgroundRefresh
}

func (f posterFixture) Cached() (BackgroundAssets, error) { return f.cached, nil }

func (f posterFixture) Refresh(context.Context) (BackgroundRefresh, error) { return f.update, nil }

type blockedBackground struct {
	cached  BackgroundAssets
	update  BackgroundRefresh
	started chan struct{}
	release chan struct{}
}

func (b *blockedBackground) Cached() (BackgroundAssets, error) { return b.cached, nil }

func (b *blockedBackground) Refresh(ctx context.Context) (BackgroundRefresh, error) {
	close(b.started)
	select {
	case <-b.release:
		return b.update, nil
	case <-ctx.Done():
		return BackgroundRefresh{}, ctx.Err()
	}
}

func waitForBackground(t *testing.T, tt *ui.Tester, wake <-chan struct{}, done func() bool) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for !done() {
		select {
		case <-wake:
			tt.Frame()
		case <-deadline:
			t.Fatal("background did not settle")
		}
	}
}

func TestCachedBackgroundAppearsWhileRefreshIsBlocked(t *testing.T) {
	s := NewShell(state.InitialNormal())
	defer s.Dispose()
	cached := image.NewRGBA(image.Rect(0, 0, 16, 9))
	for y := 0; y < 9; y++ {
		for x := 0; x < 16; x++ {
			cached.Set(x, y, color.RGBA{R: 20, G: 90, B: 230, A: 255})
		}
	}
	refreshed := image.NewRGBA(image.Rect(0, 0, 32, 18))
	for y := 0; y < 18; y++ {
		for x := 0; x < 32; x++ {
			refreshed.Set(x, y, color.RGBA{R: 220, G: 70, B: 30, A: 255})
		}
	}
	source := &blockedBackground{
		cached:  BackgroundAssets{Poster: cached},
		update:  BackgroundRefresh{Assets: BackgroundAssets{Poster: refreshed}, PosterChanged: true},
		started: make(chan struct{}), release: make(chan struct{}),
	}
	s.BackgroundSource = source
	wake := make(chan struct{}, 1)
	s.RequestFrame = func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	tt := ui.NewTester(s.View, 1024, 631)
	size := func() (int, int) {
		if s.Poster == nil {
			return 0, 0
		}
		return s.Poster.Size()
	}
	waitForBackground(t, tt, wake, func() bool { w, h := size(); return w == 16 && h == 9 })
	if s.Accent.A == 0 {
		t.Fatal("cached poster did not provide its accent")
	}
	select {
	case <-source.started:
	case <-time.After(2 * time.Second):
		t.Fatal("refresh did not start after the cached artwork")
	}
	// The blocked refresh must not blank or replace what is already visible.
	tt.Frame()
	if w, h := size(); w != 16 || h != 9 {
		t.Fatalf("cached poster lost while refresh was blocked: %dx%d", w, h)
	}
	close(source.release)
	waitForBackground(t, tt, wake, func() bool {
		w, h := size()
		return w == 32 && h == 18 && s.backgroundOut == nil
	})
	if s.Model.Media != state.MediaUnavailable || s.Player != nil {
		t.Fatalf("static fallback not settled: media=%v, player=%v", s.Model.Media, s.Player != nil)
	}
}

func TestOfficialBackgroundWakesIdleWindowAndShowsStaticFallback(t *testing.T) {
	s := NewShell(state.InitialNormal())
	defer s.Dispose()
	s.BackgroundSource = posterFixture{cached: BackgroundAssets{Poster: image.NewRGBA(image.Rect(0, 0, 16, 9))}}
	wake := make(chan struct{}, 1)
	s.RequestFrame = func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	tt := ui.NewTester(s.View, 1024, 631)
	deadline := time.After(2 * time.Second)
	for s.Poster == nil || s.backgroundOut != nil {
		select {
		case <-wake:
			tt.Frame()
		case <-deadline:
			t.Fatal("completed background fetch did not wake the native window")
		}
	}
	if s.Poster == nil || s.Player != nil || s.Model.Media != state.MediaUnavailable {
		t.Fatalf("static fallback not applied: poster=%v, player=%v, media=%v", s.Poster != nil, s.Player != nil, s.Model.Media)
	}
}

func TestNativeSloganPreservesAspectAndDoesNotCoverActions(t *testing.T) {
	s := NewShell(settled(state.Ready))
	t.Cleanup(s.Dispose)
	s.Slogan = ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 1097, 500)))
	for _, size := range [][2]int{{1024, 631}, {1280, 720}, {1600, 900}} {
		tt := ui.NewTester(s.View, size[0], size[1])
		slogan, ok := tt.Find("异环官网活动标语")
		if !ok {
			t.Fatal("official slogan missing from native image view")
		}
		if slogan.Y < 90 || slogan.Y+slogan.H > 380 || slogan.X+slogan.W > 600 {
			t.Fatalf("slogan blocks the character or action region: %+v", slogan)
		}
		if delta := slogan.W/slogan.H - 1097.0/500.0; delta < -.01 || delta > .01 {
			t.Fatalf("slogan stretched: %+v", slogan)
		}
		if clickFailed(tt, "启动游戏") {
			t.Fatal("slogan intercepted primary action")
		}
		s.Model = settled(state.Ready)
	}
}
