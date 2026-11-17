// SPDX-License-Identifier: GPL-3.0-or-later
package imagecheck

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"math"
	"testing"
)

func fixture(t *testing.T, format string, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	im := image.NewRGBA(image.Rect(0, 0, w, h))
	var err error
	if format == "png" {
		err = png.Encode(&b, im)
	} else {
		err = jpeg.Encode(&b, im, nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}
func TestCheck(t *testing.T) {
	pngData := fixture(t, "png", 8, 8)
	jpegData := fixture(t, "jpeg", 8, 8)
	limits := Limits{MaxBytes: 1 << 20, MaxSide: 1024, MaxPixels: 1024 * 1024, Formats: []string{"png", "jpeg"}}
	for _, tc := range []struct {
		name   string
		data   []byte
		limits Limits
		valid  bool
		format string
	}{
		{"PNG", pngData, limits, true, "png"}, {"JPEG", jpegData, limits, true, "jpeg"},
		{"empty", nil, limits, false, ""}, {"text", []byte("not an image"), limits, false, ""},
		{"broken header", pngData[:20], limits, false, ""},
		{"bytes", pngData, Limits{MaxBytes: 1, MaxSide: 1024, MaxPixels: 1 << 20, Formats: []string{"png"}}, false, ""},
		{"side", pngData, Limits{MaxBytes: 1 << 20, MaxSide: 7, MaxPixels: 1 << 20, Formats: []string{"png"}}, false, ""},
		{"pixels", pngData, Limits{MaxBytes: 1 << 20, MaxSide: 1024, MaxPixels: 63, Formats: []string{"png"}}, false, ""},
		{"format", jpegData, Limits{MaxBytes: 1 << 20, MaxSide: 1024, MaxPixels: 1 << 20, Formats: []string{"png"}}, false, ""},
		{"invalid limits", pngData, Limits{}, false, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, format, err := Check(tc.data, tc.limits)
			if tc.valid {
				if err != nil || format != tc.format || cfg.Width != 8 || cfg.Height != 8 {
					t.Fatalf("Check = %+v %q %v", cfg, format, err)
				}
			} else if !errors.Is(err, ErrImage) {
				t.Fatalf("want ErrImage, got %v", err)
			}
		})
	}
}
func TestCheckPixelOverflow(t *testing.T) {
	// PNG encodes uint32 dimensions, so a 65536-square header exercises the
	// division-based budget guard without allocating a decoded image.
	data := fixture(t, "png", 1, 1)
	data[16], data[17], data[18], data[19] = 0, 1, 0, 0
	data[20], data[21], data[22], data[23] = 0, 1, 0, 0
	// DecodeConfig checks the IHDR CRC.
	fixPNGCRC(data)
	_, _, err := Check(data, Limits{MaxBytes: 1 << 20, MaxSide: math.MaxInt, MaxPixels: math.MaxInt32, Formats: []string{"png"}})
	if !errors.Is(err, ErrImage) {
		t.Fatalf("pixel overflow must fail: %v", err)
	}
}
func TestReadBounded(t *testing.T) {
	for _, n := range []int{0, 3, 4} {
		data, err := ReadBounded(bytes.NewReader([]byte("abc")), int64(n))
		if n < 3 {
			if err == nil {
				t.Fatal("over limit accepted")
			}
		} else if err != nil || string(data) != "abc" {
			t.Fatalf("%q %v", data, err)
		}
	}
	if _, err := ReadBounded(bytes.NewReader(nil), -1); err == nil {
		t.Fatal("negative limit accepted")
	}
	if _, err := ReadBounded(errorReader{}, 10); err == nil {
		t.Fatal("reader error lost")
	}
	if data, err := ReadBounded(bytes.NewReader([]byte("a")), math.MaxInt64); err != nil || string(data) != "a" {
		t.Fatalf("max limit: %q %v", data, err)
	}
}
func fixPNGCRC(data []byte) { binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29])) }

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
