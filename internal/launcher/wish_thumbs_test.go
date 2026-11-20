// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func wishThumbFiles(t *testing.T, dataDir string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(dataDir, "cache", "wish-thumbs"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read thumbnail directory: %v", err)
	}
	return entries
}

// A restart must not fetch or decode an icon the previous run already decoded:
// the pixels are on disk, and the second shell's source is never asked.
func TestWishIconsSurviveRestartFromTheThumbnailCache(t *testing.T) {
	dir := t.TempDir()
	first := &pipelineSource{}
	h := newPipeHarness(t, first)
	h.s.DataDir = dir
	h.paint("11001")
	h.s.syncWishImages()
	h.waitResult(t)
	h.s.syncWishImages()
	if got := len(first.calls()); got != 1 {
		t.Fatalf("first run fetched %d times, want 1", got)
	}
	if h.s.wishIcons.cache["11001"] == nil {
		t.Fatal("first run never delivered the icon")
	}
	if entries := wishThumbFiles(t, dir); len(entries) != 1 {
		t.Fatalf("thumbnail directory holds %v, want one entry", entries)
	}

	second := &pipelineSource{err: errors.New("network disabled")}
	second.fail.Store(1)
	h2 := newPipeHarness(t, second)
	h2.s.DataDir = dir
	var traces []wishIconTrace
	h2.s.wishIcons.trace = func(trace wishIconTrace) { traces = append(traces, trace) }
	h2.paint("11001")
	h2.s.syncWishImages()
	h2.waitResult(t)
	h2.s.syncWishImages()
	if calls := second.calls(); len(calls) != 0 {
		t.Fatalf("restart asked the source for %v, want a thumbnail hit", calls)
	}
	if h2.s.wishIcons.cache["11001"] == nil {
		t.Fatal("restart never delivered the cached thumbnail")
	}
	if len(traces) != 1 || !traces[0].ThumbHit {
		t.Fatalf("trace %+v does not report the thumbnail hit", traces)
	}
}

// A thumbnail the cache cannot validate is a miss, not an error: the icon is
// fetched again, exactly as if the file had never existed.
func TestWishIconIgnoresUnreadableThumbnails(t *testing.T) {
	dir := t.TempDir()
	first := &pipelineSource{}
	h := newPipeHarness(t, first)
	h.s.DataDir = dir
	h.paint("11001")
	h.s.syncWishImages()
	h.waitResult(t)
	h.s.syncWishImages()
	entries := wishThumbFiles(t, dir)
	if len(entries) != 1 {
		t.Fatalf("thumbnail directory holds %v, want one entry", entries)
	}
	if err := os.WriteFile(filepath.Join(dir, "cache", "wish-thumbs", entries[0].Name()), []byte("junk"), 0o600); err != nil {
		t.Fatalf("corrupt thumbnail: %v", err)
	}
	second := &pipelineSource{}
	h2 := newPipeHarness(t, second)
	h2.s.DataDir = dir
	h2.paint("11001")
	h2.s.syncWishImages()
	h2.waitResult(t)
	h2.s.syncWishImages()
	if calls := second.calls(); len(calls) != 1 {
		t.Fatalf("source called %v, want exactly one refetch", calls)
	}
	if h2.s.wishIcons.cache["11001"] == nil {
		t.Fatal("a corrupt thumbnail left the icon missing")
	}
}
