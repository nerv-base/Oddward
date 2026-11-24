// SPDX-License-Identifier: GPL-3.0-or-later

package state

import "testing"

func TestGalleryInitialDefaultsAndNavigationLifecycle(t *testing.T) {
	m := Initial()
	if m.Gallery.AutoArchive {
		t.Fatal("AutoArchive must default to false")
	}
	if !m.Gallery.AutoDiscoverGame || m.Gallery.ThumbCacheMB != GalleryThumbCacheDefaultMB {
		t.Fatalf("unexpected initial gallery state: %+v", m.Gallery)
	}

	// Navigating to Gallery loads snapshot, runs initial scan, and enables watcher.
	m, fx := Reduce(m, Event{Kind: Navigate, Page: Gallery})
	if m.Page != Gallery || m.Gallery.Phase != GalleryScanning || !m.Gallery.Initialized {
		t.Fatalf("entering gallery failed: page=%v state=%+v", m.Page, m.Gallery)
	}
	if len(fx) != 3 || fx[0].Kind != EffectGalleryLoad || fx[1].Kind != EffectGalleryScan || fx[2].Kind != EffectGalleryWatchSync || !fx[2].Enabled {
		t.Fatalf("entering gallery effects = %+v", fx)
	}

	// Navigating away with AutoArchive OFF stops background watcher.
	m, fx = Reduce(m, Event{Kind: Navigate, Page: Home})
	if m.Page != Home || len(fx) != 1 || fx[0].Kind != EffectGalleryWatchSync || fx[0].Enabled {
		t.Fatalf("leaving gallery with auto-archive off must stop watcher: %+v", fx)
	}

	// Re-entering gallery does not re-trigger initial scan, just loads and resumes watching.
	m, fx = Reduce(m, Event{Kind: Navigate, Page: Gallery})
	if len(fx) != 2 || fx[0].Kind != EffectGalleryLoad || fx[1].Kind != EffectGalleryWatchSync || !fx[1].Enabled {
		t.Fatalf("re-entering gallery effects = %+v", fx)
	}

	// Enabling auto-archive keeps watcher running when leaving gallery.
	m.Gallery.Phase = GalleryReady
	m, fx = Reduce(m, Event{Kind: GalleryToggleAutoArchive, Enabled: true})
	if !m.Gallery.AutoArchive || len(fx) < 1 || fx[0].Kind != EffectGallerySaveSettings || !fx[0].Gallery.AutoArchive {
		t.Fatalf("toggle auto-archive failed: state=%+v fx=%+v", m.Gallery, fx)
	}
	m, _ = Reduce(m, Event{Kind: GalleryActionCompleted, Token: m.Gallery.Token, Message: "已保存"})
	m, fx = Reduce(m, Event{Kind: Navigate, Page: Home})
	if len(fx) != 1 || fx[0].Kind != EffectGalleryWatchSync || !fx[0].Enabled {
		t.Fatalf("leaving gallery with auto-archive ON must keep watcher active: %+v", fx)
	}
}

