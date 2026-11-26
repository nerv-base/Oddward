// SPDX-License-Identifier: GPL-3.0-or-later

package state

// GalleryPhase describes the local gallery's indexing/loading phase.
type GalleryPhase uint8

const (
	GalleryIdle GalleryPhase = iota
	GalleryScanning
	GalleryReady
	GalleryFailed
)

// GalleryScope filters indexed media by provenance, favorites or media type.
type GalleryScope uint8

const (
	GalleryScopeAll GalleryScope = iota
	GalleryScopeCached
	GalleryScopeExported
	GalleryScopeArchived
	GalleryScopeFavorites
	GalleryScopeVideos
)

// GallerySort controls gallery ordering.
type GallerySort uint8

const (
	GallerySortNewest GallerySort = iota
	GallerySortOldest
	GallerySortLargest
)

// GalleryViewMode switches between the free-panning Bravais infinite gallery
// wall (default) and the date-grouped timeline list.
type GalleryViewMode uint8

const (
	GalleryViewCanvas GalleryViewMode = iota
	GalleryViewTimeline
)

// GalleryUIDUnknown filters assets whose UID cannot be confirmed (for example
// exported or custom directories).
const GalleryUIDUnknown = "__unknown__"

// GalleryThumbCacheDefaultMB is the default disk thumbnail cache budget.
const (
	GalleryThumbCacheMinMB     = 32
	GalleryThumbCacheDefaultMB = 256
	GalleryThumbCacheMaxMB     = 4096
)

const (
	GalleryEnter EventKind = 420 + iota
	GalleryRefresh
	GalleryGameRootChanged
	GallerySetScope
	GallerySetUID
	GallerySetSort
	GalleryToggleSelectMode
	GallerySelectionChanged
	GallerySelectAll
	GalleryClearSelection
	GalleryOpenViewer
	GalleryCloseViewer
	GalleryViewerStep
	GalleryOpenSettings
	GalleryCloseSettings
	GalleryToggleAutoArchive
	GalleryToggleAutoDiscover
	GalleryPickArchiveDir
	GalleryArchiveDirPicked
	GallerySetThumbCacheMB
	GalleryAddCustomSource
	GalleryCustomSourcePicked
	GalleryRemoveCustomSource
	GalleryArchiveExisting
	GalleryArchiveAsset
	GalleryRequestDeleteArchive
	GalleryConfirmDeleteArchive
	GalleryCancelDeleteArchive
	GalleryClearThumbCache
	GalleryRebuildIndex
	GalleryCopyImage
	GalleryExportSingle
	GalleryExportSinglePicked
	GalleryExportSelected
	GalleryExportBatchPicked
	GalleryOpenFolder
	GalleryRevealAsset
	GallerySnapshotLoaded
	GalleryScanCompleted
	GalleryWatchChanged
	GalleryActionCompleted
	GalleryActionFailed
	// P1 organization, search, batch management and custom static background.
	GallerySetAlbum
	GallerySetTag
	GallerySetSearch
	GalleryToggleFavorite
	GalleryBatchFavorite
	GalleryOpenAlbumModal
	GalleryCloseAlbumModal
	GalleryCreateAlbum
	GalleryRenameAlbum
	GalleryDeleteAlbum
	GalleryAddToAlbum
	GalleryRemoveFromAlbum
	GalleryOpenTagModal
	GalleryCloseTagModal
	GalleryAddTag
	GalleryRemoveTag
	GalleryArchiveSelected
	GalleryRequestBatchDeleteArchive
	GalleryConfirmBatchDeleteArchive
	GalleryCancelBatchDeleteArchive
	GalleryPruneMissing
	GallerySetAsBackground
	GalleryClearCustomBackground
	GalleryCustomBackgroundLoaded
	GallerySetViewMode
	galleryEventLast
)

const (
	EffectGalleryLoad EffectKind = 420 + iota
	EffectGalleryScan
	EffectGalleryWatchSync
	EffectGallerySaveSettings
	EffectGalleryPickArchiveDir
	EffectGalleryPickCustomSource
	EffectGalleryAddCustomSource
	EffectGalleryRemoveCustomSource
	EffectGalleryArchiveExisting
	EffectGalleryArchiveAsset
	EffectGalleryDeleteArchive
	EffectGalleryClearThumbCache
	EffectGalleryRebuildIndex
	EffectGalleryCopyImage
	EffectGalleryPickExportSingle
	EffectGalleryExportSingle
	EffectGalleryPickExportDir
	EffectGalleryExportBatch
	EffectGalleryOpenFolder
	EffectGalleryRevealAsset
	EffectGallerySelectAll
	EffectGalleryViewerStep
	// P1 organization, batch management and custom static background effects.
	EffectGallerySetFavorites
	EffectGalleryCreateAlbum
	EffectGalleryRenameAlbum
	EffectGalleryDeleteAlbum
	EffectGalleryAddToAlbum
	EffectGalleryRemoveFromAlbum
	EffectGalleryAddTag
	EffectGalleryRemoveTag
	EffectGalleryArchiveBatch
	EffectGalleryDeleteArchiveBatch
	EffectGalleryPruneMissing
	EffectGallerySetCustomBackground
	EffectGalleryClearCustomBackground
	effectGalleryLast
)

