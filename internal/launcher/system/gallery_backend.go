// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"context"
	"errors"
	"image"
	"path/filepath"

	"github.com/nerv-base/Oddward/internal/database"
	"github.com/nerv-base/Oddward/internal/gallery"
	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// GalleryBackend adapts internal/gallery.Service to launcher.GalleryBackend.
type GalleryBackend struct {
	svc *gallery.Service
}

var _ launcher.GalleryBackend = (*GalleryBackend)(nil)

// NewGalleryBackend constructs the production gallery adapter backed by Oddward's
// shared SQLite database and disk thumbnail cache under dataDir/cache/gallery/thumbnails.
func NewGalleryBackend(db *database.DB, dataDir string) (*GalleryBackend, error) {
	if !filepath.IsAbs(dataDir) {
		return nil, errors.New("gallery data directory must be an absolute path")
	}
	svc, err := gallery.NewService(db, gallery.ServiceOptions{
		DataRoot:      dataDir,
		ThumbCacheDir: filepath.Join(dataDir, "cache", "gallery", "thumbnails"),
	})
	if err != nil {
		return nil, err
	}
	return &GalleryBackend{svc: svc}, nil
}

// Close releases active directory watchers.
func (g *GalleryBackend) Close() {
	if g != nil && g.svc != nil {
		g.svc.Close()
	}
}

func (g *GalleryBackend) LoadSnapshot(ctx context.Context, query launcher.GalleryQuery) (launcher.GallerySnapshot, error) {
	snap, err := g.svc.LoadSnapshot(ctx, gallery.Filter{
		Scope:   mapGalleryScope(query.Scope),
		UID:     query.UID,
		AlbumID: query.AlbumID,
		Tag:     query.Tag,
		Query:   query.Search,
		Sort:    mapGallerySort(query.Sort),
	})
	if err != nil {
		return launcher.GallerySnapshot{}, err
	}
	out := launcher.GallerySnapshot{
		Assets:        make([]launcher.GalleryAsset, len(snap.Assets)),
		UIDs:          snap.UIDs,
		HasUnknownUID: snap.HasUnknownUID,
		Sources:       make([]launcher.GallerySource, len(snap.Sources)),
		Albums:        make([]launcher.GalleryAlbum, len(snap.Albums)),
		Tags:          make([]launcher.GalleryTag, len(snap.Tags)),
		Stats: launcher.GalleryStats{
			TotalAssets:       snap.Stats.TotalAssets,
			PhotoCount:        snap.Stats.PhotoCount,
			VideoCount:        snap.Stats.VideoCount,
			FavoriteCount:     snap.Stats.FavoriteCount,
			CachedCount:       snap.Stats.CachedCount,
			ExportedCount:     snap.Stats.ExportedCount,
			ArchivedCount:     snap.Stats.ArchivedCount,
			MissingCount:      snap.Stats.MissingCount,
			InvalidCount:      snap.Stats.InvalidCount,
			ArchiveCount:      snap.Stats.ArchiveCount,
			ArchiveBytes:      snap.Stats.ArchiveBytes,
			ArchivePhotoCount: snap.Stats.ArchivePhotoCount,
			ArchivePhotoBytes: snap.Stats.ArchivePhotoBytes,
			ArchiveVideoCount: snap.Stats.ArchiveVideoCount,
			ArchiveVideoBytes: snap.Stats.ArchiveVideoBytes,
			ArchiveFreeBytes:  snap.Stats.ArchiveFreeBytes,
			ThumbCacheBytes:   snap.Stats.ThumbCacheBytes,
		},
		Settings: launcher.GallerySettings{
			AutoArchive:      snap.Settings.AutoArchive,
			ArchiveDir:       snap.Settings.ArchiveDir,
			AutoDiscoverGame: snap.Settings.AutoDiscoverGame,
			ThumbCacheMB:     snap.Settings.ThumbCacheMB,
		},
		ArchiveDir: snap.ArchiveDir,
	}
	for i, a := range snap.Assets {
		out.Assets[i] = toLauncherAsset(a)
	}
	for i, s := range snap.Sources {
		out.Sources[i] = launcher.GallerySource{
			ID:         s.ID,
			RootPath:   s.RootPath,
			SourceType: launcher.GallerySourceType(s.SourceType),
			UID:        s.UID,
			Enabled:    s.Enabled,
			LastScanAt: s.LastScanAt,
		}
	}
	for i, al := range snap.Albums {
		out.Albums[i] = launcher.GalleryAlbum{
			ID:           al.ID,
			Name:         al.Name,
			Count:        al.Count,
			CoverAssetID: al.CoverAssetID,
		}
	}
	for i, tag := range snap.Tags {
		out.Tags[i] = launcher.GalleryTag{
			ID:    tag.ID,
			Name:  tag.Name,
			Count: tag.Count,
		}
	}
	return out, nil
}

