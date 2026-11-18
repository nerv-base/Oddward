// SPDX-License-Identifier: GPL-3.0-or-later
package gamepatch

import (
	"crypto/md5"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/nerv-base/Oddward/internal/gameres"
)

func object(b []byte) gameres.Object {
	return gameres.Object{MD5: fmt.Sprintf("%x", md5.Sum(b)), Size: int64(len(b))}
}

// packed encodes the public HDIFF13 big-endian base-128 integer, with tag
// bits preceding the continuation bit in the first byte.
func packed(value uint64, tagBits uint, tag byte) []byte {
	var tail []byte
	for value >= uint64(1)<<(7-tagBits) {
		tail = append(tail, byte(value&127))
		value >>= 7
	}
	first := byte(value) | tag<<(8-tagBits)
	if len(tail) > 0 {
		first |= 1 << (7 - tagBits)
	}
	out := []byte{first}
	for i := len(tail) - 1; i >= 0; i-- {
		b := tail[i]
		if i > 0 {
			b |= 128
		}
		out = append(out, b)
	}
	return out
}
func wire(newSize, oldSize, count uint64, sections [4][]byte) []byte {
	out := []byte("HDIFF13&\x00")
	for _, n := range []uint64{newSize, oldSize, count} {
		out = append(out, packed(n, 0, 0)...)
	}
	for _, s := range sections {
		out = append(out, packed(uint64(len(s)), 0, 0)...)
		out = append(out, 0)
	}
	for _, s := range sections {
		out = append(out, s...)
	}
	return out
}
func literalPatch(old, new []byte) []byte {
	var control []byte
	if len(new) > 0 {
		control = packed(uint64(len(new)-1), 2, 0)
	}
	return wire(uint64(len(new)), uint64(len(old)), 0, [4][]byte{nil, control, nil, new})
}
func fixture(t *testing.T, old, patch, new []byte) Options {
	t.Helper()
	dir := t.TempDir()
	opts := Options{OldPath: filepath.Join(dir, "old"), PatchPath: filepath.Join(dir, "patch"), OutputPath: filepath.Join(dir, "new"), OldExpect: object(old), PatchExpect: object(patch), Expect: object(new)}
	for path, b := range map[string][]byte{opts.OldPath: old, opts.PatchPath: patch} {
		if err := os.WriteFile(path, b, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return opts
}
func unchanged(t *testing.T, path string, want []byte) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil || string(b) != string(want) {
		t.Fatalf("source changed: %q %v", b, err)
	}
}
func absent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("output exists or unexpected stat error: %v", err)
	}
}
