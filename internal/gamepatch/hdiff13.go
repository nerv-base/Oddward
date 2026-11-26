// SPDX-License-Identifier: GPL-3.0-or-later
package gamepatch

// This decoder is independently implemented from the HDIFF13 wire format.
// Format inspection: HDiffPatch's public format and offline MIT reference.
// No reference implementation code or game data is included here.

import (
	"bufio"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	"math"
)

const (
	maxSectionBuffer = 256 << 10
	oldWindowSize    = 1 << 20
	oldDirectCutover = 256 << 10
)

type section struct {
	reader *bufio.Reader
	left   int64
}

func newSection(f io.ReaderAt, offset, size int64) *section {
	bufSize := int(min(size, maxSectionBuffer))
	if bufSize < 16 {
		bufSize = 16
	}
	return &section{bufio.NewReaderSize(io.NewSectionReader(f, offset, size), bufSize), size}
}
func (s *section) ReadByte() (byte, error) {
	if s.left == 0 {
		return 0, fmt.Errorf("%w: truncated section", ErrCorruptPatch)
	}
	b, err := s.reader.ReadByte()
	if err != nil {
		return 0, fmt.Errorf("%w: section read: %w", ErrCorruptPatch, err)
	}
	s.left--
	return b, nil
}
func (s *section) read(p []byte) error {
	if int64(len(p)) > s.left {
		return fmt.Errorf("%w: section underflow", ErrCorruptPatch)
	}
	n, err := io.ReadFull(s.reader, p)
	s.left -= int64(n)
	if err != nil {
		return fmt.Errorf("%w: section read: %w", ErrCorruptPatch, err)
	}
	return nil
}

// uintTag reads one tagged, most-significant-group-first base-128 integer.
// All offsets remain signed int64 so every subsequent bound check is safe.
func uintTag(s *section, bits uint) (int64, byte, error) {
	first, err := s.ReadByte()
	if err != nil {
		return 0, 0, err
	}
	payloadBits := 7 - bits
	value := int64(first & byte((1<<payloadBits)-1))
	tag := byte(0)
	if bits != 0 {
		tag = first >> (8 - bits)
	}
	more := first&(1<<payloadBits) != 0
	for groups := 1; more; groups++ {
		if groups >= 10 {
			return 0, 0, fmt.Errorf("%w: overlong integer", ErrCorruptPatch)
		}
		next, err := s.ReadByte()
		if err != nil {
			return 0, 0, err
		}
		low := int64(next & 127)
		if value > (math.MaxInt64-low)/128 {
			return 0, 0, fmt.Errorf("%w: integer overflow", ErrCorruptPatch)
		}
		value = value*128 + low
		more = next&128 != 0
	}
	return value, tag, nil
}
func uintValue(s *section) (int64, error) { v, _, err := uintTag(s, 0); return v, err }

type patchHeader struct {
	newSize, oldSize, coverCount int64
	offsets, sizes               [4]int64
}

func readHeader(f io.ReaderAt, size int64) (patchHeader, error) {
	var h patchHeader
	s := newSection(f, 0, size)
	var magic [9]byte
	if err := s.read(magic[:]); err != nil {
		return h, err
	}
	if string(magic[:]) != "HDIFF13&\x00" {
		return h, ErrUnsupportedPatch
	}
	for _, dest := range []*int64{&h.newSize, &h.oldSize, &h.coverCount} {
		v, err := uintValue(s)
		if err != nil {
			return h, err
		}
		*dest = v
	}
	for i := range h.sizes {
		v, err := uintValue(s)
		if err != nil {
			return h, err
		}
		h.sizes[i] = v
		c, err := uintValue(s)
		if err != nil {
			return h, err
		}
		if c != 0 {
			return h, fmt.Errorf("%w: section=%d compression=%d", ErrUnsupportedPatch, i, c)
		}
	}
	offset := size - s.left
	for i, n := range h.sizes {
		h.offsets[i] = offset
		if n > size-offset {
			return h, fmt.Errorf("%w: section=%d exceeds blob", ErrCorruptPatch, i)
		}
		offset += n
	}
	if offset != size || h.coverCount > h.sizes[0]/3 {
		return h, fmt.Errorf("%w: section lengths or cover count", ErrCorruptPatch)
	}
	return h, nil
}

type rle struct {
	controls, codes *section
	remaining       int64
	kind, value     byte
	scratch         [32 << 10]byte
}

func (r *rle) consume(ctx context.Context, data []byte, add bool) error {
	for len(data) > 0 {
		if err := ctx.Err(); err != nil {
			return err
		}
		if r.remaining == 0 {
			n, kind, err := uintTag(r.controls, 2)
			if err != nil {
				return err
			}
			if n == math.MaxInt64 {
				return fmt.Errorf("%w: RLE length overflow", ErrCorruptPatch)
			}
			r.remaining = n + 1
			r.kind = kind
			r.value = 0
			if kind == 1 {
				r.value = 255
			}
			if kind == 2 {
				r.value, err = r.codes.ReadByte()
				if err != nil {
					return err
				}
			}
		}
		count := len(data)
		if int64(count) > r.remaining {
			count = int(r.remaining)
		}
		if r.kind == 3 {
			for pos := 0; pos < count; {
				if err := ctx.Err(); err != nil {
					return err
				}
				n := min(count-pos, len(r.scratch))
				delta := r.scratch[:n]
				if err := r.codes.read(delta); err != nil {
					return err
				}
				if add {
					for j, b := range delta {
						data[pos+j] += b
					}
				}
				pos += n
			}
		} else if add && r.value != 0 {
			for j := 0; j < count; j++ {
				data[j] += r.value
			}
		}
		data = data[count:]
		r.remaining -= int64(count)
	}
	return nil
}