func (g *GalleryBackend) SyncAndScan(ctx context.Context, gameRoot string) (launcher.GalleryScanSummary, error) {
	rep, _, err := g.svc.SyncAndScan(ctx, gameRoot)
	return launcher.GalleryScanSummary{
		FilesSeen: rep.FilesSeen,
		Added:     rep.Added,
		Archived:  rep.Archived,
		DiskFull:  rep.DiskFull,
	}, err
}

func (g *GalleryBackend) RebuildIndex(ctx context.Context, gameRoot string) (launcher.GalleryScanSummary, error) {
	rep, err := g.svc.RebuildIndex(ctx, gameRoot)
	return launcher.GalleryScanSummary{
		FilesSeen: rep.FilesSeen,
		Added:     rep.Added,
		Archived:  rep.Archived,
		DiskFull:  rep.DiskFull,
	}, err
}

func (g *GalleryBackend) StartWatching(ctx context.Context, gameRoot string, onNotify func(diskFull bool)) error {
	return g.svc.StartWatching(ctx, gameRoot, onNotify)
}

func (g *GalleryBackend) StopWatching() {
	g.svc.StopWatching()
}

func (g *GalleryBackend) SaveSettings(ctx context.Context, cfg launcher.GallerySettings) error {
	return g.svc.SaveSettings(ctx, gallery.Settings{
		AutoArchive:      cfg.AutoArchive,
		ArchiveDir:       cfg.ArchiveDir,
		AutoDiscoverGame: cfg.AutoDiscoverGame,
		ThumbCacheMB:     cfg.ThumbCacheMB,
	})
}

func (g *GalleryBackend) AddCustomSource(ctx context.Context, dir string) error {
	_, err := g.svc.AddCustomSource(ctx, dir)
	return err
}

func (g *GalleryBackend) RemoveCustomSource(ctx context.Context, sourceID int64) error {
	return g.svc.RemoveCustomSource(ctx, sourceID)
}

func (g *GalleryBackend) ArchiveExisting(ctx context.Context) (launcher.GalleryArchiveSummary, error) {
	rep, err := g.svc.ArchiveExisting(ctx)
	return launcher.GalleryArchiveSummary{
		Archived: rep.Archived,
		Dedups:   rep.Dedups,
		Skipped:  rep.Skipped,
		Failed:   rep.Failed,
		DiskFull: rep.DiskFull,
	}, err
}

func (g *GalleryBackend) ArchiveAsset(ctx context.Context, asset launcher.GalleryAsset) (bool, error) {
	res, err := g.svc.ArchiveOne(ctx, toDomainAsset(asset))
	return res.Deduplicated, err
}

func (g *GalleryBackend) ArchiveBatch(ctx context.Context, assets []launcher.GalleryAsset) (launcher.GalleryArchiveSummary, error) {
	domainAssets := make([]gallery.Asset, len(assets))
	for i, a := range assets {
		domainAssets[i] = toDomainAsset(a)
	}
	rep, err := g.svc.ArchiveBatch(ctx, domainAssets)
	return launcher.GalleryArchiveSummary{
		Archived: rep.Archived,
		Dedups:   rep.Dedups,
		Skipped:  rep.Skipped,
		Failed:   rep.Failed,
		DiskFull: rep.DiskFull,
	}, err
}

