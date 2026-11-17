// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

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