func TestGalleryFiltersSelectionViewerAndDismissOrder(t *testing.T) {
	m := Initial()
	m, _ = Reduce(m, Event{Kind: Navigate, Page: Gallery})
	m, _ = Reduce(m, Event{Kind: GalleryScanCompleted, Token: m.Gallery.ScanToken})
	m, _ = Reduce(m, Event{Kind: GallerySnapshotLoaded, Token: m.Gallery.Token, Gallery: GalleryPayload{
		TotalCount: 12, FilteredCount: 12, CachedCount: 8, ExportedCount: 4, ArchivedCount: 2,
	}})
	if m.Gallery.Phase != GalleryReady || m.Gallery.TotalCount != 12 {
		t.Fatalf("snapshot load failed: %+v", m.Gallery)
	}

	// Select all enters multi-select mode.
	m, fx := Reduce(m, Event{Kind: GallerySelectAll})
	if !m.Gallery.SelectMode || m.Gallery.SelectedCnt != 12 || len(fx) != 1 || fx[0].Kind != EffectGallerySelectAll {
		t.Fatalf("select all failed: state=%+v fx=%+v", m.Gallery, fx)
	}

	// Esc exits select mode.
	m, _ = Reduce(m, Event{Kind: Dismiss})
	if m.Gallery.SelectMode || m.Gallery.SelectedCnt != 0 {
		t.Fatal("Dismiss should exit select mode")
	}

	// Open viewer -> Step -> Esc closes viewer.
	m, _ = Reduce(m, Event{Kind: GalleryOpenViewer, Gallery: GalleryPayload{AssetID: 42}})
	if !m.Gallery.ViewerOpen || m.Gallery.ViewerID != 42 {
		t.Fatalf("viewer did not open: %+v", m.Gallery)
	}
	m, fx = Reduce(m, Event{Kind: GalleryViewerStep, Gallery: GalleryPayload{Step: 1}})
	if len(fx) != 1 || fx[0].Kind != EffectGalleryViewerStep || fx[0].Gallery.Step != 1 {
		t.Fatalf("viewer step failed: %+v", fx)
	}
	m, _ = Reduce(m, Event{Kind: Dismiss})
	if m.Gallery.ViewerOpen || m.Gallery.ViewerID != 0 {
		t.Fatal("Dismiss should close viewer")
	}

	// Filter change clears selection and emits reload.
	m.Gallery.SelectMode, m.Gallery.SelectedCnt = true, 3
	m, fx = Reduce(m, Event{Kind: GallerySetScope, Gallery: GalleryPayload{Scope: GalleryScopeArchived}})
	if m.Gallery.Scope != GalleryScopeArchived || m.Gallery.SelectMode || m.Gallery.SelectedCnt != 0 || len(fx) != 1 || fx[0].Kind != EffectGalleryLoad {
		t.Fatalf("scope change failed: state=%+v fx=%+v", m.Gallery, fx)
	}

	// UID filter & sort order.
	m, fx = Reduce(m, Event{Kind: GallerySetUID, Gallery: GalleryPayload{UID: GalleryUIDUnknown}})
	if m.Gallery.UID != GalleryUIDUnknown || len(fx) != 1 || fx[0].Kind != EffectGalleryLoad {
		t.Fatalf("UID change failed: %+v", fx)
	}
	m, fx = Reduce(m, Event{Kind: GallerySetSort, Gallery: GalleryPayload{Sort: GallerySortLargest}})
	if m.Gallery.Sort != GallerySortLargest || len(fx) != 1 || fx[0].Kind != EffectGalleryLoad {
		t.Fatalf("sort change failed: %+v", fx)
	}
}

func TestGalleryGameRootSwitchCancelsStaleScanAndDiskFullWarns(t *testing.T) {
	m := Initial()
	m, _ = Reduce(m, Event{Kind: Navigate, Page: Gallery})
	oldToken := m.Gallery.ScanToken

	// Switching game root bumps ScanToken and emits fresh scan + watch.
	m, fx := Reduce(m, Event{Kind: GalleryGameRootChanged, Path: `D:\Games\NTE`})
	if m.Gallery.ScanToken <= oldToken || len(fx) != 2 || fx[0].Kind != EffectGalleryScan || fx[0].Gallery.GameRoot != `D:\Games\NTE` {
		t.Fatalf("game root change failed: state=%+v fx=%+v", m.Gallery, fx)
	}

	// Stale scan completion from old root MUST be ignored.
	m2, fx2 := Reduce(m, Event{Kind: GalleryScanCompleted, Token: oldToken})
	if m2 != m || len(fx2) != 0 {
		t.Fatal("stale scan completion must be dropped")
	}

	// Current scan completion with DiskFull sets warning notice.
	m, fx = Reduce(m, Event{Kind: GalleryScanCompleted, Token: m.Gallery.ScanToken, Gallery: GalleryPayload{DiskFull: true}})
	if !m.Gallery.NoticeWarn || m.Gallery.Notice == "" || len(fx) != 1 || fx[0].Kind != EffectGalleryLoad {
		t.Fatalf("disk full notice missing: state=%+v fx=%+v", m.Gallery, fx)
	}
}

func TestGalleryDeleteArchiveRequiresConfirmation(t *testing.T) {
	m := Initial()
	m, _ = Reduce(m, Event{Kind: Navigate, Page: Gallery})
	m, fx := Reduce(m, Event{Kind: GalleryRequestDeleteArchive, Gallery: GalleryPayload{AssetID: 99}})
	if m.Gallery.ConfirmDeleteID != 99 || len(fx) != 0 {
		t.Fatalf("request delete must arm confirmation without deleting immediately: state=%+v fx=%+v", m.Gallery, fx)
	}
	m, fx = Reduce(m, Event{Kind: GalleryConfirmDeleteArchive})
	if m.Gallery.ConfirmDeleteID != 0 || !m.Gallery.Busy || len(fx) != 1 || fx[0].Kind != EffectGalleryDeleteArchive || fx[0].Gallery.AssetID != 99 {
		t.Fatalf("confirm delete failed: state=%+v fx=%+v", m.Gallery, fx)
	}
}