func (g *GalleryBackend) DeleteArchiveCopy(ctx context.Context, assetID int64) error {
	return g.svc.DeleteArchiveCopy(ctx, assetID)
}

func (g *GalleryBackend) DeleteArchiveCopies(ctx context.Context, assetIDs []int64) (int, error) {
	return g.svc.DeleteArchiveCopies(ctx, assetIDs)
}

func (g *GalleryBackend) PruneMissingUnarchived(ctx context.Context) (int, error) {
	return g.svc.PruneMissingUnarchived(ctx)
}

func (g *GalleryBackend) ClearThumbCache(ctx context.Context) error {
	return g.svc.ClearThumbCache(ctx)
}

func (g *GalleryBackend) DecodeThumbnail(ctx context.Context, asset launcher.GalleryAsset, edge int) (*image.RGBA, bool, error) {
	if asset.Invalid || !asset.Available {
		return nil, false, gallery.ErrNotFound
	}
	return g.svc.DecodeAssetThumbnail(ctx, toDomainAsset(asset), edge)
}

func (g *GalleryBackend) DecodeOriginal(ctx context.Context, asset launcher.GalleryAsset) (*image.RGBA, error) {
	if asset.PrimaryPath == "" || asset.Invalid || !asset.Available || asset.IsVideo() {
		return nil, gallery.ErrNotFound
	}
	return g.svc.DecodeOriginal(ctx, asset.PrimaryPath)
}

func (g *GalleryBackend) ReadClipboardPNG(ctx context.Context, asset launcher.GalleryAsset) ([]byte, error) {
	if asset.PrimaryPath == "" || asset.Invalid || !asset.Available || asset.IsVideo() {
		return nil, gallery.ErrNotFound
	}
	return gallery.ReadClipboardPNG(ctx, asset.PrimaryPath)
}

func (g *GalleryBackend) ExportSingle(ctx context.Context, asset launcher.GalleryAsset, destPath string) error {
	return gallery.ExportAsset(ctx, toDomainAsset(asset), destPath)
}

func (g *GalleryBackend) ExportBatch(ctx context.Context, assets []launcher.GalleryAsset, destDir string) (launcher.GalleryExportSummary, error) {
	domainAssets := make([]gallery.Asset, len(assets))
	for i, a := range assets {
		domainAssets[i] = toDomainAsset(a)
	}
	rep, err := gallery.ExportBatch(ctx, domainAssets, destDir)
	return launcher.GalleryExportSummary{
		Exported: rep.Exported,
		Failed:   rep.Failed,
	}, err
}

func (g *GalleryBackend) SetFavorites(ctx context.Context, assetIDs []int64, on bool) error {
	return g.svc.SetFavorites(ctx, assetIDs, on)
}

func (g *GalleryBackend) CreateAlbum(ctx context.Context, name string) (launcher.GalleryAlbum, error) {
	al, err := g.svc.CreateAlbum(ctx, name)
	return launcher.GalleryAlbum{ID: al.ID, Name: al.Name, Count: al.Count, CoverAssetID: al.CoverAssetID}, err
}

func (g *GalleryBackend) RenameAlbum(ctx context.Context, albumID int64, name string) error {
	return g.svc.RenameAlbum(ctx, albumID, name)
}

func (g *GalleryBackend) DeleteAlbum(ctx context.Context, albumID int64) error {
	return g.svc.DeleteAlbum(ctx, albumID)
}

func (g *GalleryBackend) AddAssetsToAlbum(ctx context.Context, albumID int64, assetIDs []int64) error {
	return g.svc.AddAssetsToAlbum(ctx, albumID, assetIDs)
}

func (g *GalleryBackend) RemoveAssetsFromAlbum(ctx context.Context, albumID int64, assetIDs []int64) error {
	return g.svc.RemoveAssetsFromAlbum(ctx, albumID, assetIDs)
}

func (g *GalleryBackend) AddTagToAssets(ctx context.Context, tag string, assetIDs []int64) error {
	return g.svc.AddTagToAssets(ctx, tag, assetIDs)
}

