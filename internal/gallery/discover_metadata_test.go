// SPDX-License-Identifier: GPL-3.0-or-later

package gallery

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestDiscoverSourcesFindsMultiUIDVariantsAndExported(t *testing.T) {
	ctx := context.Background()
	tmp := t.TempDir()
	contentRoot := filepath.Join(tmp, "Game", "Client", "WindowsNoEditor")
	uid1Shots := filepath.Join(contentRoot, "Selfie", "100001234567", "ScreenShots")
	uid2Shots := filepath.Join(contentRoot, "Selfie", "98765", "ScreenShots")
	uid1FFMpeg := filepath.Join(contentRoot, "Selfie", "100001234567", "FFMpeg")
	htVariant := filepath.Join(contentRoot, "HT", "Selfie", "555555555", "ScreenShots")
	picsRoot := filepath.Join(tmp, "Pictures")
	exportedDir := filepath.Join(picsRoot, "NevernessToEverness")

	for _, d := range []string{uid1Shots, uid2Shots, uid1FFMpeg, htVariant, exportedDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	sources, err := DiscoverSources(ctx, DiscoveryOptions{
		AutoDiscoverGame:    true,
		ContentRootOverride: contentRoot,
		PicturesDirOverride: picsRoot,
	})
	if err != nil {
		t.Fatalf("DiscoverSources: %v", err)
	}

	gotUIDs := make(map[string]SourceType)
	var sawFFMpeg bool
	for _, s := range sources {
		if filepath.Base(s.RootPath) == "FFMpeg" {
			sawFFMpeg = true
		}
		gotUIDs[s.UID] = s.SourceType
	}
	if !sawFFMpeg {
		t.Fatalf("P1 must discover real CN FFMpeg video directory: %+v", sources)
	}
	if len(sources) != 5 {
		t.Fatalf("discovered %d sources, want 5: %+v", len(sources), sources)
	}
	for _, uid := range []string{"100001234567", "98765", "555555555"} {
		if gotUIDs[uid] != SourceGameCache {
			t.Fatalf("missing game_cache source for UID %s: %+v", uid, sources)
		}
	}
	if gotUIDs[""] != SourceExported {
		t.Fatalf("exported source must have empty (unknown) UID: %+v", sources)
	}
}

func TestPathSecurityRejectsTraversalReservedAndJunction(t *testing.T) {
	ctx := context.Background()
	for _, bad := range []string{
		"",
		`relative\path`,
		`\\server\share\photos`,
		`C:\foo\..\bar`,
		`C:\CON\photos`,
		`C:\NUL`,
		`C:\COM1\img.png`,
		"C:\\bad\x00path",
	} {
		if err := ValidPathSyntax(bad); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("ValidPathSyntax(%q) = %v, want ErrUnsafePath", bad, err)
		}
	}
	for _, badRel := range []string{"", "../escape.png", `..\escape.png`, `/abs.png`, `C:\abs.png`, `a/../b.png`} {
		if _, err := NormalizeRelativePath(badRel); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("NormalizeRelativePath(%q) = %v, want ErrUnsafePath", badRel, err)
		}
	}

	// Create a real Windows directory junction and ensure discovery & validation reject it.
	tmp := t.TempDir()
	realTarget := filepath.Join(tmp, "outside")
	if err := os.MkdirAll(realTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(realTarget, "secret.png"), makePNGBytes(t, 16, 16, 1))

	junction := filepath.Join(tmp, "junction")
	cmd := exec.Command("cmd", "/c", "mklink", "/J", junction, realTarget)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("mklink /J unavailable: %v (%s)", err, string(out))
	}
	if _, err := ValidateCustomSource(ctx, junction); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("ValidateCustomSource accepted junction: %v", err)
	}
	if _, err := checkSafeSubpath(ctx, tmp, "junction/secret.png", false, false); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("checkSafeSubpath accepted file behind junction: %v", err)
	}
}

func TestInspectReaderValidatesMagicEXIFAndLeavesUnknownCaptureTimeNull(t *testing.T) {
	// 1. Plain PNG (like real CN NTE screenshots): no EXIF -> CapturedAt MUST remain zero!
	pngBytes := makePNGBytes(t, 64, 36, 7)
	info, err := InspectReader(bytes.NewReader(pngBytes), int64(len(pngBytes)))
	if err != nil {
		t.Fatalf("InspectReader valid png: %v", err)
	}
	if info.MIMEType != "image/png" || info.Width != 64 || info.Height != 36 {
		t.Fatalf("unexpected png info: %+v", info)
	}
	if !info.CapturedAt.IsZero() {
		t.Fatalf("PNG without EXIF must keep CapturedAt zero (NULL), got %v", info.CapturedAt)
	}

	// 2. JPEG with real EXIF DateTimeOriginal + Orientation=6 (90° CW -> swapped W/H).
	jpgExif := makeJPEGWithExif(t, 80, 40, 3, 6, "2026:11:24 16:52:02", "+08:00")
	info, err = InspectReader(bytes.NewReader(jpgExif), int64(len(jpgExif)))
	if err != nil {
		t.Fatalf("InspectReader exif jpeg: %v", err)
	}
	if info.MIMEType != "image/jpeg" || info.Orientation != 6 || info.Width != 40 || info.Height != 80 {
		t.Fatalf("unexpected exif jpeg info: %+v", info)
	}
	wantTime := time.Date(2026, 11, 24, 8, 52, 2, 0, time.UTC)
	if !info.CapturedAt.Equal(wantTime) {
		t.Fatalf("CapturedAt = %v, want %v", info.CapturedAt, wantTime)
	}

	// 3. Spoofed file (.png containing plain text) rejected by magic check.
	fake := []byte("not a png image at all 1234567890")
	if _, err := InspectReader(bytes.NewReader(fake), int64(len(fake))); !errors.Is(err, ErrInvalidMedia) {
		t.Fatalf("spoofed media err = %v, want ErrInvalidMedia", err)
	}

	// 4. Truncated PNG header rejected.
	if _, err := InspectReader(bytes.NewReader(pngBytes[:12]), 12); !errors.Is(err, ErrInvalidMedia) {
		t.Fatalf("truncated png err = %v, want ErrInvalidMedia", err)
	}

	// 5. Oversized file rejected before allocation.
	if _, err := InspectReader(bytes.NewReader(pngBytes), MaxImageBytes+1); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized media err = %v, want ErrTooLarge", err)
	}

	// 6. Derived video keyframe ignored in game cache.
	if !IsIgnoredCandidate(SourceGameCache, "FFMpeg/26_11_24_16_49_20_KeyFrame.jpg") {
		t.Fatal("FFMpeg keyframe jpg must be ignored in game cache")
	}
	if !IsIgnoredCandidate(SourceGameCache, "26_11_24_16_49_20_KeyFrame.jpg") {
		t.Fatal("_KeyFrame.jpg must be ignored in game cache")
	}
	if IsIgnoredCandidate(SourceGameCache, "26_11_24_16_52_02abcd.png") {
		t.Fatal("real ScreenShots png must not be ignored")
	}
}
