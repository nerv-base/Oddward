// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestArtAccentFollowsTheDominantVividHue(t *testing.T) {
	red, ok := ArtAccent(paintBands(160, 90,
		band{.7, color.RGBA{128, 128, 128, 255}}, band{.2, color.RGBA{220, 40, 40, 255}}, band{.1, color.RGBA{8, 8, 10, 255}}))
	if !ok || int(red.R) < int(red.G)+60 || int(red.R) < int(red.B)+60 {
		t.Fatalf("grey art with a red subject must yield a red tone, got %+v ok=%v", red, ok)
	}
	blue, ok := ArtAccent(paintBands(160, 90,
		band{.55, color.RGBA{30, 44, 120, 255}}, band{.25, color.RGBA{240, 240, 245, 255}}, band{.2, color.RGBA{12, 12, 16, 255}}))
	if !ok || blue.B <= blue.R || blue.B <= blue.G {
		t.Fatalf("navy art must yield a blue tone, got %+v ok=%v", blue, ok)
	}
	for _, tone := range []ui.Color{red, blue} {
		if tone.A != 255 || contrast(tone, ink) < 7 {
			t.Fatalf("launch tone %+v must be opaque and carry ink text at 7:1, got %.2f", tone, contrast(tone, ink))
		}
	}
	if _, ok := ArtAccent(paintBands(160, 90,
		band{.5, color.RGBA{20, 20, 20, 255}}, band{.3, color.RGBA{180, 180, 182, 255}}, band{.2, color.RGBA{250, 250, 250, 255}})); ok {
		t.Fatal("achromatic art has no hue to borrow; the default tone must apply")
	}
}

func TestPosterAccentReadsTheSameStillAsThePoster(t *testing.T) {
	dir := t.TempDir()
	still := paintBands(64, 36, band{1, color.RGBA{40, 170, 90, 255}})
	f, err := os.Create(filepath.Join(dir, "bg_0.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, still); err != nil {
		t.Fatal(err)
	}
	f.Close()
	tone, ok := PosterAccent(filepath.Join(dir, "bg.mp4"), "")
	if !ok || tone.G <= tone.R || tone.G <= tone.B {
		t.Fatalf("poster tone must come from the video's bg_0.png sibling, got %+v ok=%v", tone, ok)
	}
	if _, ok := PosterAccent("", filepath.Join(dir, "missing.png")); ok {
		t.Fatal("a missing still must fall back to the default tone")
	}
}