func (g *GalleryBackend) RemoveTagFromAsset(ctx context.Context, assetID int64, tag string) error {
	return g.svc.RemoveTagFromAsset(ctx, assetID, tag)
}

func (g *GalleryBackend) SetCustomBackground(ctx context.Context, asset launcher.GalleryAsset) (image.Image, error) {
	return g.svc.SetCustomBackground(ctx, toDomainAsset(asset))
}

func (g *GalleryBackend) LoadCustomBackground(ctx context.Context) (image.Image, error) {
	img, _, err := g.svc.LoadCustomBackground(ctx)
	return img, err
}

func (g *GalleryBackend) ClearCustomBackground(ctx context.Context) error {
	return g.svc.ClearCustomBackground(ctx)
}

func mapGalleryScope(s state.GalleryScope) gallery.Scope {
	switch s {
	case state.GalleryScopeCached:
		return gallery.ScopeCached
	case state.GalleryScopeExported:
		return gallery.ScopeExported
	case state.GalleryScopeArchived:
		return gallery.ScopeArchived
	case state.GalleryScopeFavorites:
		return gallery.ScopeFavorites
	case state.GalleryScopeVideos:
		return gallery.ScopeVideos
	default:
		return gallery.ScopeAll
	}
}

func mapGallerySort(s state.GallerySort) gallery.SortOrder {
	switch s {
	case state.GallerySortOldest:
		return gallery.SortOldest
	case state.GallerySortLargest:
		return gallery.SortLargest
	default:
		return gallery.SortNewest
	}
}

func toLauncherAsset(a gallery.Asset) launcher.GalleryAsset {
	mt := launcher.GalleryMediaImage
	if a.MediaType == gallery.MediaVideo {
		mt = launcher.GalleryMediaVideo
	}
	return launcher.GalleryAsset{
		ID:            a.ID,
		SHA256:        a.SHA256,
		MediaType:     mt,
		MIMEType:      a.MIMEType,
		ByteSize:      a.ByteSize,
		Width:         a.Width,
		Height:        a.Height,
		DurationMS:    a.DurationMS,
		VideoCodec:    a.VideoCodec,
		AudioCodec:    a.AudioCodec,
		PosterPath:    a.PosterPath,
		CapturedAt:    a.CapturedAt,
		ModifiedAt:    a.ModifiedAt,
		DisplayTime:   a.DisplayTime,
		FileName:      a.FileName,
		PrimaryPath:   a.PrimaryPath,
		SourcePath:    a.SourcePath,
		ArchivePath:   a.ArchivePath,
		UIDs:          a.UIDs,
		Favorited:     a.Favorited,
		Tags:          a.Tags,
		AlbumIDs:      a.AlbumIDs,
		HasCached:     a.HasCached,
		HasExported:   a.HasExported,
		HasCustom:     a.HasCustom,
		HasArchived:   a.HasArchived,
		Available:     a.Available,
		Invalid:       a.Invalid,
		InvalidReason: a.InvalidReason,
	}
}

func toDomainAsset(a launcher.GalleryAsset) gallery.Asset {
	mt := gallery.MediaImage
	if a.MediaType == launcher.GalleryMediaVideo {
		mt = gallery.MediaVideo
	}
	return gallery.Asset{
		ID:          a.ID,
		SHA256:      a.SHA256,
		MediaType:   mt,
		MIMEType:    a.MIMEType,
		ByteSize:    a.ByteSize,
		Width:       a.Width,
		Height:      a.Height,
		DurationMS:  a.DurationMS,
		VideoCodec:  a.VideoCodec,
		AudioCodec:  a.AudioCodec,
		PosterPath:  a.PosterPath,
		FileName:    a.FileName,
		PrimaryPath: a.PrimaryPath,
		SourcePath:  a.SourcePath,
		ArchivePath: a.ArchivePath,
		HasArchived: a.HasArchived,
		Available:   a.Available,
		Invalid:     a.Invalid,
	}
}