// IsGalleryEffect reports whether k belongs to the gallery effect range.
func IsGalleryEffect(k EffectKind) bool {
	return k >= EffectGalleryLoad && k < effectGalleryLast
}

// GalleryState stores comparable scalar gallery state inside Model. Asset lists
// and bitmap caches stay on the UI thread inside Shell.
type GalleryState struct {
	Phase        GalleryPhase
	Busy         bool
	Initialized  bool
	Token        uint64
	ScanToken    uint64
	GameRoot     string
	Scope        GalleryScope
	UID          string
	AlbumID      int64
	Tag          string
	Search       string
	Sort         GallerySort
	ViewMode     GalleryViewMode
	SelectMode   bool
	SelectedCnt  int
	FocusedID    int64
	ViewerOpen   bool
	ViewerID     int64
	SettingsOpen bool
	// ConfirmDeleteID names an asset whose Oddward-managed archive copy awaits
	// explicit user confirmation before removal.
	ConfirmDeleteID int64
	// ConfirmBatchDelete guards batch deletion of Oddward-managed archive copies.
	ConfirmBatchDelete bool
	// AlbumModalOpen and TagModalOpen control centered P1 organization dialogs.
	AlbumModalOpen    bool
	AlbumModalAssetID int64
	TagModalOpen      bool
	TagModalAssetID   int64

	// Persisted settings projection.
	AutoArchive      bool
	AutoDiscoverGame bool
	ArchiveDir       string
	EffectiveArchive string
	ThumbCacheMB     int

	// Real metrics reported by the local SQLite store and thumbnail cache.
	TotalCount        int
	PhotoCount        int
	VideoCount        int
	FavoriteCount     int
	FilteredCount     int
	CachedCount       int
	ExportedCount     int
	ArchivedCount     int
	MissingCount      int
	InvalidCount      int
	ArchiveBytes      int64
	ArchivePhotoCount int
	ArchivePhotoBytes int64
	ArchiveVideoCount int
	ArchiveVideoBytes int64
	ArchiveFreeBytes  int64
	ThumbCacheBytes   int64

	Watching   bool
	Notice     string
	NoticeWarn bool
	Error      string
}

// InitialGalleryState returns the default comparable gallery state.
func InitialGalleryState() GalleryState {
	return GalleryState{
		AutoArchive:      false,
		AutoDiscoverGame: true,
		ThumbCacheMB:     GalleryThumbCacheDefaultMB,
	}
}

// GalleryPayload carries scalar values for gallery events.
type GalleryPayload struct {
	Scope             GalleryScope
	Sort              GallerySort
	ViewMode          GalleryViewMode
	UID               string
	AlbumID           int64
	Tag               string
	Search            string
	AssetID           int64
	SourceID          int64
	Step              int
	SelectedCount     int
	FocusedID         int64
	AutoArchive       bool
	AutoDiscoverGame  bool
	ArchiveDir        string
	EffectiveArchive  string
	ThumbCacheMB      int
	TotalCount        int
	PhotoCount        int
	VideoCount        int
	FavoriteCount     int
	FilteredCount     int
	CachedCount       int
	ExportedCount     int
	ArchivedCount     int
	MissingCount      int
	InvalidCount      int
	ArchiveBytes      int64
	ArchivePhotoCount int
	ArchivePhotoBytes int64
	ArchiveVideoCount int
	ArchiveVideoBytes int64
	ArchiveFreeBytes  int64
	ThumbCacheBytes   int64
	DiskFull          bool
	Warn              bool
	Watching          bool
}

// GalleryEffectData carries parameters for gallery effects.
type GalleryEffectData struct {
	Scope            GalleryScope
	Sort             GallerySort
	UID              string
	AlbumID          int64
	Tag              string
	Search           string
	Name             string
	GameRoot         string
	AssetID          int64
	SourceID         int64
	Step             int
	AutoArchive      bool
	AutoDiscoverGame bool
	ArchiveDir       string
	ThumbCacheMB     int
}

// ModalOpen reports whether a gallery modal (viewer, settings, organization or
// archive delete confirmation) is currently active on top of the gallery page.
func (g GalleryState) ModalOpen() bool {
	return g.ViewerOpen || g.SettingsOpen || g.ConfirmDeleteID != 0 || g.ConfirmBatchDelete || g.AlbumModalOpen || g.TagModalOpen
}

