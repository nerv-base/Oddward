// SPDX-License-Identifier: GPL-3.0-or-later
package gamedl

import (
	"bytes"
	"encoding/binary"
	"encoding/hex"
	"hash/crc32"
	"os"

	"github.com/nerv-base/Oddward/internal/gameres"
)

var bitmapMagic = []byte{'O', 'D', 'W', 'B', 'M', '1', 0, 0}

type bitmap struct {
	object    gameres.Object
	chunkSize int64
	count     uint32
	bits      []byte
}

func newBitmap(obj gameres.Object, chunk int64) *bitmap {
	var count uint32
	if obj.Size > 0 {
		count = uint32((obj.Size-1)/chunk + 1)
	}
	return &bitmap{object: obj, chunkSize: chunk, count: count, bits: make([]byte, (count+7)/8)}
}
func (b *bitmap) has(i uint32) bool   { return b.bits[i/8]&(1<<(i%8)) != 0 }
func (b *bitmap) set(i uint32)        { b.bits[i/8] |= 1 << (i % 8) }
func (b *bitmap) size(i uint32) int64 { return min(b.chunkSize, b.object.Size-int64(i)*b.chunkSize) }
func (b *bitmap) downloaded() int64 {
	var size int64
	for i := uint32(0); i < b.count; i++ {
		if b.has(i) {
			size += b.size(i)
		}
	}
	return size
}
func (b *bitmap) marshal() []byte {
	data := make([]byte, 40+len(b.bits)+4)
	copy(data, bitmapMagic)
	hash, _ := hex.DecodeString(b.object.MD5)
	copy(data[8:24], hash)
	binary.LittleEndian.PutUint64(data[24:32], uint64(b.object.Size))
	binary.LittleEndian.PutUint32(data[32:36], b.count)
	var last int64
	if b.count > 0 {
		last = b.size(b.count - 1)
	}
	binary.LittleEndian.PutUint32(data[36:40], uint32(last))
	copy(data[40:], b.bits)
	binary.LittleEndian.PutUint32(data[len(data)-4:], crc32.ChecksumIEEE(data[:len(data)-4]))
	return data
}
func (b *bitmap) restore(data []byte) bool {
	expected := b.marshal()
	if len(data) != len(expected) || !bytes.Equal(data[:40], expected[:40]) || binary.LittleEndian.Uint32(data[len(data)-4:]) != crc32.ChecksumIEEE(data[:len(data)-4]) {
		return false
	}
	if b.count%8 != 0 && len(b.bits) > 0 && data[len(data)-5]>>uint(b.count%8) != 0 {
		return false
	}
	copy(b.bits, data[40:len(data)-4])
	return true
}
func loadBitmap(root *os.Root, path string, f *os.File, obj gameres.Object, chunk int64) (*bitmap, error) {
	b := newBitmap(obj, chunk)
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	data, readErr := readBounded(root, path, 1<<20)
	if readErr == nil && info.Size() == obj.Size && b.restore(data) {
		return b, nil
	}
	// A malformed bitmap or truncated data cannot become a completed chunk.
	// Unsafe paths are never removed as part of automatic repair.
	if err := checkPath(root, path); err != nil {
		return nil, err
	}
	if err := removeSafe(root, path); err != nil {
		return nil, err
	}
	if err := f.Truncate(0); err != nil {
		return nil, classify("reset part", path, err)
	}
	if err := f.Truncate(obj.Size); err != nil {
		return nil, classify("truncate part", path, err)
	}
	return b, nil
}
