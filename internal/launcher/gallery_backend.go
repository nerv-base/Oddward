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

// GallerySource is the UI projection of one registered media root.
type GallerySource struct {
	ID         int64
	RootPath   string
	SourceType GallerySourceType
	UID        string
	Enabled    bool
	LastScanAt time.Time
}

// GalleryAsset is the UI projection of one deduplicated media item.
type GalleryAsset struct {
	ID            int64
	SHA256        string
	MIMEType      string
	ByteSize      int64
	Width         int
	Height        int
	CapturedAt    time.Time // Zero when no trusted EXIF capture time exists.
	ModifiedAt    time.Time
	DisplayTime   time.Time
	FileName      string
	PrimaryPath   string
	SourcePath    string
	ArchivePath   string
	UIDs          []string
	HasCached     bool
	HasExported   bool
	HasCustom     bool
	HasArchived   bool
	Available     bool
	Invalid       bool
	InvalidReason string
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
	TotalAssets     int
	CachedCount     int
	ExportedCount   int
	ArchivedCount   int
	MissingCount    int
	InvalidCount    int
	ArchiveCount    int
	ArchiveBytes    int64
	ThumbCacheBytes int64
}

// GallerySnapshot is one coherent read of filtered assets, sources and stats.
type GallerySnapshot struct {
	Assets        []GalleryAsset
	UIDs          []string
	HasUnknownUID bool
	Sources       []GallerySource
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

// GalleryArchiveSummary reports the result of archiving existing photos.
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
	LoadSnapshot(ctx context.Context, scope state.GalleryScope, uid string, sort state.GallerySort) (GallerySnapshot, error)
	SyncAndScan(ctx context.Context, gameRoot string) (GalleryScanSummary, error)
	RebuildIndex(ctx context.Context, gameRoot string) (GalleryScanSummary, error)
	StartWatching(ctx context.Context, gameRoot string, onNotify func(diskFull bool)) error
	StopWatching()
	SaveSettings(ctx context.Context, cfg GallerySettings) error
	AddCustomSource(ctx context.Context, dir string) error
	RemoveCustomSource(ctx context.Context, sourceID int64) error
	ArchiveExisting(ctx context.Context) (GalleryArchiveSummary, error)
	ArchiveAsset(ctx context.Context, asset GalleryAsset) (dedup bool, err error)
	DeleteArchiveCopy(ctx context.Context, assetID int64) error
	ClearThumbCache(ctx context.Context) error
	DecodeThumbnail(ctx context.Context, asset GalleryAsset, edge int) (*image.RGBA, bool, error)
	DecodeOriginal(ctx context.Context, asset GalleryAsset) (*image.RGBA, error)
	ReadClipboardPNG(ctx context.Context, asset GalleryAsset) ([]byte, error)
	ExportSingle(ctx context.Context, asset GalleryAsset, destPath string) error
	ExportBatch(ctx context.Context, assets []GalleryAsset, destDir string) (GalleryExportSummary, error)
}