func reduceGallery(m Model, e Event) (Model, []Effect, bool) {
	if m.Closed {
		return m, nil, false
	}
	g := &m.Gallery

	// Dismiss closes active gallery modals or exits select mode before falling
	// back to global dismiss behavior.
	if e.Kind == Dismiss && m.Overlay == NoOverlay && m.Page == Gallery {
		if g.ConfirmDeleteID != 0 {
			g.ConfirmDeleteID = 0
			return m, nil, true
		}
		if g.ConfirmBatchDelete {
			g.ConfirmBatchDelete = false
			return m, nil, true
		}
		if g.AlbumModalOpen {
			g.AlbumModalOpen, g.AlbumModalAssetID = false, 0
			return m, nil, true
		}
		if g.TagModalOpen {
			g.TagModalOpen, g.TagModalAssetID = false, 0
			return m, nil, true
		}
		if g.SettingsOpen {
			g.SettingsOpen = false
			return m, nil, true
		}
		if g.ViewerOpen {
			g.ViewerOpen = false
			g.ViewerID = 0
			return m, nil, true
		}
		if g.SelectMode {
			g.SelectMode = false
			g.SelectedCnt = 0
			return m, nil, true
		}
		return m, nil, false
	}

	// Navigating away from Gallery closes transient gallery modals and updates
	// watcher state so idle standby does not keep gallery work alive when
	// auto-archive is off.
	if e.Kind == Navigate && m.Overlay == NoOverlay && e.Page <= LauncherSettings && e.Page != m.Page {
		if m.Page == Gallery && g.ModalOpen() {
			// Close gallery modals cleanly before allowing page navigation.
			g.ViewerOpen = false
			g.ViewerID = 0
			g.SettingsOpen = false
			g.ConfirmDeleteID = 0
			g.ConfirmBatchDelete = false
			g.AlbumModalOpen, g.AlbumModalAssetID = false, 0
			g.TagModalOpen, g.TagModalAssetID = false, 0
		}
		if e.Page == Gallery {
			m.Page = Gallery
			g.Notice, g.NoticeWarn = "", false
			g.Token++
			fx := []Effect{loadGalleryEffect(*g, g.Token)}
			if !g.Initialized {
				g.Initialized = true
				g.Phase = GalleryScanning
				g.ScanToken++
				fx = append(fx, scanGalleryEffect(*g))
			}
			fx = append(fx, watchGalleryEffect(*g, true))
			return m, fx, true
		}
		if m.Page == Gallery {
			// Keep watcher alive in background ONLY when auto-archive is enabled.
			m.Page = e.Page
			if e.Page == LauncherSettings {
				m.settingsReturnPage, m.settingsFromMedia = Gallery, e.FromMedia
				m.SettingsSection = SettingsGeneral
				m.Bar = MediaBarHidden
				if !m.Network.Saving {
					m.Network.Saved, m.Network.Error = false, ""
				}
			}
			return m, []Effect{watchGalleryEffect(*g, g.AutoArchive)}, true
		}
		return m, nil, false
	}

	if e.Kind < GalleryEnter || e.Kind >= galleryEventLast {
		return m, nil, false
	}

	switch e.Kind {
	case GalleryEnter:
		if m.Page != Gallery || m.Overlay != NoOverlay {
			return m, nil, true
		}
		g.Token++
		fx := []Effect{loadGalleryEffect(*g, g.Token)}
		if !g.Initialized {
			g.Initialized = true
			g.Phase = GalleryScanning
			g.ScanToken++
			fx = append(fx, scanGalleryEffect(*g))
		}
		fx = append(fx, watchGalleryEffect(*g, true))
		return m, fx, true

	case GalleryGameRootChanged:
		if SameManagedRoot(g.GameRoot, e.Path) || (g.GameRoot == "" && e.Path == "") {
			return m, nil, true
		}
		// Switching game installation cancels any in-flight scan/watch for the
		// previous installation by bumping ScanToken.
		g.GameRoot = e.Path
		g.ScanToken++
		if !g.Initialized && !g.AutoArchive && m.Page != Gallery {
			return m, nil, true
		}
		g.Phase = GalleryScanning
		g.Error = ""
		fx := []Effect{
			scanGalleryEffect(*g),
			watchGalleryEffect(*g, m.Page == Gallery || g.AutoArchive),
		}
		return m, fx, true

	case GalleryRefresh:
		if m.Page != Gallery || m.Overlay != NoOverlay || g.Phase == GalleryScanning {
			return m, nil, true
		}
		g.Phase = GalleryScanning
		g.Error = ""
		g.Notice, g.NoticeWarn = "", false
		g.ScanToken++
		return m, []Effect{scanGalleryEffect(*g)}, true

	case GallerySetScope:
		if m.Page != Gallery || m.Overlay != NoOverlay || e.Gallery.Scope > GalleryScopeVideos {
			return m, nil, true
		}
		if g.Scope == e.Gallery.Scope {
			return m, nil, true
		}
		g.Scope = e.Gallery.Scope
		g.SelectMode, g.SelectedCnt = false, 0
		g.Token++
		return m, []Effect{loadGalleryEffect(*g, g.Token)}, true

	case GallerySetAlbum:
		if m.Page != Gallery || m.Overlay != NoOverlay {
			return m, nil, true
		}
		if g.AlbumID == e.Gallery.AlbumID {
			return m, nil, true
		}
		g.AlbumID = e.Gallery.AlbumID
		g.SelectMode, g.SelectedCnt = false, 0
		g.Token++
		return m, []Effect{loadGalleryEffect(*g, g.Token)}, true

	case GallerySetTag:
		if m.Page != Gallery || m.Overlay != NoOverlay {
			return m, nil, true
		}
		if g.Tag == e.Gallery.Tag {
			return m, nil, true
		}
		g.Tag = e.Gallery.Tag
		g.SelectMode, g.SelectedCnt = false, 0
		g.Token++
		return m, []Effect{loadGalleryEffect(*g, g.Token)}, true

	case GallerySetSearch:
		if m.Page != Gallery || m.Overlay != NoOverlay {
			return m, nil, true
		}
		if g.Search == e.Gallery.Search {
			return m, nil, true
		}
		g.Search = e.Gallery.Search
		g.SelectMode, g.SelectedCnt = false, 0
		g.Token++
		return m, []Effect{loadGalleryEffect(*g, g.Token)}, true

	case GallerySetUID:
		if m.Page != Gallery || m.Overlay != NoOverlay {
			return m, nil, true
		}
		if g.UID == e.Gallery.UID {
			return m, nil, true
		}
		g.UID = e.Gallery.UID
		g.SelectMode, g.SelectedCnt = false, 0
		g.Token++
		return m, []Effect{loadGalleryEffect(*g, g.Token)}, true

	case GallerySetSort:
		if m.Page != Gallery || m.Overlay != NoOverlay || e.Gallery.Sort > GallerySortLargest {
			return m, nil, true
		}
		if g.Sort == e.Gallery.Sort {
			return m, nil, true
		}
		g.Sort = e.Gallery.Sort
		g.Token++
		return m, []Effect{loadGalleryEffect(*g, g.Token)}, true

	case GalleryToggleSelectMode:
		if m.Page != Gallery || m.Overlay != NoOverlay || g.ModalOpen() {
			return m, nil, true
		}
		g.SelectMode = !g.SelectMode
		if !g.SelectMode {
			g.SelectedCnt = 0
		}
		return m, nil, true

	case GallerySelectionChanged:
		g.SelectedCnt = max(0, e.Gallery.SelectedCount)
		if e.Gallery.FocusedID > 0 {
			g.FocusedID = e.Gallery.FocusedID
		}
		return m, nil, true

	case GallerySelectAll:
		if m.Page != Gallery || m.Overlay != NoOverlay || g.ModalOpen() || g.FilteredCount == 0 {
			return m, nil, true
		}
		g.SelectMode = true
		g.SelectedCnt = g.FilteredCount
		return m, []Effect{{Kind: EffectGallerySelectAll}}, true

	case GalleryClearSelection:
		g.SelectMode = false
		g.SelectedCnt = 0
		return m, nil, true

	case GalleryOpenViewer:
		if m.Page != Gallery || m.Overlay != NoOverlay || g.SettingsOpen || g.ConfirmDeleteID != 0 {
			return m, nil, true
		}
		id := e.Gallery.AssetID
		if id <= 0 {
			id = g.FocusedID
		}
		if id <= 0 {
			return m, nil, true
		}
		g.ViewerOpen = true
		g.ViewerID = id
		g.FocusedID = id
		return m, nil, true

	case GalleryCloseViewer:
		g.ViewerOpen = false
		g.ViewerID = 0
		return m, nil, true

	case GalleryViewerStep:
		if !g.ViewerOpen || e.Gallery.Step == 0 {
			return m, nil, true
		}
		return m, []Effect{{Kind: EffectGalleryViewerStep, Gallery: GalleryEffectData{AssetID: g.ViewerID, Step: e.Gallery.Step}}}, true

	case GalleryOpenSettings:
		if m.Page != Gallery || m.Overlay != NoOverlay {
			return m, nil, true
		}
		g.ViewerOpen = false
		g.ViewerID = 0
		g.SettingsOpen = true
		g.Notice, g.NoticeWarn = "", false
		return m, nil, true

	case GalleryCloseSettings:
		g.SettingsOpen = false
		return m, nil, true

	case GalleryToggleAutoArchive:
		if m.Page != Gallery || m.Overlay != NoOverlay || g.Busy {
			return m, nil, true
		}
		g.AutoArchive = e.Enabled
		g.Busy = true
		g.Token++
		g.Notice, g.NoticeWarn = "", false
		fx := []Effect{saveGallerySettingsEffect(*g, g.Token)}
		fx = append(fx, watchGalleryEffect(*g, true))
		return m, fx, true

	case GalleryToggleAutoDiscover:
		if m.Page != Gallery || m.Overlay != NoOverlay || g.Busy {
			return m, nil, true
		}
		g.AutoDiscoverGame = e.Enabled
		g.Busy = true
		g.Token++
		return m, []Effect{saveGallerySettingsEffect(*g, g.Token)}, true

	case GalleryPickArchiveDir:
		if m.Page != Gallery || m.Overlay != NoOverlay || g.Busy {
			return m, nil, true
		}
		g.Busy = true
		g.Token++
		return m, []Effect{{Kind: EffectGalleryPickArchiveDir, Token: g.Token}}, true

	case GalleryArchiveDirPicked:
		if e.Token != g.Token {
			return m, nil, true
		}
		if e.Path == "" {
			g.Busy = false
			return m, nil, true
		}
		g.ArchiveDir = e.Path
		g.Token++
		return m, []Effect{saveGallerySettingsEffect(*g, g.Token)}, true

	case GallerySetThumbCacheMB:
		if m.Page != Gallery || m.Overlay != NoOverlay || g.Busy {
			return m, nil, true
		}
		mb := e.Gallery.ThumbCacheMB
		if mb < GalleryThumbCacheMinMB || mb > GalleryThumbCacheMaxMB {
			return m, nil, true
		}
		g.ThumbCacheMB = mb
		g.Busy = true
		g.Token++
		return m, []Effect{saveGallerySettingsEffect(*g, g.Token)}, true

	case GalleryAddCustomSource:
		if m.Page != Gallery || m.Overlay != NoOverlay || g.Busy {
			return m, nil, true
		}
		g.Busy = true
		g.Token++
		return m, []Effect{{Kind: EffectGalleryPickCustomSource, Token: g.Token}}, true

	case GalleryCustomSourcePicked:
		if e.Token != g.Token {
			return m, nil, true
		}
		if e.Path == "" {
			g.Busy = false
			return m, nil, true
		}
		g.Token++
		return m, []Effect{{Kind: EffectGalleryAddCustomSource, Token: g.Token, Path: e.Path}}, true

	case GalleryRemoveCustomSource:
		if m.Page != Gallery || m.Overlay != NoOverlay || g.Busy || e.Gallery.SourceID <= 0 {
			return m, nil, true
		}
		g.Busy = true
		g.Token++
		return m, []Effect{{Kind: EffectGalleryRemoveCustomSource, Token: g.Token, Gallery: GalleryEffectData{SourceID: e.Gallery.SourceID}}}, true

	case GalleryArchiveExisting:
		if m.Page != Gallery || m.Overlay != NoOverlay || g.Busy {
			return m, nil, true
		}
		g.Busy = true
		g.Token++
		g.Notice, g.NoticeWarn = "正在归档已有游戏照片…", false
		return m, []Effect{{Kind: EffectGalleryArchiveExisting, Token: g.Token}}, true

	case GalleryArchiveAsset:
		if m.Page != Gallery || m.Overlay != NoOverlay || g.Busy || e.Gallery.AssetID <= 0 {
			return m, nil, true
		}
		g.Busy = true
		g.Token++
		return m, []Effect{{Kind: EffectGalleryArchiveAsset, Token: g.Token, Gallery: GalleryEffectData{AssetID: e.Gallery.AssetID}}}, true

	case GalleryRequestDeleteArchive:
		if m.Page != Gallery || m.Overlay != NoOverlay || g.Busy || e.Gallery.AssetID <= 0 {
			return m, nil, true
		}
		g.ConfirmDeleteID = e.Gallery.AssetID
		return m, nil, true

	case GalleryCancelDeleteArchive:
		g.ConfirmDeleteID = 0
		return m, nil, true

	case GalleryConfirmDeleteArchive:
		if g.ConfirmDeleteID <= 0 || g.Busy {
			return m, nil, true
		}
		id := g.ConfirmDeleteID
		g.ConfirmDeleteID = 0
		g.Busy = true
		g.Token++
		return m, []Effect{{Kind: EffectGalleryDeleteArchive, Token: g.Token, Gallery: GalleryEffectData{AssetID: id}}}, true

	case GalleryClearThumbCache:
		if m.Page != Gallery || m.Overlay != NoOverlay || g.Busy {
			return m, nil, true
		}
		g.Busy = true
		g.Token++
		return m, []Effect{{Kind: EffectGalleryClearThumbCache, Token: g.Token}}, true

	case GalleryRebuildIndex:
		if m.Page != Gallery || m.Overlay != NoOverlay || g.Busy {
			return m, nil, true
		}
		g.Busy = true
		g.Phase = GalleryScanning
		g.Error = ""
		g.Notice, g.NoticeWarn = "正在重新建立索引…", false
		g.ScanToken++
		g.Token++
		return m, []Effect{{Kind: EffectGalleryRebuildIndex, Token: g.Token, Gallery: GalleryEffectData{GameRoot: g.GameRoot}}}, true

	case GalleryCopyImage:
		if m.Page != Gallery || m.Overlay != NoOverlay {
			return m, nil, true
		}
		id := e.Gallery.AssetID
		if id <= 0 {
			if g.ViewerOpen {
				id = g.ViewerID
			} else {
				id = g.FocusedID
			}
		}
		if id <= 0 {
			return m, nil, true
		}
		g.Token++
		return m, []Effect{{Kind: EffectGalleryCopyImage, Token: g.Token, Gallery: GalleryEffectData{AssetID: id}}}, true

	case GalleryExportSingle:
		if m.Page != Gallery || m.Overlay != NoOverlay || g.Busy {
			return m, nil, true
		}
		id := e.Gallery.AssetID
		if id <= 0 {
			if g.ViewerOpen {
				id = g.ViewerID
			} else {
				id = g.FocusedID
			}
		}
		if id <= 0 {
			return m, nil, true
		}
		g.Busy = true
		g.Token++
		return m, []Effect{{Kind: EffectGalleryPickExportSingle, Token: g.Token, Gallery: GalleryEffectData{AssetID: id}}}, true

	case GalleryExportSinglePicked:
		if e.Token != g.Token {
			return m, nil, true
		}
		if e.Path == "" {
			g.Busy = false
			return m, nil, true
		}
		g.Token++
		return m, []Effect{{Kind: EffectGalleryExportSingle, Token: g.Token, Path: e.Path, Gallery: GalleryEffectData{AssetID: e.Gallery.AssetID}}}, true

	case GalleryExportSelected:
		if m.Page != Gallery || m.Overlay != NoOverlay || g.Busy || g.SelectedCnt == 0 {
			return m, nil, true
		}
		g.Busy = true
		g.Token++
		return m, []Effect{{Kind: EffectGalleryPickExportDir, Token: g.Token}}, true

	case GalleryExportBatchPicked:
		if e.Token != g.Token {
			return m, nil, true
		}
		if e.Path == "" {
			g.Busy = false
			return m, nil, true
		}
		g.Token++
		return m, []Effect{{Kind: EffectGalleryExportBatch, Token: g.Token, Path: e.Path}}, true

	case GalleryOpenFolder:
		if m.Page != Gallery || m.Overlay != NoOverlay {
			return m, nil, true
		}
		target := e.Path
		if target == "" {
			target = g.EffectiveArchive
		}
		if target == "" {
			return m, nil, true
		}
		return m, []Effect{{Kind: EffectGalleryOpenFolder, Path: target}}, true

	case GalleryRevealAsset:
		if m.Page != Gallery || m.Overlay != NoOverlay || e.Gallery.AssetID <= 0 {
			return m, nil, true
		}
		return m, []Effect{{Kind: EffectGalleryRevealAsset, Gallery: GalleryEffectData{AssetID: e.Gallery.AssetID}}}, true

	case GallerySnapshotLoaded:
		if e.Token != 0 && e.Token != g.Token {
			return m, nil, true
		}
		if g.Phase != GalleryScanning {
			g.Phase = GalleryReady
		}
		g.Error = ""
		g.AutoArchive = e.Gallery.AutoArchive
		g.AutoDiscoverGame = e.Gallery.AutoDiscoverGame
		g.ArchiveDir = e.Gallery.ArchiveDir
		g.EffectiveArchive = e.Gallery.EffectiveArchive
		if e.Gallery.ThumbCacheMB > 0 {
			g.ThumbCacheMB = e.Gallery.ThumbCacheMB
		}
		g.TotalCount = e.Gallery.TotalCount
		g.PhotoCount = e.Gallery.PhotoCount
		g.VideoCount = e.Gallery.VideoCount
		g.FavoriteCount = e.Gallery.FavoriteCount
		g.FilteredCount = e.Gallery.FilteredCount
		g.CachedCount = e.Gallery.CachedCount
		g.ExportedCount = e.Gallery.ExportedCount
		g.ArchivedCount = e.Gallery.ArchivedCount
		g.MissingCount = e.Gallery.MissingCount
		g.InvalidCount = e.Gallery.InvalidCount
		g.ArchiveBytes = e.Gallery.ArchiveBytes
		g.ArchivePhotoCount = e.Gallery.ArchivePhotoCount
		g.ArchivePhotoBytes = e.Gallery.ArchivePhotoBytes
		g.ArchiveVideoCount = e.Gallery.ArchiveVideoCount
		g.ArchiveVideoBytes = e.Gallery.ArchiveVideoBytes
		g.ArchiveFreeBytes = e.Gallery.ArchiveFreeBytes
		g.ThumbCacheBytes = e.Gallery.ThumbCacheBytes
		g.Watching = e.Gallery.Watching
		if g.SelectedCnt > g.FilteredCount {
			g.SelectedCnt = g.FilteredCount
		}
		return m, nil, true

	case GalleryScanCompleted:
		if e.Token != g.ScanToken {
			return m, nil, true
		}
		g.Phase = GalleryReady
		g.Busy = false
		g.Error = ""
		if e.Gallery.DiskFull {
			g.Notice = "磁盘空间不足，自动归档已暂停；原始游戏文件保持完好。"
			g.NoticeWarn = true
		} else if e.Message != "" {
			g.Notice = e.Message
			g.NoticeWarn = false
		}
		g.Token++
		return m, []Effect{loadGalleryEffect(*g, g.Token)}, true

	case GalleryWatchChanged:
		if e.Gallery.DiskFull {
			g.Notice = "磁盘空间不足，自动归档已暂停；原始游戏文件保持完好。"
			g.NoticeWarn = true
		}
		if m.Page == Gallery {
			g.Token++
			return m, []Effect{loadGalleryEffect(*g, g.Token)}, true
		}
		return m, nil, true

	case GalleryActionCompleted:
		if e.Token != g.Token {
			return m, nil, true
		}
		g.Busy = false
		g.Notice = e.Message
		g.NoticeWarn = e.Gallery.Warn || e.Gallery.DiskFull
		if e.Enabled { // reload requested
			g.Token++
			fx := []Effect{loadGalleryEffect(*g, g.Token)}
			if e.Focused { // rescan requested (e.g. custom source added or auto-discover changed)
				g.Phase = GalleryScanning
				g.ScanToken++
				fx = append(fx, scanGalleryEffect(*g), watchGalleryEffect(*g, m.Page == Gallery || g.AutoArchive))
			}
			return m, fx, true
		}
		return m, nil, true

	case GalleryActionFailed:
		if e.Token != g.Token && e.Token != g.ScanToken {
			return m, nil, true
		}
		g.Busy = false
		if g.Phase == GalleryScanning {
			g.Phase = GalleryFailed
			g.Error = e.Message
		}
		g.Notice = e.Message
		g.NoticeWarn = true
		return m, nil, true

	case GalleryToggleFavorite:
		if m.Page != Gallery || m.Overlay != NoOverlay || g.Busy || e.Gallery.AssetID <= 0 {
			return m, nil, true
		}
		g.Busy = true
		g.Token++
		return m, []Effect{{
			Kind:    EffectGallerySetFavorites,
			Token:   g.Token,
			Enabled: e.Enabled,
			Gallery: GalleryEffectData{AssetID: e.Gallery.AssetID},
		}}, true

	case GalleryBatchFavorite:
		if m.Page != Gallery || m.Overlay != NoOverlay || g.Busy || g.SelectedCnt == 0 {
			return m, nil, true
		}
		g.Busy = true
		g.Token++
		return m, []Effect{{
			Kind:    EffectGallerySetFavorites,
			Token:   g.Token,
			Enabled: e.Enabled,
		}}, true

	case GalleryOpenAlbumModal:
		if m.Page != Gallery || m.Overlay != NoOverlay || g.SettingsOpen || g.ConfirmDeleteID != 0 || g.ConfirmBatchDelete {
			return m, nil, true
		}
		g.TagModalOpen, g.TagModalAssetID = false, 0
		g.AlbumModalOpen = true
		g.AlbumModalAssetID = e.Gallery.AssetID
		return m, nil, true

	case GalleryCloseAlbumModal:
		g.AlbumModalOpen, g.AlbumModalAssetID = false, 0
		return m, nil, true

	case GalleryCreateAlbum:
		if m.Page != Gallery || m.Overlay != NoOverlay || g.Busy || e.Message == "" {
			return m, nil, true
		}
		g.Busy = true
		g.Token++
		return m, []Effect{{
			Kind:    EffectGalleryCreateAlbum,
			Token:   g.Token,
			Gallery: GalleryEffectData{Name: e.Message, AssetID: e.Gallery.AssetID},
		}}, true

	case GalleryRenameAlbum:
		if m.Page != Gallery || m.Overlay != NoOverlay || g.Busy || e.Gallery.AlbumID <= 0 || e.Message == "" {
			return m, nil, true
		}
		g.Busy = true
		g.Token++
		return m, []Effect{{
			Kind:    EffectGalleryRenameAlbum,
			Token:   g.Token,
			Gallery: GalleryEffectData{AlbumID: e.Gallery.AlbumID, Name: e.Message},
		}}, true

	case GalleryDeleteAlbum:
		if m.Page != Gallery || m.Overlay != NoOverlay || g.Busy || e.Gallery.AlbumID <= 0 {
			return m, nil, true
		}
		if g.AlbumID == e.Gallery.AlbumID {
			g.AlbumID = 0
		}
		g.Busy = true
		g.Token++
		return m, []Effect{{
			Kind:    EffectGalleryDeleteAlbum,
			Token:   g.Token,
			Gallery: GalleryEffectData{AlbumID: e.Gallery.AlbumID},
		}}, true

	case GalleryAddToAlbum:
		if m.Page != Gallery || m.Overlay != NoOverlay || g.Busy || e.Gallery.AlbumID <= 0 {
			return m, nil, true
		}
		if e.Gallery.AssetID <= 0 && g.SelectedCnt == 0 {
			return m, nil, true
		}
		g.Busy = true
		g.Token++
		return m, []Effect{{
			Kind:    EffectGalleryAddToAlbum,
			Token:   g.Token,
			Gallery: GalleryEffectData{AlbumID: e.Gallery.AlbumID, AssetID: e.Gallery.AssetID},
		}}, true

	case GalleryRemoveFromAlbum:
		if m.Page != Gallery || m.Overlay != NoOverlay || g.Busy || e.Gallery.AlbumID <= 0 {
			return m, nil, true
		}
		if e.Gallery.AssetID <= 0 && g.SelectedCnt == 0 {
			return m, nil, true
		}
		g.Busy = true
		g.Token++
		return m, []Effect{{
			Kind:    EffectGalleryRemoveFromAlbum,
			Token:   g.Token,
			Gallery: GalleryEffectData{AlbumID: e.Gallery.AlbumID, AssetID: e.Gallery.AssetID},
		}}, true

	case GalleryOpenTagModal:
		if m.Page != Gallery || m.Overlay != NoOverlay || g.SettingsOpen || g.ConfirmDeleteID != 0 || g.ConfirmBatchDelete {
			return m, nil, true
		}
		g.AlbumModalOpen, g.AlbumModalAssetID = false, 0
		g.TagModalOpen = true
		g.TagModalAssetID = e.Gallery.AssetID
		return m, nil, true

	case GalleryCloseTagModal:
		g.TagModalOpen, g.TagModalAssetID = false, 0
		return m, nil, true

	case GalleryAddTag:
		if m.Page != Gallery || m.Overlay != NoOverlay || g.Busy || e.Message == "" {
			return m, nil, true
		}
		if e.Gallery.AssetID <= 0 && g.SelectedCnt == 0 {
			return m, nil, true
		}
		g.Busy = true
		g.Token++
		return m, []Effect{{
			Kind:    EffectGalleryAddTag,
			Token:   g.Token,
			Gallery: GalleryEffectData{Name: e.Message, AssetID: e.Gallery.AssetID},
		}}, true

	case GalleryRemoveTag:
		if m.Page != Gallery || m.Overlay != NoOverlay || g.Busy || e.Gallery.AssetID <= 0 || e.Message == "" {
			return m, nil, true
		}
		g.Busy = true
		g.Token++
		return m, []Effect{{
			Kind:    EffectGalleryRemoveTag,
			Token:   g.Token,
			Gallery: GalleryEffectData{Name: e.Message, AssetID: e.Gallery.AssetID},
		}}, true

	case GalleryArchiveSelected:
		if m.Page != Gallery || m.Overlay != NoOverlay || g.Busy || g.SelectedCnt == 0 {
			return m, nil, true
		}
		g.Busy = true
		g.Token++
		g.Notice, g.NoticeWarn = "正在归档所选媒体…", false
		return m, []Effect{{Kind: EffectGalleryArchiveBatch, Token: g.Token}}, true

	case GalleryRequestBatchDeleteArchive:
		if m.Page != Gallery || m.Overlay != NoOverlay || g.Busy || g.SelectedCnt == 0 {
			return m, nil, true
		}
		g.ConfirmBatchDelete = true
		return m, nil, true

	case GalleryCancelBatchDeleteArchive:
		g.ConfirmBatchDelete = false
		return m, nil, true

	case GalleryConfirmBatchDeleteArchive:
		if !g.ConfirmBatchDelete || g.Busy || g.SelectedCnt == 0 {
			g.ConfirmBatchDelete = false
			return m, nil, true
		}
		g.ConfirmBatchDelete = false
		g.Busy = true
		g.Token++
		return m, []Effect{{Kind: EffectGalleryDeleteArchiveBatch, Token: g.Token}}, true

	case GalleryPruneMissing:
		if m.Page != Gallery || m.Overlay != NoOverlay || g.Busy {
			return m, nil, true
		}
		g.Busy = true
		g.Token++
		return m, []Effect{{Kind: EffectGalleryPruneMissing, Token: g.Token}}, true

	case GallerySetAsBackground:
		if m.Page != Gallery || m.Overlay != NoOverlay || g.Busy || e.Gallery.AssetID <= 0 {
			return m, nil, true
		}
		g.Busy = true
		g.Token++
		return m, []Effect{{
			Kind:    EffectGallerySetCustomBackground,
			Token:   g.Token,
			Gallery: GalleryEffectData{AssetID: e.Gallery.AssetID},
		}}, true

	case GalleryClearCustomBackground:
		if (m.Page != Gallery && m.Page != LauncherSettings) || m.Overlay != NoOverlay || g.Busy || !m.CustomBackground {
			return m, nil, true
		}
		g.Busy = true
		g.Token++
		return m, []Effect{{Kind: EffectGalleryClearCustomBackground, Token: g.Token}}, true

	case GalleryCustomBackgroundLoaded:
		m.CustomBackground = e.Enabled
		return m, nil, true

	case GallerySetViewMode:
		if m.Page != Gallery || m.Overlay != NoOverlay || e.Gallery.ViewMode > GalleryViewTimeline {
			return m, nil, true
		}
		g.ViewMode = e.Gallery.ViewMode
		return m, nil, true
	}

	return m, nil, true
}