type bufferedOld struct {
	src      io.ReaderAt
	size     int64
	buf      []byte
	winStart int64
	winEnd   int64
}

func (b *bufferedOld) readAt(p []byte, offset int64) error {
	if len(p) == 0 {
		return nil
	}
	end := offset + int64(len(p))
	if offset >= b.winStart && end <= b.winEnd {
		copy(p, b.buf[offset-b.winStart:end-b.winStart])
		return nil
	}
	if len(p) >= oldDirectCutover {
		n, err := b.src.ReadAt(p, offset)
		if n == len(p) && (err == nil || err == io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		return io.ErrUnexpectedEOF
	}
	if b.buf == nil {
		b.buf = make([]byte, oldWindowSize)
	}
	want := min(int64(len(b.buf)), b.size-offset)
	if want < int64(len(p)) {
		want = int64(len(p))
	}
	if want > int64(len(b.buf)) {
		want = int64(len(b.buf))
	}
	n, err := b.src.ReadAt(b.buf[:want], offset)
	if int64(n) < int64(len(p)) {
		if err != nil {
			return err
		}
		return io.ErrUnexpectedEOF
	}
	b.winStart, b.winEnd = offset, offset+int64(n)
	copy(p, b.buf[:len(p)])
	return nil
}

func decode(ctx context.Context, old, patch io.ReaderAt, out io.Writer, h patchHeader, buffer []byte, report func(int64, int64)) (string, error) {
	sections := [4]*section{}
	for i := range sections {
		sections[i] = newSection(patch, h.offsets[i], h.sizes[i])
	}
	covers, literals := sections[0], sections[3]
	delta := rle{controls: sections[1], codes: sections[2]}
	oldSrc := bufferedOld{src: old, size: h.oldSize}
	bufOut := bufio.NewWriterSize(out, len(buffer))
	outHash := md5.New()
	written := int64(0)
	// The delta stream spans ALL output bytes; literal deltas are consumed but
	// ignored. Old-file bytes receive unsigned byte addition modulo 256.
	transfer := func(offset, count int64, fromOld bool) error {
		if count < 0 || count > h.newSize-written {
			return fmt.Errorf("%w: output range exceeds newSize", ErrCorruptPatch)
		}
		for count > 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			n := min(int64(len(buffer)), count)
			block := buffer[:n]
			if fromOld {
				if err := oldSrc.readAt(block, offset); err != nil {
					return err
				}
			} else if err := literals.read(block); err != nil {
				return err
			}
			if err := delta.consume(ctx, block, fromOld); err != nil {
				return err
			}
			outHash.Write(block)
			if bufOut.Buffered() == 0 && len(block) >= len(buffer) {
				if err := writeFull(out, block); err != nil {
					return err
				}
			} else {
				if err := writeFull(bufOut, block); err != nil {
					return err
				}
				if bufOut.Buffered() >= len(buffer) {
					if err := bufOut.Flush(); err != nil {
						return err
					}
				}
			}
			written += n
			offset += n
			count -= n
			if report != nil {
				report(written, h.newSize)
			}
		}
		return nil
	}
	oldEnd := int64(0)
	for i := int64(0); i < h.coverCount; i++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		distance, sign, err := uintTag(covers, 1)
		if err != nil {
			return "", err
		}
		position := int64(0)
		if sign == 0 {
			if distance > h.oldSize-oldEnd {
				return "", fmt.Errorf("%w: cover=%d forward old offset", ErrCorruptPatch, i)
			}
			position = oldEnd + distance
		} else {
			if distance > oldEnd {
				return "", fmt.Errorf("%w: cover=%d negative old offset", ErrCorruptPatch, i)
			}
			position = oldEnd - distance
		}
		gap, err := uintValue(covers)
		if err != nil {
			return "", err
		}
		length, err := uintValue(covers)
		if err != nil {
			return "", err
		}
		if length > h.oldSize-position {
			return "", fmt.Errorf("%w: cover=%d old range", ErrCorruptPatch, i)
		}
		if err = transfer(0, gap, false); err != nil {
			return "", err
		}
		if err = transfer(position, length, true); err != nil {
			return "", err
		}
		oldEnd = position + length
	}
	if err := transfer(0, h.newSize-written, false); err != nil {
		return "", err
	}
	if covers.left != 0 || literals.left != 0 || delta.controls.left != 0 || delta.codes.left != 0 || delta.remaining != 0 {
		return "", fmt.Errorf("%w: unconsumed sections or RLE run", ErrCorruptPatch)
	}
	if err := bufOut.Flush(); err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return hex.EncodeToString(outHash.Sum(nil)), nil
}
