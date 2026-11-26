// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"context"
	"image"
	"time"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// GallerySourceType identifies where a gallery folder originated.
type GallerySourceType string

const (
	GallerySourceGameCache GallerySourceType = "game_cache"
	GallerySourceExported  GallerySourceType = "exported"
	GallerySourceCustom    GallerySourceType = "custom"
)

// GalleryMediaType classifies a gallery item as a photo or video.
type GalleryMediaType string

const (
	GalleryMediaImage GalleryMediaType = "image"
	GalleryMediaVideo GalleryMediaType = "video"
)

// GallerySource is the UI projection of one registered media root.
type GallerySource struct {
	ID         int64
	RootPath   string
	SourceType GallerySourceType
	UID        string
	Enabled    bool
	LastScanAt time.Time
}

// GalleryAlbum is the UI projection of a user-created custom album.
type GalleryAlbum struct {
	ID           int64
	Name         string
	Count        int
	CoverAssetID int64
}

// GalleryTag is the UI projection of a tag with its attached asset count.
type GalleryTag struct {
	ID    int64
	Name  string
	Count int
}

// GalleryQuery describes active filter and sort parameters for LoadSnapshot.
type GalleryQuery struct {
	Scope   state.GalleryScope
	UID     string
	AlbumID int64
	Tag     string
	Search  string
	Sort    state.GallerySort
}

// GalleryAsset is the UI projection of one deduplicated media item.
type GalleryAsset struct {
	ID            int64
	SHA256        string
	MediaType     GalleryMediaType
	MIMEType      string
	ByteSize      int64
	Width         int
	Height        int
	DurationMS    int64
	VideoCodec    string
	AudioCodec    string
	PosterPath    string
	CapturedAt    time.Time // Zero when no trusted EXIF capture time exists.
	ModifiedAt    time.Time
	DisplayTime   time.Time
	FileName      string
	PrimaryPath   string
	SourcePath    string
	ArchivePath   string
	UIDs          []string
	Favorited     bool
	Tags          []string
	AlbumIDs      []int64
	HasCached     bool
	HasExported   bool
	HasCustom     bool
	HasArchived   bool
	Available     bool
	Invalid       bool
	InvalidReason string
}

// IsVideo reports whether a represents a video recording.
func (a GalleryAsset) IsVideo() bool {
	return a.MediaType == GalleryMediaVideo
}

// GallerySettings mirrors persisted gallery preferences.
type GallerySettings struct {
	AutoArchive      bool
	ArchiveDir       string
	AutoDiscoverGame bool
	ThumbCacheMB     int
}

// GalleryStats holds real counts and storage figures reported by the backend.
type GalleryStats struct {
	TotalAssets       int
	PhotoCount        int
	VideoCount        int
	FavoriteCount     int
	CachedCount       int
	ExportedCount     int
	ArchivedCount     int
	MissingCount      int
	InvalidCount      int
	ArchiveCount      int
	ArchiveBytes      int64
	ArchivePhotoCount int
	ArchivePhotoBytes int64
	ArchiveVideoCount int
	ArchiveVideoBytes int64
	ArchiveFreeBytes  int64
	ThumbCacheBytes   int64
}

// GallerySnapshot is one coherent read of filtered assets, sources, albums,
// tags and stats.
type GallerySnapshot struct {
	Assets        []GalleryAsset
	UIDs          []string
	HasUnknownUID bool
	Sources       []GallerySource
	Albums        []GalleryAlbum
	Tags          []GalleryTag
	Stats         GalleryStats
	Settings      GallerySettings
	ArchiveDir    string
}

// GalleryScanSummary reports the result of a scan or index rebuild.
type GalleryScanSummary struct {
	FilesSeen int
	Added     int
	Archived  int
	DiskFull  bool
}

// GalleryArchiveSummary reports the result of archiving existing photos/videos.
type GalleryArchiveSummary struct {
	Archived int
	Dedups   int
	Skipped  int
	Failed   int
	DiskFull bool
}

// GalleryExportSummary reports the result of a batch export.
type GalleryExportSummary struct {
	Exported int
	Failed   int
}

// GalleryBackend is the minimal boundary between the gallery UI and the native
// storage, filesystem and thumbnail cache layer (implemented in internal/launcher/system).
type GalleryBackend interface {
	LoadSnapshot(ctx context.Context, query GalleryQuery) (GallerySnapshot, error)
	SyncAndScan(ctx context.Context, gameRoot string) (GalleryScanSummary, error)
	RebuildIndex(ctx context.Context, gameRoot string) (GalleryScanSummary, error)
	StartWatching(ctx context.Context, gameRoot string, onNotify func(diskFull bool)) error
	StopWatching()
	SaveSettings(ctx context.Context, cfg GallerySettings) error
	AddCustomSource(ctx context.Context, dir string) error
	RemoveCustomSource(ctx context.Context, sourceID int64) error
	ArchiveExisting(ctx context.Context) (GalleryArchiveSummary, error)
	ArchiveAsset(ctx context.Context, asset GalleryAsset) (dedup bool, err error)
	ArchiveBatch(ctx context.Context, assets []GalleryAsset) (GalleryArchiveSummary, error)
	DeleteArchiveCopy(ctx context.Context, assetID int64) error
	DeleteArchiveCopies(ctx context.Context, assetIDs []int64) (int, error)
	PruneMissingUnarchived(ctx context.Context) (int, error)
	ClearThumbCache(ctx context.Context) error
	DecodeThumbnail(ctx context.Context, asset GalleryAsset, edge int) (*image.RGBA, bool, error)
	DecodeOriginal(ctx context.Context, asset GalleryAsset) (*image.RGBA, error)
	ReadClipboardPNG(ctx context.Context, asset GalleryAsset) ([]byte, error)
	ExportSingle(ctx context.Context, asset GalleryAsset, destPath string) error
	ExportBatch(ctx context.Context, assets []GalleryAsset, destDir string) (GalleryExportSummary, error)
	SetFavorites(ctx context.Context, assetIDs []int64, on bool) error
	CreateAlbum(ctx context.Context, name string) (GalleryAlbum, error)
	RenameAlbum(ctx context.Context, albumID int64, name string) error
	DeleteAlbum(ctx context.Context, albumID int64) error
	AddAssetsToAlbum(ctx context.Context, albumID int64, assetIDs []int64) error
	RemoveAssetsFromAlbum(ctx context.Context, albumID int64, assetIDs []int64) error
	AddTagToAssets(ctx context.Context, tag string, assetIDs []int64) error
	RemoveTagFromAsset(ctx context.Context, assetID int64, tag string) error
	SetCustomBackground(ctx context.Context, asset GalleryAsset) (image.Image, error)
	LoadCustomBackground(ctx context.Context) (image.Image, error)
	ClearCustomBackground(ctx context.Context) error
}