func loadGalleryEffect(g GalleryState, token uint64) Effect {
	return Effect{
		Kind:  EffectGalleryLoad,
		Token: token,
		Gallery: GalleryEffectData{
			Scope:   g.Scope,
			Sort:    g.Sort,
			UID:     g.UID,
			AlbumID: g.AlbumID,
			Tag:     g.Tag,
			Search:  g.Search,
		},
	}
}

func scanGalleryEffect(g GalleryState) Effect {
	return Effect{
		Kind:  EffectGalleryScan,
		Token: g.ScanToken,
		Gallery: GalleryEffectData{
			GameRoot: g.GameRoot,
		},
	}
}

func watchGalleryEffect(g GalleryState, active bool) Effect {
	return Effect{
		Kind:    EffectGalleryWatchSync,
		Token:   g.ScanToken,
		Enabled: active,
		Gallery: GalleryEffectData{
			GameRoot: g.GameRoot,
		},
	}
}

func saveGallerySettingsEffect(g GalleryState, token uint64) Effect {
	return Effect{
		Kind:  EffectGallerySaveSettings,
		Token: token,
		Gallery: GalleryEffectData{
			AutoArchive:      g.AutoArchive,
			AutoDiscoverGame: g.AutoDiscoverGame,
			ArchiveDir:       g.ArchiveDir,
			ThumbCacheMB:     g.ThumbCacheMB,
		},
	}
}
