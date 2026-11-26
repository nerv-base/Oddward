// SPDX-License-Identifier: GPL-3.0-or-later

package gallery

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"

	"github.com/nerv-base/Oddward/internal/imagecheck"
)

// customBackgroundLimits reuses Oddward's imagecheck bounds for launcher
// static backgrounds (PNG or JPEG, bounded bytes/sides/pixels).
var customBackgroundLimits = imagecheck.Limits{
	MaxBytes:  32 << 20,
	MaxSide:   8192,
	MaxPixels: 36_000_000,
	Formats:   []string{"png", "jpeg"},
}

// saveCustomBackgroundFile validates srcPath using imagecheck + full pixel
// decode, then writes an independent copy into <dataRoot>\background\custom-static.<ext>
// via same-volume temp file and atomic rename.
func saveCustomBackgroundFile(ctx context.Context, dataRoot, srcPath string) (string, image.Image, error) {
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	if dataRoot == "" || srcPath == "" {
		return "", nil, ErrInvalidSettings
	}
	cleanSrc, err := checkLocalPath(ctx, srcPath, false, false)
	if err != nil {
		return "", nil, err
	}
	f, err := os.Open(cleanSrc)
	if err != nil {
		return "", nil, err
	}
	st, err := statOpenFile(f)
	if err != nil {
		_ = f.Close()
		return "", nil, err
	}
	if st.Reparse || st.IsDir {
		_ = f.Close()
		return "", nil, ErrUnsafePath
	}
	if st.Size <= 0 {
		_ = f.Close()
		return "", nil, ErrInvalidMedia
	}
	raw, err := imagecheck.ReadBounded(f, MaxImageBytes)
	_ = f.Close()
	if err != nil {
		return "", nil, fmt.Errorf("%w: %v", ErrTooLarge, err)
	}

	var payload []byte
	var ext string
	var decoded image.Image

	// Fast path: PNG or JPEG directly validated via Oddward's imagecheck.Check.
	if _, format, cerr := imagecheck.Check(raw, customBackgroundLimits); cerr == nil {
		img, _, derr := image.Decode(bytes.NewReader(raw))
		if derr != nil {
			return "", nil, fmt.Errorf("%w: %v", ErrInvalidMedia, derr)
		}
		// Apply EXIF orientation if JPEG is rotated so the launcher backdrop renders upright.
		orient := 1
		if format == "jpeg" {
			if _, o := parseJPEGExif(raw); o >= 1 && o <= 8 {
				orient = o
			}
		}
		if orient > 1 {
			upright := toRGBAWithOrientation(img, orient)
			var buf bytes.Buffer
			if err := png.Encode(&buf, upright); err != nil {
				return "", nil, err
			}
			payload = buf.Bytes()
			ext = ".png"
			decoded = upright
		} else {
			payload = raw
			if format == "jpeg" {
				ext = ".jpg"
			} else {
				ext = ".png"
			}
			decoded = img
		}
	} else {
		// Fallback for valid WebP/BMP gallery photos: verify header, decode and
		// re-encode as bounded PNG validated through imagecheck.Check.
		info, ierr := InspectReader(bytes.NewReader(raw), int64(len(raw)))
		if ierr != nil || info.MediaType != MediaImage {
			return "", nil, fmt.Errorf("%w: %v", ErrInvalidMedia, cerr)
		}
		img, _, derr := image.Decode(bytes.NewReader(raw))
		if derr != nil {
			return "", nil, fmt.Errorf("%w: %v", ErrInvalidMedia, derr)
		}
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return "", nil, err
		}
		payload = buf.Bytes()
		if _, _, recheckErr := imagecheck.Check(payload, customBackgroundLimits); recheckErr != nil {
			return "", nil, fmt.Errorf("%w: %v", ErrTooLarge, recheckErr)
		}
		ext = ".png"
		decoded = img
	}

	bgDir := filepath.Join(filepath.Clean(dataRoot), "background")
	if err := os.MkdirAll(bgDir, 0o700); err != nil {
		return "", nil, err
	}
	if _, err := checkLocalPath(ctx, bgDir, true, false); err != nil {
		return "", nil, err
	}

	dest := filepath.Join(bgDir, "custom-static"+ext)
	tmp, err := os.CreateTemp(bgDir, ".bg-stage-*.tmp")
	if err != nil {
		return "", nil, err
	}
	tmpPath := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpPath)
		}
	}()
	if _, err := tmp.Write(payload); err != nil {
		return "", nil, err
	}
	if err := tmp.Sync(); err != nil {
		return "", nil, err
	}
	if err := tmp.Close(); err != nil {
		return "", nil, err
	}
	if err := replaceFile(tmpPath, dest); err != nil {
		return "", nil, err
	}
	committed = true

	// Remove stale alternate extension if user switched between .png and .jpg.
	for _, other := range []string{".png", ".jpg"} {
		if other != ext {
			_ = os.Remove(filepath.Join(bgDir, "custom-static"+other))
		}
	}
	return dest, decoded, nil
}

// loadCustomBackgroundFile revalidates a persisted custom background path via
// checkLocalPath + imagecheck.Check and decodes it for launcher startup.
func loadCustomBackgroundFile(ctx context.Context, path string) (image.Image, error) {
	if path == "" {
		return nil, nil
	}
	clean, err := checkLocalPath(ctx, path, false, false)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	f, err := os.Open(clean)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	st, err := statOpenFile(f)
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if st.Reparse || st.IsDir {
		_ = f.Close()
		return nil, ErrUnsafePath
	}
	raw, err := imagecheck.ReadBounded(f, customBackgroundLimits.MaxBytes)
	_ = f.Close()
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTooLarge, err)
	}
	if _, _, err := imagecheck.Check(raw, customBackgroundLimits); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidMedia, err)
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidMedia, err)
	}
	return img, nil
}

// clearCustomBackgroundFiles removes Oddward-managed custom-static.* files in
// <dataRoot>\background without touching user photos elsewhere.
func clearCustomBackgroundFiles(ctx context.Context, dataRoot string) {
	if dataRoot == "" {
		return
	}
	bgDir := filepath.Join(filepath.Clean(dataRoot), "background")
	if _, err := checkLocalPath(ctx, bgDir, true, false); err != nil {
		return
	}
	for _, ext := range []string{".png", ".jpg"} {
		p := filepath.Join(bgDir, "custom-static"+ext)
		if strings.HasPrefix(strings.ToLower(p), strings.ToLower(bgDir)) {
			_ = os.Remove(p)
		}
	}
}
