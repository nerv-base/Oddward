// SPDX-License-Identifier: GPL-3.0-or-later

package gallery

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"
)

// Security limits enforced before any pixel decode allocation.
const (
	MaxImageBytes  int64 = 64 << 20   // 64 MiB per image file
	MaxImageSide   int   = 16384      // 16K maximum width or height
	MaxImagePixels int   = 64_000_000 // 64 MP (~256 MiB RGBA max)

	// headerProbeBytes bounds the prefix read to sniff magic bytes, decode
	// headers and parse JPEG EXIF segments without loading whole files.
	headerProbeBytes = 256 << 10
)

// IsIgnoredCandidate reports whether a file should be skipped during scanning
// (temporary files, hidden dotfiles, non-image extensions or derived FFMpeg
// video keyframes). Note: passing this filter does NOT grant trust — actual
// bytes are always sniffed and header-validated by InspectFile.
func IsIgnoredCandidate(sourceType SourceType, relPath string) bool {
	norm := strings.ToLower(strings.ReplaceAll(relPath, `\`, `/`))
	base := filepath.Base(norm)
	if base == "" || strings.HasPrefix(base, ".") || strings.HasSuffix(base, ".tmp") || strings.HasSuffix(base, ".part") {
		return true
	}
	if sourceType == SourceGameCache {
		// Real CN client writes video keyframe thumbnails under <UID>/FFMpeg/*_KeyFrame.jpg.
		if strings.Contains(norm, "/ffmpeg/") || strings.HasPrefix(norm, "ffmpeg/") || strings.Contains(base, "_keyframe.") {
			return true
		}
	}
	ext := strings.ToLower(filepath.Ext(base))
	switch ext {
	case ".png", ".jpg", ".jpeg", ".webp", ".bmp":
		return false
	default:
		return true
	}
}

// SniffImageMIME inspects actual leading bytes instead of trusting file names.
func SniffImageMIME(header []byte) (mimeType, format string, ok bool) {
	if len(header) >= 12 && string(header[0:4]) == "RIFF" && string(header[8:12]) == "WEBP" {
		return "image/webp", "webp", true
	}
	if len(header) >= 2 && header[0] == 'B' && header[1] == 'M' {
		return "image/bmp", "bmp", true
	}
	switch http.DetectContentType(header) {
	case "image/png":
		return "image/png", "png", true
	case "image/jpeg":
		return "image/jpeg", "jpeg", true
	case "image/webp":
		return "image/webp", "webp", true
	case "image/bmp":
		return "image/bmp", "bmp", true
	default:
		return "", "", false
	}
}

// ExtensionForMIME returns the canonical lowercase extension for a validated
// image MIME type.
func ExtensionForMIME(mimeType string) string {
	switch mimeType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/webp":
		return ".webp"
	case "image/bmp":
		return ".bmp"
	default:
		return ""
	}
}

// InspectReader validates an image stream given its known byte size.
func InspectReader(r io.Reader, byteSize int64) (MediaInfo, error) {
	if byteSize <= 0 {
		return MediaInfo{}, fmt.Errorf("%w: empty file", ErrInvalidMedia)
	}
	if byteSize > MaxImageBytes {
		return MediaInfo{}, fmt.Errorf("%w: %d bytes exceeds %d byte limit", ErrTooLarge, byteSize, MaxImageBytes)
	}
	probeLen := min(byteSize, int64(headerProbeBytes))
	buf := make([]byte, probeLen)
	n, err := io.ReadFull(r, buf)
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return MediaInfo{}, err
	}
	buf = buf[:n]
	if len(buf) < 8 {
		return MediaInfo{}, fmt.Errorf("%w: truncated header", ErrInvalidMedia)
	}
	mimeType, expectedFormat, ok := SniffImageMIME(buf)
	if !ok {
		return MediaInfo{}, fmt.Errorf("%w: unsupported media signature", ErrInvalidMedia)
	}
	cfg, decodedFormat, err := image.DecodeConfig(bytes.NewReader(buf))
	if err != nil {
		return MediaInfo{}, fmt.Errorf("%w: unreadable image header: %v", ErrInvalidMedia, err)
	}
	if decodedFormat != expectedFormat {
		return MediaInfo{}, fmt.Errorf("%w: sniffed %s does not match decoded %s", ErrInvalidMedia, expectedFormat, decodedFormat)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > MaxImageSide || cfg.Height > MaxImageSide {
		return MediaInfo{}, fmt.Errorf("%w: dimensions %dx%d out of bounds", ErrTooLarge, cfg.Width, cfg.Height)
	}
	if cfg.Width > MaxImagePixels/cfg.Height {
		return MediaInfo{}, fmt.Errorf("%w: %dx%d exceeds pixel budget", ErrTooLarge, cfg.Width, cfg.Height)
	}

	info := MediaInfo{
		MediaType:   MediaImage,
		MIMEType:    mimeType,
		Width:       cfg.Width,
		Height:      cfg.Height,
		ByteSize:    byteSize,
		Orientation: 1,
	}
	if expectedFormat == "jpeg" {
		if captured, orient := parseJPEGExif(buf); orient > 0 || !captured.IsZero() {
			info.CapturedAt = captured
			if orient >= 1 && orient <= 8 {
				info.Orientation = orient
				if orient >= 5 && orient <= 8 {
					info.Width, info.Height = info.Height, info.Width
				}
			}
		}
	}
	return info, nil
}

// parseJPEGExif performs bounded, read-only extraction of Orientation and
// DateTimeOriginal/CreateDate from a JPEG APP1 EXIF segment.
func parseJPEGExif(data []byte) (capturedAt time.Time, orientation int) {
	if len(data) < 4 || data[0] != 0xFF || data[1] != 0xD8 {
		return time.Time{}, 0
	}
	pos := 2
	for pos+4 <= len(data) {
		if data[pos] != 0xFF {
			return time.Time{}, 0
		}
		for pos < len(data) && data[pos] == 0xFF {
			pos++
		}
		if pos >= len(data) {
			return time.Time{}, 0
		}
		marker := data[pos]
		pos++
		if marker == 0xDA || marker == 0xD9 { // SOS or EOI
			return time.Time{}, 0
		}
		if marker >= 0xD0 && marker <= 0xD7 || marker == 0x01 {
			continue
		}
		if pos+2 > len(data) {
			return time.Time{}, 0
		}
		segLen := int(binary.BigEndian.Uint16(data[pos : pos+2]))
		if segLen < 2 || pos+segLen > len(data) {
			return time.Time{}, 0
		}
		payload := data[pos+2 : pos+segLen]
		if marker == 0xE1 && len(payload) > 6 && string(payload[:6]) == "Exif\x00\x00" {
			return parseTIFFExif(payload[6:])
		}
		pos += segLen
	}
	return time.Time{}, 0
}

func parseTIFFExif(tiff []byte) (time.Time, int) {
	if len(tiff) < 8 {
		return time.Time{}, 0
	}
	var bo binary.ByteOrder
	switch string(tiff[:2]) {
	case "II":
		bo = binary.LittleEndian
	case "MM":
		bo = binary.BigEndian
	default:
		return time.Time{}, 0
	}
	if bo.Uint16(tiff[2:4]) != 0x002A {
		return time.Time{}, 0
	}
	ifd0Offset := int(bo.Uint32(tiff[4:8]))
	if ifd0Offset < 8 || ifd0Offset+2 > len(tiff) {
		return time.Time{}, 0
	}
	orient := 0
	exifIFDOffset := 0
	var dtRaw, offsetRaw string

	readIFD := func(offset int, visit func(tag uint16, typ uint16, count uint32, valBytes []byte, valueOffset uint32)) {
		if offset < 8 || offset+2 > len(tiff) {
			return
		}
		num := int(bo.Uint16(tiff[offset : offset+2]))
		if num > 256 {
			num = 256
		}
		entryPos := offset + 2
		for i := 0; i < num && entryPos+12 <= len(tiff); i++ {
			tag := bo.Uint16(tiff[entryPos : entryPos+2])
			typ := bo.Uint16(tiff[entryPos+2 : entryPos+4])
			count := bo.Uint32(tiff[entryPos+4 : entryPos+8])
			valSlice := tiff[entryPos+8 : entryPos+12]
			valOff := bo.Uint32(valSlice)
			visit(tag, typ, count, valSlice, valOff)
			entryPos += 12
		}
	}

	readASCII := func(count, valOff uint32, valSlice []byte) string {
		if count == 0 || count > 64 {
			return ""
		}
		var raw []byte
		if count <= 4 {
			raw = valSlice[:count]
		} else {
			start := int(valOff)
			end := start + int(count)
			if start < 0 || end > len(tiff) || end < start {
				return ""
			}
			raw = tiff[start:end]
		}
		return strings.TrimRight(string(raw), "\x00 ")
	}

	readIFD(ifd0Offset, func(tag, typ uint16, count uint32, valSlice []byte, valOff uint32) {
		switch tag {
		case 0x0112: // Orientation
			if typ == 3 && count >= 1 {
				v := int(bo.Uint16(valSlice[:2]))
				if v >= 1 && v <= 8 {
					orient = v
				}
			}
		case 0x8769: // ExifIFD pointer
			if typ == 4 && count == 1 {
				exifIFDOffset = int(valOff)
			}
		}
	})

	if exifIFDOffset > 0 {
		readIFD(exifIFDOffset, func(tag, typ uint16, count uint32, valSlice []byte, valOff uint32) {
			if typ != 2 { // ASCII
				return
			}
			switch tag {
			case 0x9003: // DateTimeOriginal
				if s := readASCII(count, valOff, valSlice); s != "" {
					dtRaw = s
				}
			case 0x9004: // CreateDate (fallback only if DateTimeOriginal absent)
				if dtRaw == "" {
					if s := readASCII(count, valOff, valSlice); s != "" {
						dtRaw = s
					}
				}
			case 0x9011: // OffsetTimeOriginal
				if s := readASCII(count, valOff, valSlice); s != "" {
					offsetRaw = s
				}
			}
		})
	}

	return parseExifDateTime(dtRaw, offsetRaw), orient
}

func parseExifDateTime(dt, offset string) time.Time {
	if len(dt) < 19 {
		return time.Time{}
	}
	if offset != "" {
		if t, err := time.Parse("2006:01:02 15:04:05-07:00", dt+offset); err == nil {
			return t.UTC()
		}
	}
	if t, err := time.ParseInLocation("2006:01:02 15:04:05", dt, time.Local); err == nil {
		return t.UTC()
	}
	return time.Time{}
}
