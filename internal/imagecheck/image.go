// SPDX-License-Identifier: GPL-3.0-or-later

// Package imagecheck validates bounded PNG and JPEG headers without decoding pixels.
package imagecheck

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"net/http"
)

// Limits bounds transferred bytes and the memory a subsequent pixel decode needs.
// Formats contains only the explicitly accepted "png" and/or "jpeg" formats.
type Limits struct {
	MaxBytes  int64
	MaxSide   int
	MaxPixels int
	Formats   []string
}

// ErrImage identifies every validation failure returned by Check.
var ErrImage = errors.New("imagecheck: invalid image")

// Check sniffs the actual bytes and reads only the image header. It does not
// guarantee a full pixel decode will succeed (for example for truncated IDAT).
func Check(data []byte, l Limits) (image.Config, string, error) {
	fail := func(reason string) (image.Config, string, error) {
		return image.Config{}, "", fmt.Errorf("%w: %s", ErrImage, reason)
	}
	if len(data) == 0 || l.MaxBytes <= 0 || int64(len(data)) > l.MaxBytes {
		return fail("empty image or byte limit exceeded")
	}
	if l.MaxSide <= 0 || l.MaxPixels <= 0 {
		return fail("limits must be positive")
	}
	format := ""
	switch http.DetectContentType(data) {
	case "image/png":
		format = "png"
	case "image/jpeg":
		format = "jpeg"
	}
	accepted := false
	for _, f := range l.Formats {
		if f != "png" && f != "jpeg" {
			return fail("unsupported format in limits")
		}
		if format == f {
			accepted = true
		}
	}
	if !accepted {
		return fail("image format is not allowed")
	}
	cfg, decoded, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return fail(fmt.Sprintf("unreadable image header: %v", err))
	}
	if decoded != format {
		return fail("sniffed and decoded formats differ")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > l.MaxSide || cfg.Height > l.MaxSide {
		return fail("invalid dimensions or side limit exceeded")
	}
	// Division avoids overflowing Width*Height, even on a 32-bit host.
	if cfg.Width > l.MaxPixels/cfg.Height {
		return fail("pixel limit exceeded")
	}
	return cfg, format, nil
}

// ReadBounded reads at most limit+1 bytes, rejecting a payload over limit.
func ReadBounded(r io.Reader, limit int64) ([]byte, error) {
	if limit < 0 {
		return nil, errors.New("imagecheck: negative byte limit")
	}
	n := limit
	if n < math.MaxInt64 {
		n++
	}
	data, err := io.ReadAll(io.LimitReader(r, n))
	if err != nil {
		return nil, fmt.Errorf("imagecheck: read: %w", err)
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("imagecheck: payload exceeds %d bytes", limit)
	}
	return data, nil
}
