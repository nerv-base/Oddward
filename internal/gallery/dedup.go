// SPDX-License-Identifier: GPL-3.0-or-later

package gallery

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

// ArchiveRelativePath returns the content-addressed path under the archive root:
// originals/<sha256[:2]>/<sha256><ext>.
func ArchiveRelativePath(sha256Hex, mimeType string) (string, error) {
	if !ValidSHA256Hex(sha256Hex) {
		return "", fmt.Errorf("%w: invalid sha256 digest", ErrInvalidMedia)
	}
	ext := ExtensionForMIME(mimeType)
	if ext == "" {
		return "", fmt.Errorf("%w: unsupported mime %q", ErrInvalidMedia, mimeType)
	}
	lower := strings.ToLower(sha256Hex)
	return path.Join("originals", lower[:2], lower+ext), nil
}

// ValidSHA256Hex checks that s is a 64-character lowercase hexadecimal digest.
func ValidSHA256Hex(s string) bool {
	if len(s) != 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// HashAndInspectFile opens path once, verifies it is a safe local regular file,
// validates its media header, streams its SHA256 digest, and verifies the file
// did not change while being read.
func HashAndInspectFile(ctx context.Context, path string) (info MediaInfo, digest string, st fileStat, err error) {
	if err = ctx.Err(); err != nil {
		return MediaInfo{}, "", fileStat{}, err
	}
	f, err := os.Open(path)
	if err != nil {
		return MediaInfo{}, "", fileStat{}, err
	}
	defer f.Close()

	before, err := statOpenFile(f)
	if err != nil {
		return MediaInfo{}, "", fileStat{}, err
	}
	if before.Reparse || before.IsDir {
		return MediaInfo{}, "", fileStat{}, ErrUnsafePath
	}
	if before.Size <= 0 {
		return MediaInfo{}, "", before, fmt.Errorf("%w: empty file", ErrInvalidMedia)
	}
	if before.Size > MaxImageBytes {
		return MediaInfo{}, "", before, fmt.Errorf("%w: %d bytes exceeds limit", ErrTooLarge, before.Size)
	}

	h := sha256.New()
	headerLimit := min(before.Size, int64(headerProbeBytes))
	headerBuf := make([]byte, headerLimit)
	n, readErr := io.ReadFull(f, headerBuf)
	if readErr != nil && readErr != io.EOF && readErr != io.ErrUnexpectedEOF {
		return MediaInfo{}, "", before, readErr
	}
	headerBuf = headerBuf[:n]
	_, _ = h.Write(headerBuf)

	// Validate header before reading the remainder of large files.
	info, inspectErr := InspectReader(bytesReader(headerBuf), before.Size)

	var copied int64 = int64(n)
	buf := make([]byte, 32<<10)
	for {
		if err := ctx.Err(); err != nil {
			return MediaInfo{}, "", before, err
		}
		nr, rerr := f.Read(buf)
		if nr > 0 {
			copied += int64(nr)
			if copied > MaxImageBytes {
				return MediaInfo{}, "", before, fmt.Errorf("%w: file grew past byte limit", ErrTooLarge)
			}
			_, _ = h.Write(buf[:nr])
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return MediaInfo{}, "", before, rerr
		}
	}

	after, err := statOpenFile(f)
	if err != nil {
		return MediaInfo{}, "", before, err
	}
	if after.Identity != before.Identity || after.Size != before.Size || !after.ModifiedAt.Equal(before.ModifiedAt) || copied != before.Size {
		return MediaInfo{}, "", after, ErrFileUnstable
	}
	digest = hex.EncodeToString(h.Sum(nil))
	if inspectErr != nil {
		return MediaInfo{MediaType: MediaImage, MIMEType: "application/octet-stream", ByteSize: before.Size}, digest, after, inspectErr
	}
	return info, digest, after, nil
}

// HashFile computes the streaming SHA256 digest of a regular file up to maxBytes.
func HashFile(ctx context.Context, path string, maxBytes int64) (string, int64, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	st, err := statOpenFile(f)
	if err != nil {
		return "", 0, err
	}
	if st.Reparse || st.IsDir {
		return "", 0, ErrUnsafePath
	}
	if maxBytes > 0 && st.Size > maxBytes {
		return "", st.Size, ErrTooLarge
	}
	h := sha256.New()
	buf := make([]byte, 32<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return "", 0, err
		}
		nr, rerr := f.Read(buf)
		if nr > 0 {
			total += int64(nr)
			if maxBytes > 0 && total > maxBytes {
				return "", total, ErrTooLarge
			}
			_, _ = h.Write(buf[:nr])
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return "", 0, rerr
		}
	}
	return hex.EncodeToString(h.Sum(nil)), total, nil
}

type byteSliceReader struct {
	b []byte
	i int
}

func bytesReader(b []byte) *byteSliceReader { return &byteSliceReader{b: b} }

func (r *byteSliceReader) Read(p []byte) (int, error) {
	if r.i >= len(r.b) {
		return 0, io.EOF
	}
	n := copy(p, r.b[r.i:])
	r.i += n
	return n, nil
}
