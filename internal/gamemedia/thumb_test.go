// SPDX-License-Identifier: GPL-3.0-or-later

package gamemedia

import (
	"encoding/binary"
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func thumbPixels(w, h int) *image.RGBA {
	pixels := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range pixels.Pix {
		pixels.Pix[i] = byte(i * 7)
	}
	return pixels
}

func TestThumbCacheRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cache := NewThumbCache(dir)
	want := thumbPixels(96, 48)
	if err := cache.Save("11001", 96, want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got := cache.Load("11001", 96)
	if got == nil || got.Rect != want.Rect || string(got.Pix) != string(want.Pix) {
		t.Fatalf("load returned %v, want %v", got, want.Rect)
	}
	if cache.Load("11002", 96) != nil {
		t.Fatal("an id that was never saved must miss")
	}
	if cache.Load("11001", 64) != nil {
		t.Fatal("a different edge must miss")
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 || !validThumbName(entries[0].Name()) {
		t.Fatalf("cache directory holds %v (%v)", entries, err)
	}
}

func TestThumbCacheRejectsCorruptFiles(t *testing.T) {
	base := thumbPixels(96, 96)
	cases := []struct {
		name    string
		corrupt func(path string, data []byte) []byte
	}{
		{name: "bad magic", corrupt: func(path string, data []byte) []byte { copy(data[0:4], "NOPE"); return data }},
		{name: "bad version", corrupt: func(path string, data []byte) []byte { data[4] = 9; return data }},
		{name: "payload truncated", corrupt: func(path string, data []byte) []byte { return data[:len(data)-1] }},
		{name: "payload padded", corrupt: func(path string, data []byte) []byte { return append(data, 0) }},
		{name: "edge mismatch", corrupt: func(path string, data []byte) []byte {
			binary.LittleEndian.PutUint16(data[5:7], 64)
			return data
		}},
		{name: "zero width", corrupt: func(path string, data []byte) []byte {
			binary.LittleEndian.PutUint16(data[7:9], 0)
			return data
		}},
		{name: "empty", corrupt: func(path string, data []byte) []byte { return nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			cache := NewThumbCache(dir)
			if err := cache.Save("11001", 96, base); err != nil {
				t.Fatalf("save: %v", err)
			}
			path := filepath.Join(dir, thumbName("11001", 96))
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			if err := os.WriteFile(path, tc.corrupt(path, data), 0o600); err != nil {
				t.Fatalf("write: %v", err)
			}
			if got := cache.Load("11001", 96); got != nil {
				t.Fatalf("corrupt entry (%s) was trusted", tc.name)
			}
		})
	}
}

func TestThumbCacheExpiresEntries(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	cache := NewThumbCache(dir)
	cache.now = func() time.Time { return now }
	if err := cache.Save("11001", 96, thumbPixels(96, 96)); err != nil {
		t.Fatalf("save: %v", err)
	}
	now = now.Add(thumbTTL - time.Hour)
	if cache.Load("11001", 96) == nil {
		t.Fatal("an entry inside the TTL must load")
	}
	now = now.Add(2 * time.Hour)
	if cache.Load("11001", 96) != nil {
		t.Fatal("an expired entry must miss")
	}
}

func TestThumbCacheKeepsIdsInsideItsDirectory(t *testing.T) {
	dir := t.TempDir()
	cache := NewThumbCache(dir)
	if err := cache.Save("../../evil", 96, thumbPixels(96, 96)); err != nil {
		t.Fatalf("save: %v", err)
	}
	if name := thumbName("../../evil", 96); strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") {
		t.Fatalf("thumbnail name %q escapes the directory", name)
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("cache directory holds %v (%v)", entries, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "..", "..", "evil")); err == nil {
		t.Fatal("id escaped the cache directory")
	}
}

func TestThumbCachePrunesOldestEntriesOnly(t *testing.T) {
	dir := t.TempDir()
	cache := NewThumbCache(dir)
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	cache.now = func() time.Time { return now }
	for i, id := range []string{"oldest", "middle", "newest"} {
		if err := cache.Save(id, 96, thumbPixels(96, 96)); err != nil {
			t.Fatalf("save %s: %v", id, err)
		}
		stamp := now.Add(time.Duration(i) * time.Hour)
		if err := os.Chtimes(filepath.Join(dir, thumbName(id, 96)), stamp, stamp); err != nil {
			t.Fatalf("chtimes: %v", err)
		}
	}
	foreign := filepath.Join(dir, "notes.txt")
	if err := os.WriteFile(foreign, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("write foreign file: %v", err)
	}
	info, err := os.Stat(filepath.Join(dir, thumbName("newest", 96)))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if err := cache.pruneTo(info.Size()); err != nil {
		t.Fatalf("prune: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	names := make(map[string]bool, len(entries))
	for _, entry := range entries {
		names[entry.Name()] = true
	}
	if !names[thumbName("newest", 96)] {
		t.Fatal("prune deleted the newest entry")
	}
	if names[thumbName("oldest", 96)] || names[thumbName("middle", 96)] {
		t.Fatalf("prune kept entries over budget: %v", names)
	}
	if !names["notes.txt"] {
		t.Fatal("prune deleted a file the cache does not own")
	}
}

func TestThumbCacheRejectsPixelsThatDoNotMatchTheEdge(t *testing.T) {
	cache := NewThumbCache(t.TempDir())
	for _, tc := range []struct {
		name   string
		edge   int
		pixels *image.RGBA
	}{
		{name: "too wide", edge: 48, pixels: thumbPixels(96, 48)},
		{name: "too tall", edge: 48, pixels: thumbPixels(48, 96)},
		{name: "nil pixels", edge: 96},
		{name: "zero edge", edge: 0, pixels: thumbPixels(1, 1)},
		{name: "absurd edge", edge: thumbMaxEdge + 1, pixels: thumbPixels(1, 1)},
	} {
		if err := cache.Save("11001", tc.edge, tc.pixels); err == nil {
			t.Fatalf("%s: Save accepted invalid pixels", tc.name)
		}
	}
}
