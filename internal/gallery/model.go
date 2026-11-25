// SPDX-License-Identifier: GPL-3.0-or-later

// Package gallery implements local-first media discovery, indexing, file
// watching, content-addressed archiving, crash recovery and tiered thumbnail
// caching for Hethereau Gallery (海特洛相册).
//
// It never modifies, moves or deletes game source files, never makes network
// requests and never logs raw user photos, full UIDs or sensitive paths.
package gallery

import (
	"errors"
	"time"
)

// Domain errors reported by gallery operations.
var (
	ErrUnsafePath         = errors.New("gallery: unsafe or non-local path")
	ErrInvalidMedia       = errors.New("gallery: invalid or unsupported media")
	ErrTooLarge           = errors.New("gallery: media exceeds size or pixel budget")
	ErrDiskFull           = errors.New("gallery: insufficient disk space")
	ErrNotFound           = errors.New("gallery: asset or source not found")
	ErrArchiveUnavailable = errors.New("gallery: archive directory unavailable")
	ErrInvalidSettings    = errors.New("gallery: invalid settings")
	ErrFileUnstable       = errors.New("gallery: file is still changing")
)

// SourceType classifies where a media directory comes from.
type SourceType string

const (
	SourceGameCache SourceType = "game_cache"
	SourceExported  SourceType = "exported"
	SourceCustom    SourceType = "custom"
)

// MediaType classifies an indexed asset.
type MediaType string

const (
	MediaImage MediaType = "image"
	MediaVideo MediaType = "video"
)

// ArchiveStatus tracks the integrity state of an Oddward-managed archive copy.
type ArchiveStatus string

const (
	ArchiveStatusReady   ArchiveStatus = "ready"
	ArchiveStatusMissing ArchiveStatus = "missing"
	ArchiveStatusCorrupt ArchiveStatus = "corrupt"
)

// AssetStatus is the primary display status of an asset. Note that a single
// asset may simultaneously exist in game cache, exported directories and the
// Oddward archive (see HasCached/HasExported/HasArchived).
type AssetStatus uint8

const (
	StatusCached AssetStatus = iota
	StatusExported
	StatusArchived
	StatusMissing
	StatusInvalid
)

// Scope filters assets by provenance/availability/media kind.
type Scope uint8

const (
	ScopeAll Scope = iota
	ScopeCached
	ScopeExported
	ScopeArchived
	ScopeFavorites
	ScopeVideos
)

// SortOrder controls how assets are ordered.
type SortOrder uint8

const (
	SortNewest SortOrder = iota
	SortOldest
	SortLargest
)

// UIDUnknown is the sentinel filter value matching assets with no known UID
// (for example exported or custom-folder photos).
const UIDUnknown = "__unknown__"

// Default configuration bounds.
const (
	DefaultThumbCacheMB = 256
	MinThumbCacheMB     = 32
	MaxThumbCacheMB     = 4096

	// ArchiveFreeReserveBytes keeps safety headroom on the target volume so
	// auto-archive stops before exhausting disk space.
	ArchiveFreeReserveBytes int64 = 64 << 20
)

// Settings stores persisted gallery preferences. AutoArchive defaults to false
// and must only be enabled by explicit user consent.
type Settings struct {
	AutoArchive      bool
	ArchiveDir       string
	AutoDiscoverGame bool
	ThumbCacheMB     int
	CustomBackground string
}

// DefaultSettings returns the initial gallery settings for a fresh profile.
func DefaultSettings() Settings {
	return Settings{
		AutoArchive:      false,
		ArchiveDir:       "",
		AutoDiscoverGame: true,
		ThumbCacheMB:     DefaultThumbCacheMB,
	}
}

// Source describes one registered media root directory.
type Source struct {
	ID         int64
	RootPath   string
	SourceType SourceType
	Channel    string
	UID        string
	Enabled    bool
	LastScanAt time.Time
}

// Location binds one asset to one file path inside a Source.
type Location struct {
	ID           int64
	AssetID      int64
	SourceID     int64
	SourceType   SourceType
	SourceRoot   string
	UID          string
	RelativePath string
	FileSize     int64
	ModifiedAt   time.Time
	FileIdentity string
	Available    bool
	LastSeenAt   time.Time
}

// FullPath returns the normalized absolute path of this location on disk.
func (l Location) FullPath() string {
	return joinNormalized(l.SourceRoot, l.RelativePath)
}

// ArchiveRecord describes the independent copy saved in Oddward's archive.
type ArchiveRecord struct {
	AssetID      int64
	RelativePath string
	ArchivedAt   time.Time
	Status       ArchiveStatus
}

// MediaInfo holds validated media headers extracted without decoding full pixels.
type MediaInfo struct {
	MediaType   MediaType
	MIMEType    string
	Width       int
	Height      int
	ByteSize    int64
	DurationMS  int64
	VideoCodec  string
	AudioCodec  string
	PosterPath  string
	CapturedAt  time.Time // Zero (NULL in SQLite) when no trusted EXIF timestamp exists.
	Orientation int       // EXIF orientation 1..8 (1 = upright).
}

// Asset is the aggregated view of one deduplicated media item.
type Asset struct {
	ID            int64
	SHA256        string
	MediaType     MediaType
	MIMEType      string
	ByteSize      int64
	Width         int
	Height        int
	DurationMS    int64
	VideoCodec    string
	AudioCodec    string
	PosterPath    string    // Companion keyframe or archived poster path for videos, if available.
	CapturedAt    time.Time // Zero when unknown; never faked from file mtime.
	DiscoveredAt  time.Time
	ModifiedAt    time.Time // Latest observed file modification time across locations.
	DisplayTime   time.Time // CapturedAt if known, else ModifiedAt, else DiscoveredAt.
	FileName      string    // Representative file name for display/export.
	PrimaryPath   string    // Best readable file path on disk (archive preferred if ready, else available location).
	SourcePath    string    // Best available original source location path, if any.
	ArchivePath   string    // Full path to Oddward archive copy if recorded.
	ArchiveStatus ArchiveStatus
	UIDs          []string
	Locations     []Location
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

// Status returns the asset's summarized state.
func (a Asset) Status() AssetStatus {
	if a.Invalid {
		return StatusInvalid
	}
	if !a.Available {
		return StatusMissing
	}
	if a.HasArchived {
		return StatusArchived
	}
	if a.HasExported || a.HasCustom {
		return StatusExported
	}
	return StatusCached
}

// Filter narrows and sorts queried assets.
type Filter struct {
	Scope   Scope
	UID     string
	AlbumID int64
	Tag     string
	Query   string
	Sort    SortOrder
}

// Stats summarizes indexed assets and storage usage from real records.
type Stats struct {
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

// ScanReport summarizes one incremental or full scan pass.
type ScanReport struct {
	SourcesScanned int
	FilesSeen      int
	Unchanged      int
	Added          int
	Updated        int
	MissingMarked  int
	InvalidCount   int
	Archived       int
	ArchiveSkipped int
	ArchiveErrors  int
	DiskFull       bool
}

// ArchiveReport summarizes a batch archive pass.
type ArchiveReport struct {
	Archived int
	Dedups   int
	Skipped  int
	Failed   int
	DiskFull bool
}

// ExportReport summarizes a batch export operation.
type ExportReport struct {
	Exported int
	Failed   int
}
