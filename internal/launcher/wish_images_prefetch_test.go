// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nerv-base/Oddward/internal/gacha"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// prefetchArchive builds a loaded archive with perPool records in every pool,
// ordered so a column's record index is the row the page shows there.
func prefetchArchive(perPool int) gacha.Archive {
	archive := gacha.Archive{RoleID: "100012345"}
	for _, pool := range []gacha.Pool{gacha.Limited, gacha.Permanent, gacha.Weapon} {
		for row := 0; row < perPool; row++ {
			archive.Records = append(archive.Records, gacha.Record{Pool: pool, ItemID: prefetchID(pool, row)})
		}
	}
	return archive
}

func prefetchID(pool gacha.Pool, row int) string {
	return fmt.Sprintf("%s-%d", pool, row)
}

// loadWishArchive hands a finished load to the shell exactly as the worker
// would and lets syncWish apply it.
func loadWishArchive(t *testing.T, s *Shell, archive gacha.Archive) {
	t.Helper()
	s.wish.out <- wishArchiveResult{effect: state.Effect{Kind: state.WishLoad, Token: s.Model.Wish.Token}, archive: archive}
	s.syncWish()
}

// firstScreenIDs is the request order the prefetch must produce: one row of
// every pool at a time, so the three columns fill together.
func firstScreenIDs(rows int) []string {
	ids := make([]string, 0, rows*3)
	for row := 0; row < rows; row++ {
		for _, pool := range []gacha.Pool{gacha.Limited, gacha.Permanent, gacha.Weapon} {
			ids = append(ids, prefetchID(pool, row))
		}
	}
	return ids
}

func TestWishLoadPrefetchesFirstScreenIconsInColumnOrder(t *testing.T) {
	h := newPipeHarness(t, &pipelineSource{release: make(chan struct{})})
	h.s.viewportW, h.s.viewportH = 1280, 720
	rows := wishFirstScreenRows(1280, 720)
	if rows < 1 {
		t.Fatalf("first screen estimate for 1280x720 is %d rows", rows)
	}
	loadWishArchive(t, h.s, prefetchArchive(12))
	want := firstScreenIDs(rows)
	if got := h.s.wishIcons.wanted; !sameIDs(got, want) {
		t.Fatalf("prefetch order = %v, want %v", got, want)
	}
	h.s.syncWishImages()
	if got := len(h.s.wishIcons.inflight); got != wishIconWorkers {
		t.Fatalf("%d loads in flight after prefetch, want %d", got, wishIconWorkers)
	}
	for _, id := range want[:wishIconWorkers] {
		if h.s.wishIcons.inflight[id] == nil {
			t.Fatalf("prefetch did not start %q first", id)
		}
	}
}

func TestWishPrefetchSurvivesFramesWithoutPaint(t *testing.T) {
	h := newPipeHarness(t, &pipelineSource{release: make(chan struct{})})
	h.s.viewportW, h.s.viewportH = 1280, 720
	loadWishArchive(t, h.s, prefetchArchive(12))
	h.s.syncWishImages()
	if got := len(h.s.wishIcons.inflight); got != wishIconWorkers {
		t.Fatalf("%d loads in flight, want %d", got, wishIconWorkers)
	}
	// The archive paints one frame after it loads, so the frame between the
	// prefetch and the first paint must not cancel what the prefetch started.
	h.s.syncWishImages()
	if got := len(h.s.wishIcons.inflight); got != wishIconWorkers {
		t.Fatalf("prefetched loads were cancelled before their first paint: %d left", got)
	}
}

func TestWishPrefetchOnlyRunsOnTheWishPage(t *testing.T) {
	h := newPipeHarness(t, &pipelineSource{release: make(chan struct{})})
	h.s.Dispatch(state.Event{Kind: state.Navigate, Page: state.Home})
	if h.s.wishIconsEnabled() {
		t.Fatal("wish icons enabled off the wish page")
	}
	h.s.viewportW, h.s.viewportH = 1280, 720
	h.s.prefetchWishIcons(prefetchArchive(12))
	if len(h.s.wishIcons.wanted) != 0 {
		t.Fatalf("off-page prefetch queued %v", h.s.wishIcons.wanted)
	}
}

func TestWishFirstScreenRowsFollowsWindowSize(t *testing.T) {
	if got := wishFirstScreenRows(0, 720); got != 0 {
		t.Fatalf("rows for a zero-width window = %d", got)
	}
	short, tall := wishFirstScreenRows(1280, 720), wishFirstScreenRows(1280, 1080)
	if short < 1 || tall <= short {
		t.Fatalf("rows 720p=%d 1080p=%d, want a taller window to show more rows", short, tall)
	}
	if tall > wishIconFirstScreenMaxRows {
		t.Fatalf("rows 1080p=%d exceeds the cap %d", tall, wishIconFirstScreenMaxRows)
	}
	if narrow := wishFirstScreenRows(720, 720); narrow < 1 {
		t.Fatalf("narrow layout rows = %d", narrow)
	}
}

// TestWishPaintLeavesBelowFoldRowsUnrequested is the regression for the first
// paint requesting every record of the archive before any row has bounds.
func TestWishPaintLeavesBelowFoldRowsUnrequested(t *testing.T) {
	archive := prefetchArchive(40)
	src := &pipelineSource{release: make(chan struct{})}
	s := NewShell(state.Model{Page: state.WishAnalysis})
	defer s.Dispose()
	s.WishImages = src
	s.RequestFrame = func() {}
	s.wish.started = true // drive the archive by hand, not through the backend
	s.wish.out <- wishArchiveResult{effect: state.Effect{Kind: state.WishLoad, Token: s.Model.Wish.Token}, archive: archive}
	tt := ui.NewTester(s.View, 1280, 720)
	for i := 0; i < 3; i++ {
		tt.Frame()
	}
	close(src.release)
	tt.Frame()

	queued := map[string]bool{}
	for _, id := range src.calls() {
		queued[id] = true
	}
	for _, id := range s.wishIcons.wanted {
		queued[id] = true
	}
	if !queued[prefetchID(gacha.Limited, 0)] {
		t.Fatal("the first record never requested its icon")
	}
	if queued[prefetchID(gacha.Limited, 39)] {
		t.Fatal("a record far below the fold requested its icon on the first paint")
	}
}

func sameIDs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
