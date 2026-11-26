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

func TestGalleryP1SearchFavoritesAlbumsTagsBatchDeleteAndCustomBackground(t *testing.T) {
	m := Initial()
	m, _ = Reduce(m, Event{Kind: Navigate, Page: Gallery})
	m, _ = Reduce(m, Event{Kind: GalleryScanCompleted, Token: m.Gallery.ScanToken})
	m, _ = Reduce(m, Event{Kind: GallerySnapshotLoaded, Token: m.Gallery.Token, Gallery: GalleryPayload{
		TotalCount: 8, PhotoCount: 6, VideoCount: 2, FavoriteCount: 3, FilteredCount: 8,
		ArchivePhotoCount: 2, ArchivePhotoBytes: 4 << 20, ArchiveVideoCount: 1, ArchiveVideoBytes: 20 << 20,
		ArchiveFreeBytes: 50 << 30,
	}})
	if m.Gallery.VideoCount != 2 || m.Gallery.FavoriteCount != 3 || m.Gallery.ArchiveVideoBytes != 20<<20 {
		t.Fatalf("P1 metrics missing from snapshot: %+v", m.Gallery)
	}

	// Favorites & videos scopes + album, tag, search filters emit EffectGalleryLoad.
	m, fx := Reduce(m, Event{Kind: GallerySetScope, Gallery: GalleryPayload{Scope: GalleryScopeVideos}})
	if m.Gallery.Scope != GalleryScopeVideos || len(fx) != 1 || fx[0].Kind != EffectGalleryLoad || fx[0].Gallery.Scope != GalleryScopeVideos {
		t.Fatalf("GalleryScopeVideos failed: %+v %+v", m.Gallery, fx)
	}
	m, fx = Reduce(m, Event{Kind: GallerySetAlbum, Gallery: GalleryPayload{AlbumID: 7}})
	if m.Gallery.AlbumID != 7 || len(fx) != 1 || fx[0].Gallery.AlbumID != 7 {
		t.Fatalf("GallerySetAlbum failed: %+v", fx)
	}
	m, fx = Reduce(m, Event{Kind: GallerySetTag, Gallery: GalleryPayload{Tag: "霓虹"}})
	if m.Gallery.Tag != "霓虹" || len(fx) != 1 || fx[0].Gallery.Tag != "霓虹" {
		t.Fatalf("GallerySetTag failed: %+v", fx)
	}
	m, fx = Reduce(m, Event{Kind: GallerySetSearch, Gallery: GalleryPayload{Search: "海特洛"}})
	if m.Gallery.Search != "海特洛" || len(fx) != 1 || fx[0].Gallery.Search != "海特洛" {
		t.Fatalf("GallerySetSearch failed: %+v", fx)
	}

	// Deleting active album clears AlbumID filter cleanly.
	m, fx = Reduce(m, Event{Kind: GalleryDeleteAlbum, Gallery: GalleryPayload{AlbumID: 7}})
	if m.Gallery.AlbumID != 0 || len(fx) != 1 || fx[0].Kind != EffectGalleryDeleteAlbum {
		t.Fatalf("GalleryDeleteAlbum should clear active album filter: %+v %+v", m.Gallery, fx)
	}
	m, _ = Reduce(m, Event{Kind: GalleryActionCompleted, Token: m.Gallery.Token, Message: "已删除"})

	// Centered organization modals can open over viewer, and Dismiss closes modal BEFORE viewer.
	m, _ = Reduce(m, Event{Kind: GalleryOpenViewer, Gallery: GalleryPayload{AssetID: 5}})
	m, _ = Reduce(m, Event{Kind: GalleryOpenTagModal, Gallery: GalleryPayload{AssetID: 5}})
	if !m.Gallery.ViewerOpen || !m.Gallery.TagModalOpen || !m.Gallery.ModalOpen() {
		t.Fatalf("tag modal over viewer failed: %+v", m.Gallery)
	}
	m, _ = Reduce(m, Event{Kind: Dismiss})
	if m.Gallery.TagModalOpen || !m.Gallery.ViewerOpen {
		t.Fatalf("Dismiss must close tag modal first and keep viewer open: %+v", m.Gallery)
	}
	m, _ = Reduce(m, Event{Kind: Dismiss})
	if m.Gallery.ViewerOpen {
		t.Fatal("second Dismiss must close viewer")
	}

	// Batch archive delete requires explicit confirmation.
	m, _ = Reduce(m, Event{Kind: GallerySelectAll})
	m, fx = Reduce(m, Event{Kind: GalleryRequestBatchDeleteArchive})
	if !m.Gallery.ConfirmBatchDelete || len(fx) != 0 {
		t.Fatalf("batch delete must arm confirmation first: %+v %+v", m.Gallery, fx)
	}
	m, fx = Reduce(m, Event{Kind: GalleryConfirmBatchDeleteArchive})
	if m.Gallery.ConfirmBatchDelete || !m.Gallery.Busy || len(fx) != 1 || fx[0].Kind != EffectGalleryDeleteArchiveBatch {
		t.Fatalf("confirm batch delete failed: %+v %+v", m.Gallery, fx)
	}
	m, _ = Reduce(m, Event{Kind: GalleryActionCompleted, Token: m.Gallery.Token, Message: "ok"})

	// Custom static background: set from Gallery, clear from LauncherSettings.
	m, fx = Reduce(m, Event{Kind: GallerySetAsBackground, Gallery: GalleryPayload{AssetID: 5}})
	if len(fx) != 1 || fx[0].Kind != EffectGallerySetCustomBackground || fx[0].Gallery.AssetID != 5 {
		t.Fatalf("SetAsBackground failed: %+v", fx)
	}
	m, _ = Reduce(m, Event{Kind: GalleryCustomBackgroundLoaded, Enabled: true})
	m, _ = Reduce(m, Event{Kind: GalleryActionCompleted, Token: m.Gallery.Token, Message: "已设为背景"})
	if !m.CustomBackground {
		t.Fatal("CustomBackground should be active")
	}
	m, _ = Reduce(m, Event{Kind: Navigate, Page: LauncherSettings})
	m, fx = Reduce(m, Event{Kind: GalleryClearCustomBackground})
	if len(fx) != 1 || fx[0].Kind != EffectGalleryClearCustomBackground {
		t.Fatalf("ClearCustomBackground from LauncherSettings failed: %+v", fx)
	}

	// ViewMode defaults to Bravais infinite canvas and toggles cleanly to timeline and back.
	m2 := Initial()
	if m2.Gallery.ViewMode != GalleryViewCanvas {
		t.Fatalf("default ViewMode = %v, want GalleryViewCanvas", m2.Gallery.ViewMode)
	}
	m2, _ = Reduce(m2, Event{Kind: Navigate, Page: Gallery})
	m2, fx = Reduce(m2, Event{Kind: GallerySetViewMode, Gallery: GalleryPayload{ViewMode: GalleryViewTimeline}})
	if m2.Gallery.ViewMode != GalleryViewTimeline || len(fx) != 0 {
		t.Fatalf("switch to timeline failed: mode=%v fx=%+v", m2.Gallery.ViewMode, fx)
	}
	m2, _ = Reduce(m2, Event{Kind: GallerySetViewMode, Gallery: GalleryPayload{ViewMode: GalleryViewCanvas}})
	if m2.Gallery.ViewMode != GalleryViewCanvas {
		t.Fatal("switch back to infinite canvas failed")
	}
}
