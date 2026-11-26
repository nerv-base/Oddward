// SPDX-License-Identifier: GPL-3.0-or-later

package gallery

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/nerv-base/Oddward/internal/database"
	"github.com/nerv-base/Oddward/internal/database/dbtest"
)

func openTestDB(t *testing.T) *database.DB {
	t.Helper()
	return dbtest.Open(t)
}

func makePNGBytes(t testing.TB, w, h int, seed byte) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{
				R: byte((x + int(seed)*17) % 256),
				G: byte((y + int(seed)*31) % 256),
				B: byte((x ^ y ^ int(seed)) % 256),
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode synthetic png: %v", err)
	}
	return buf.Bytes()
}

func makeJPEGBytes(t testing.TB, w, h int, seed byte) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{
				R: byte((x*3 + int(seed)*13) % 256),
				G: byte((y*5 + int(seed)*29) % 256),
				B: byte((int(seed) * 47) % 256),
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 85}); err != nil {
		t.Fatalf("encode synthetic jpeg: %v", err)
	}
	return buf.Bytes()
}

// makeJPEGWithExif wraps a real JPEG with an APP1 EXIF segment carrying
// Orientation and DateTimeOriginal (+08:00).
func makeJPEGWithExif(t testing.TB, w, h int, seed byte, orient uint16, dateTimeOriginal, offsetTime string) []byte {
	t.Helper()
	base := makeJPEGBytes(t, w, h, seed)
	if len(base) < 2 || base[0] != 0xFF || base[1] != 0xD8 {
		t.Fatal("invalid base jpeg")
	}

	// Build minimal little-endian TIFF structure:
	// Header(8) + IFD0(2 + 2*12 + 4 = 30) + ExifIFD(2 + 2*12 + 4 = 30) + strings
	var tiff bytes.Buffer
	tiff.WriteString("II")
	_ = binary.Write(&tiff, binary.LittleEndian, uint16(0x002A))
	_ = binary.Write(&tiff, binary.LittleEndian, uint32(8))

	// IFD0 at offset 8: 2 entries (Orientation, ExifIFD)
	exifIFDOff := uint32(8 + 2 + 2*12 + 4)
	_ = binary.Write(&tiff, binary.LittleEndian, uint16(2))
	// Tag 0x0112 Orientation (SHORT, count 1)
	_ = binary.Write(&tiff, binary.LittleEndian, uint16(0x0112))
	_ = binary.Write(&tiff, binary.LittleEndian, uint16(3))
	_ = binary.Write(&tiff, binary.LittleEndian, uint32(1))
	_ = binary.Write(&tiff, binary.LittleEndian, orient)
	_ = binary.Write(&tiff, binary.LittleEndian, uint16(0))
	// Tag 0x8769 ExifIFD (LONG, count 1)
	_ = binary.Write(&tiff, binary.LittleEndian, uint16(0x8769))
	_ = binary.Write(&tiff, binary.LittleEndian, uint16(4))
	_ = binary.Write(&tiff, binary.LittleEndian, uint32(1))
	_ = binary.Write(&tiff, binary.LittleEndian, exifIFDOff)
	// Next IFD offset = 0
	_ = binary.Write(&tiff, binary.LittleEndian, uint32(0))

	// ExifIFD at exifIFDOff: 2 entries (DateTimeOriginal, OffsetTimeOriginal)
	dtBytes := append([]byte(dateTimeOriginal), 0)
	offBytes := append([]byte(offsetTime), 0)
	dtOff := exifIFDOff + 2 + 2*12 + 4
	offOff := dtOff + uint32(len(dtBytes))

	_ = binary.Write(&tiff, binary.LittleEndian, uint16(2))
	// Tag 0x9003 DateTimeOriginal (ASCII)
	_ = binary.Write(&tiff, binary.LittleEndian, uint16(0x9003))
	_ = binary.Write(&tiff, binary.LittleEndian, uint16(2))
	_ = binary.Write(&tiff, binary.LittleEndian, uint32(len(dtBytes)))
	_ = binary.Write(&tiff, binary.LittleEndian, dtOff)
	// Tag 0x9011 OffsetTimeOriginal (ASCII)
	_ = binary.Write(&tiff, binary.LittleEndian, uint16(0x9011))
	_ = binary.Write(&tiff, binary.LittleEndian, uint16(2))
	_ = binary.Write(&tiff, binary.LittleEndian, uint32(len(offBytes)))
	_ = binary.Write(&tiff, binary.LittleEndian, offOff)
	// Next IFD = 0
	_ = binary.Write(&tiff, binary.LittleEndian, uint32(0))

	tiff.Write(dtBytes)
	tiff.Write(offBytes)

	app1Payload := append([]byte("Exif\x00\x00"), tiff.Bytes()...)
	var out bytes.Buffer
	out.Write([]byte{0xFF, 0xD8, 0xFF, 0xE1})
	_ = binary.Write(&out, binary.BigEndian, uint16(len(app1Payload)+2))
	out.Write(app1Payload)
	out.Write(base[2:])
	return out.Bytes()
}

func writeTestFile(t testing.TB, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
