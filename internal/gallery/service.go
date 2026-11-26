// SPDX-License-Identifier: GPL-3.0-or-later

package gallery

import (
	"context"
	"errors"
	"fmt"
	"image"
	"path/filepath"
	"strings"
	"sync"

	"github.com/nerv-base/Oddward/internal/database"
)

// ServiceOptions configures Service creation.
type ServiceOptions struct {
	DataRoot            string
	ThumbCacheDir       string
	ContentRootOverride string
	PicturesDirOverride string
	VideosDirOverride   string
	ArchiveDirOverride  string
	WatcherOptions      WatcherOptions
}

// Snapshot is the read model returned to callers for rendering.
type Snapshot struct {
	Assets        []Asset
	UIDs          []string
	HasUnknownUID bool
	Sources       []Source
	Albums        []Album
	Tags          []Tag
	Stats         Stats
	Settings      Settings
	ArchiveDir    string
}

// Service coordinates gallery storage, source discovery, incremental scanning,
// real-time watching, content-addressed archiving, crash recovery and thumbnail
// caching behind a single serialized mutation boundary.
type Service struct {
	store   *Store
	scanner *Scanner
	thumbs  *ThumbCache
	opts    ServiceOptions

	ops chan struct{} // cancelable serialization lock for scans/mutations

	watchMu     sync.Mutex
	watcher     *Watcher
	watchCancel context.CancelFunc
	watchRoot   string
}

// NewService creates a Gallery Service backed by db.
func NewService(db *database.DB, opts ServiceOptions) (*Service, error) {
	store, err := NewStore(db)
	if err != nil {
		return nil, err
	}
	cfg, err := store.LoadSettings(context.Background())
	if err != nil {
		cfg = DefaultSettings()
	}
	thumbs := NewThumbCache(opts.ThumbCacheDir, cfg.ThumbCacheMB)
	svc := &Service{
		store:   store,
		scanner: NewScanner(store),
		thumbs:  thumbs,
		opts:    opts,
		ops:     make(chan struct{}, 1),
	}
	svc.ops <- struct{}{}
	return svc, nil
}

func (s *Service) acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-s.ops:
		if err := ctx.Err(); err != nil {
			s.ops <- struct{}{}
			return nil, err
		}
		return func() { s.ops <- struct{}{} }, nil
	}
}

// EffectiveArchiveDir resolves the active archive directory from settings or
// default Windows Pictures Known Folder.
func (s *Service) EffectiveArchiveDir(cfg Settings) (string, error) {
	if cfg.ArchiveDir != "" {
		return filepath.Clean(cfg.ArchiveDir), nil
	}
	if s.opts.ArchiveDirOverride != "" {
		return filepath.Clean(s.opts.ArchiveDirOverride), nil
	}
	return DefaultArchiveDir()
}

// LoadSnapshot reads the current indexed view from SQLite without scanning disk.
func (s *Service) LoadSnapshot(ctx context.Context, filter Filter) (Snapshot, error) {
	cfg, err := s.store.LoadSettings(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	archiveDir, _ := s.EffectiveArchiveDir(cfg)
	assets, stats, uids, hasUnknown, err := s.store.QueryAssets(ctx, archiveDir, filter)
	if err != nil {
		return Snapshot{}, err
	}
	sources, err := s.store.ListSources(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	albums, err := s.store.ListAlbums(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	tags, err := s.store.ListTags(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	if s.thumbs != nil {
		stats.ThumbCacheBytes = s.thumbs.UsageBytes()
	}
	if archiveDir != "" {
		if free, ferr := freeSpaceFn(archiveDir); ferr == nil && free > 0 {
			stats.ArchiveFreeBytes = free
		}
	}
	return Snapshot{
		Assets:        assets,
		UIDs:          uids,
		HasUnknownUID: hasUnknown,
		Sources:       sources,
		Albums:        albums,
		Tags:          tags,
		Stats:         stats,
		Settings:      cfg,
		ArchiveDir:    archiveDir,
	}, nil
}

// SyncAndScan reconciles archive state, refreshes discovered sources for
// gameRoot and runs an incremental scan.
func (s *Service) SyncAndScan(ctx context.Context, gameRoot string) (ScanReport, RecoveryReport, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return ScanReport{}, RecoveryReport{}, err
	}
	defer release()

	cfg, err := s.store.LoadSettings(ctx)
	if err != nil {
		return ScanReport{}, RecoveryReport{}, err
	}
	archiveDir, _ := s.EffectiveArchiveDir(cfg)

	var recReport RecoveryReport
	if archiveDir != "" {
		recReport, _ = ReconcileArchive(ctx, s.store, archiveDir)
	}

	sources, err := s.syncSourcesLocked(ctx, gameRoot, cfg)
	if err != nil {
		return ScanReport{}, recReport, err
	}

	report, err := s.scanner.ScanAll(ctx, sources, ScanOptions{
		ArchiveDir:  archiveDir,
		AutoArchive: cfg.AutoArchive,
	})
	return report, recReport, err
}

// RebuildIndex drops cached location/asset index rows (preserving settings,
// custom sources and archive copies), reconciles the archive and performs a
// full rescan.
func (s *Service) RebuildIndex(ctx context.Context, gameRoot string) (ScanReport, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return ScanReport{}, err
	}
	defer release()

	if err := s.store.ResetIndex(ctx); err != nil {
		return ScanReport{}, err
	}
	cfg, err := s.store.LoadSettings(ctx)
	if err != nil {
		return ScanReport{}, err
	}
	archiveDir, _ := s.EffectiveArchiveDir(cfg)
	if archiveDir != "" {
		_, _ = ReconcileArchive(ctx, s.store, archiveDir)
	}
	sources, err := s.syncSourcesLocked(ctx, gameRoot, cfg)
	if err != nil {
		return ScanReport{}, err
	}
	return s.scanner.ScanAll(ctx, sources, ScanOptions{
		ArchiveDir:  archiveDir,
		AutoArchive: cfg.AutoArchive,
	})
}

func (s *Service) syncSourcesLocked(ctx context.Context, gameRoot string, cfg Settings) ([]Source, error) {
	discovered, err := DiscoverSources(ctx, DiscoveryOptions{
		GameRoot:            gameRoot,
		AutoDiscoverGame:    cfg.AutoDiscoverGame,
		ContentRootOverride: s.opts.ContentRootOverride,
		PicturesDirOverride: s.opts.PicturesDirOverride,
		VideosDirOverride:   s.opts.VideosDirOverride,
	})
	if err != nil {
		return nil, err
	}

	activeGame := make(map[string]bool)
	activeExported := make(map[string]bool)
	for _, src := range discovered {
		up, err := s.store.UpsertSource(ctx, src)
		if err != nil {
			continue
		}
		key := strings.ToLower(filepath.Clean(up.RootPath))
		switch up.SourceType {
		case SourceGameCache:
			activeGame[key] = true
		case SourceExported:
			activeExported[key] = true
		}
	}
	if err := s.store.DisableSourcesByTypeNotIn(ctx, SourceGameCache, activeGame); err != nil {
		return nil, err
	}
	if err := s.store.DisableSourcesByTypeNotIn(ctx, SourceExported, activeExported); err != nil {
		return nil, err
	}
	return s.store.ListSources(ctx)
}

// SaveSettings persists gallery preferences and updates thumbnail cache budget.
func (s *Service) SaveSettings(ctx context.Context, cfg Settings) error {
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()

	if err := s.store.SaveSettings(ctx, cfg); err != nil {
		return err
	}
	if s.thumbs != nil {
		s.thumbs.SetBudgetMB(cfg.ThumbCacheMB)
		_ = s.thumbs.Prune(int64(cfg.ThumbCacheMB) << 20)
	}
	return nil
}

// AddCustomSource validates and registers one user-chosen media directory.
func (s *Service) AddCustomSource(ctx context.Context, dir string) (Source, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return Source{}, err
	}
	defer release()

	clean, err := ValidateCustomSource(ctx, dir)
	if err != nil {
		return Source{}, err
	}
	return s.store.UpsertSource(ctx, Source{
		RootPath:   clean,
		SourceType: SourceCustom,
		Enabled:    true,
	})
}

// RemoveCustomSource unregisters a user-added folder without deleting files.
func (s *Service) RemoveCustomSource(ctx context.Context, sourceID int64) error {
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	return s.store.RemoveCustomSource(ctx, sourceID)
}

// ArchiveExisting archives all currently indexed, unarchived game-cache photos.
func (s *Service) ArchiveExisting(ctx context.Context) (ArchiveReport, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return ArchiveReport{}, err
	}
	defer release()

	cfg, err := s.store.LoadSettings(ctx)
	if err != nil {
		return ArchiveReport{}, err
	}
	archiveDir, err := s.EffectiveArchiveDir(cfg)
	if err != nil {
		return ArchiveReport{}, fmt.Errorf("%w: %v", ErrArchiveUnavailable, err)
	}
	return s.scanner.ArchiveExistingGameCache(ctx, archiveDir)
}

// ArchiveOne manually archives one specific asset by ID.
func (s *Service) ArchiveOne(ctx context.Context, asset Asset) (ArchiveResult, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return ArchiveResult{}, err
	}
	defer release()

	cfg, err := s.store.LoadSettings(ctx)
	if err != nil {
		return ArchiveResult{}, err
	}
	archiveDir, err := s.EffectiveArchiveDir(cfg)
	if err != nil {
		return ArchiveResult{}, fmt.Errorf("%w: %v", ErrArchiveUnavailable, err)
	}
	src := asset.SourcePath
	if src == "" {
		src = asset.PrimaryPath
	}
	if src == "" {
		return ArchiveResult{}, ErrNotFound
	}
	return ArchiveFile(ctx, s.store, archiveDir, asset.ID, src, asset.SHA256, asset.MIMEType)
}

// DeleteArchiveCopy removes only the Oddward-managed archive file for assetID.
func (s *Service) DeleteArchiveCopy(ctx context.Context, assetID int64) error {
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()

	cfg, err := s.store.LoadSettings(ctx)
	if err != nil {
		return err
	}
	archiveDir, err := s.EffectiveArchiveDir(cfg)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrArchiveUnavailable, err)
	}
	return DeleteArchiveCopy(ctx, s.store, archiveDir, assetID)
}

// ClearThumbCache deletes cached thumbnails without affecting any photo files.
func (s *Service) ClearThumbCache(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.thumbs == nil {
		return nil
	}
	return s.thumbs.Clear()
}

// DecodeThumbnail returns a cached or freshly decoded RGBA thumbnail.
func (s *Service) DecodeThumbnail(ctx context.Context, sha256Hex, sourcePath string, edge int) (*image.RGBA, bool, error) {
	if s.thumbs != nil {
		return s.thumbs.LoadOrDecode(ctx, sha256Hex, sourcePath, edge)
	}
	img, err := DecodeScaled(ctx, sourcePath, edge)
	return img, false, err
}

// DecodeAssetThumbnail returns the thumbnail for either an image asset or a
// video asset (via its paired keyframe or archived poster backup). For videos
// without a poster image, it returns (nil, false, nil) so callers render a clean
// placeholder without fabricating frames.
func (s *Service) DecodeAssetThumbnail(ctx context.Context, asset Asset, edge int) (*image.RGBA, bool, error) {
	if asset.MediaType == MediaVideo {
		if asset.PosterPath == "" {
			return nil, false, nil
		}
		return s.DecodeThumbnail(ctx, asset.SHA256, asset.PosterPath, edge)
	}
	if asset.PrimaryPath == "" {
		return nil, false, ErrNotFound
	}
	return s.DecodeThumbnail(ctx, asset.SHA256, asset.PrimaryPath, edge)
}

// DecodeOriginal decodes the full-resolution image for viewer inspection.
func (s *Service) DecodeOriginal(ctx context.Context, sourcePath string) (*image.RGBA, error) {
	return DecodeScaled(ctx, sourcePath, 0)
}

// SetFavorites toggles favorite status for assetIDs.
func (s *Service) SetFavorites(ctx context.Context, assetIDs []int64, on bool) error {
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	return s.store.SetFavorites(ctx, assetIDs, on)
}

// CreateAlbum creates a user-defined custom album.
func (s *Service) CreateAlbum(ctx context.Context, name string) (Album, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return Album{}, err
	}
	defer release()
	return s.store.CreateAlbum(ctx, name)
}

// RenameAlbum renames an existing custom album.
func (s *Service) RenameAlbum(ctx context.Context, albumID int64, name string) error {
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	return s.store.RenameAlbum(ctx, albumID, name)
}

// DeleteAlbum removes an album grouping without touching any underlying files.
func (s *Service) DeleteAlbum(ctx context.Context, albumID int64) error {
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	return s.store.DeleteAlbum(ctx, albumID)
}

// AddAssetsToAlbum attaches assetIDs to albumID.
func (s *Service) AddAssetsToAlbum(ctx context.Context, albumID int64, assetIDs []int64) error {
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	return s.store.AddAssetsToAlbum(ctx, albumID, assetIDs)
}

// RemoveAssetsFromAlbum detaches assetIDs from albumID.
func (s *Service) RemoveAssetsFromAlbum(ctx context.Context, albumID int64, assetIDs []int64) error {
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	return s.store.RemoveAssetsFromAlbum(ctx, albumID, assetIDs)
}

// AddTagToAssets attaches tag to assetIDs.
func (s *Service) AddTagToAssets(ctx context.Context, tag string, assetIDs []int64) error {
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	return s.store.AddTagToAssets(ctx, tag, assetIDs)
}

// RemoveTagFromAsset removes tag from assetID.
func (s *Service) RemoveTagFromAsset(ctx context.Context, assetID int64, tag string) error {
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	return s.store.RemoveTagFromAsset(ctx, assetID, tag)
}

// PruneMissingUnarchived clears stale index entries whose source files are gone
// and have no Oddward archive copy. It never touches files on disk.
func (s *Service) PruneMissingUnarchived(ctx context.Context) (int, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer release()
	return s.store.PruneMissingUnarchived(ctx)
}

// DeleteArchiveCopies deletes ONLY Oddward-managed archive copies for assetIDs.
// Original game/user files are never modified or removed.
func (s *Service) DeleteArchiveCopies(ctx context.Context, assetIDs []int64) (int, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer release()

	cfg, err := s.store.LoadSettings(ctx)
	if err != nil {
		return 0, err
	}
	archiveDir, err := s.EffectiveArchiveDir(cfg)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", ErrArchiveUnavailable, err)
	}
	deleted := 0
	var firstErr error
	for _, id := range assetIDs {
		if err := ctx.Err(); err != nil {
			return deleted, err
		}
		if id <= 0 {
			continue
		}
		if err := DeleteArchiveCopy(ctx, s.store, archiveDir, id); err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		deleted++
	}
	return deleted, firstErr
}

// ArchiveBatch manually archives the specified assets into Oddward's archive.
func (s *Service) ArchiveBatch(ctx context.Context, assets []Asset) (ArchiveReport, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return ArchiveReport{}, err
	}
	defer release()

	cfg, err := s.store.LoadSettings(ctx)
	if err != nil {
		return ArchiveReport{}, err
	}
	archiveDir, err := s.EffectiveArchiveDir(cfg)
	if err != nil {
		return ArchiveReport{}, fmt.Errorf("%w: %v", ErrArchiveUnavailable, err)
	}
	var report ArchiveReport
	for _, a := range assets {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if a.Invalid || !a.Available || a.HasArchived {
			report.Skipped++
			continue
		}
		src := a.SourcePath
		if src == "" {
			src = a.PrimaryPath
		}
		if src == "" {
			report.Skipped++
			continue
		}
		res, aerr := ArchiveFile(ctx, s.store, archiveDir, a.ID, src, a.SHA256, a.MIMEType)
		if aerr != nil {
			if errors.Is(aerr, ErrDiskFull) {
				report.DiskFull = true
				report.Failed++
				return report, aerr
			}
			report.Failed++
			continue
		}
		if res.Deduplicated {
			report.Dedups++
		} else {
			report.Archived++
		}
	}
	return report, nil
}

// SetCustomBackground validates asset via imagecheck, persists an independent
// copy inside <DataRoot>\background\, and returns the decoded image for live UI
// activation. Videos and corrupt/unavailable assets are rejected.
func (s *Service) SetCustomBackground(ctx context.Context, asset Asset) (image.Image, error) {
	release, err := s.acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer release()

	if asset.Invalid || !asset.Available || asset.PrimaryPath == "" || asset.MediaType != MediaImage {
		return nil, ErrInvalidMedia
	}
	dest, img, err := saveCustomBackgroundFile(ctx, s.opts.DataRoot, asset.PrimaryPath)
	if err != nil {
		return nil, err
	}
	if err := s.store.SaveCustomBackground(ctx, dest); err != nil {
		return nil, err
	}
	return img, nil
}

// LoadCustomBackground reloads and revalidates the persisted custom static
// background for launcher startup, returning (nil, "", nil) if none is set or
// the managed file no longer exists.
func (s *Service) LoadCustomBackground(ctx context.Context) (image.Image, string, error) {
	cfg, err := s.store.LoadSettings(ctx)
	if err != nil {
		return nil, "", err
	}
	if cfg.CustomBackground == "" {
		return nil, "", nil
	}
	img, err := loadCustomBackgroundFile(ctx, cfg.CustomBackground)
	if err != nil {
		return nil, "", err
	}
	if img == nil {
		return nil, "", nil
	}
	return img, cfg.CustomBackground, nil
}

// ClearCustomBackground clears the active custom background preference and
// removes Oddward's managed copy without affecting user gallery photos.
func (s *Service) ClearCustomBackground(ctx context.Context) error {
	release, err := s.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()

	if err := s.store.SaveCustomBackground(ctx, ""); err != nil {
		return err
	}
	clearCustomBackgroundFiles(ctx, s.opts.DataRoot)
	return nil
}

// StartWatching begins real-time ReadDirectoryChangesW watching for all active
// sources plus parent Selfie directories (so newly created UID folders are
// picked up dynamically). onNotify is invoked when the index changes or a disk
// full condition pauses auto-archiving.
func (s *Service) StartWatching(parent context.Context, gameRoot string, onNotify func(diskFull bool)) error {
	s.StopWatching()

	cfg, err := s.store.LoadSettings(parent)
	if err != nil {
		cfg = DefaultSettings()
	}
	sources, err := s.store.ListSources(parent)
	if err != nil {
		return err
	}

	var roots []WatchRoot
	if cfg.AutoDiscoverGame {
		for _, selfieRoot := range GameSelfieWatchRoots(parent, gameRoot, s.opts.ContentRootOverride) {
			roots = append(roots, WatchRoot{Path: selfieRoot, Subtree: true})
		}
	}
	for _, src := range sources {
		if !src.Enabled {
			continue
		}
		// Game cache sources under Selfie are already covered by Selfie subtree watches;
		// watch exported/custom sources (and any game cache outside Selfie).
		if src.SourceType == SourceGameCache && len(roots) > 0 {
			covered := false
			for _, r := range roots {
				if isWithinRoot(r.Path, src.RootPath) {
					covered = true
					break
				}
			}
			if covered {
				continue
			}
		}
		roots = append(roots, WatchRoot{Path: src.RootPath, Subtree: true})
	}
	if len(roots) == 0 {
		return nil
	}

	wctx, cancel := context.WithCancel(parent)
	watcher, err := StartWatcher(wctx, roots, s.opts.WatcherOptions)
	if err != nil {
		cancel()
		return err
	}

	s.watchMu.Lock()
	s.watcher = watcher
	s.watchCancel = cancel
	s.watchRoot = gameRoot
	s.watchMu.Unlock()

	go s.consumeWatchEvents(wctx, watcher, gameRoot, onNotify)
	return nil
}

// StopWatching shuts down the active directory watcher and releases handles.
func (s *Service) StopWatching() {
	s.watchMu.Lock()
	w := s.watcher
	cancel := s.watchCancel
	s.watcher = nil
	s.watchCancel = nil
	s.watchRoot = ""
	s.watchMu.Unlock()

	if cancel != nil {
		cancel()
	}
	if w != nil {
		w.Close()
	}
}

func (s *Service) consumeWatchEvents(ctx context.Context, w *Watcher, gameRoot string, onNotify func(diskFull bool)) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.Events():
			if !ok {
				return
			}
			if ev.Kind == WatchReconcileOverflow || ev.Kind == WatchReconcileWake {
				rep, _, err := s.SyncAndScan(ctx, gameRoot)
				if err == nil && onNotify != nil {
					onNotify(rep.DiskFull)
				}
				continue
			}
			changed, diskFull := s.handleWatchFileEvent(ctx, gameRoot, ev)
			if (changed || diskFull) && onNotify != nil {
				onNotify(diskFull)
			}
		}
	}
}

func (s *Service) handleWatchFileEvent(ctx context.Context, gameRoot string, ev WatchEvent) (changed bool, diskFull bool) {
	release, err := s.acquire(ctx)
	if err != nil {
		return false, false
	}
	defer release()

	cfg, err := s.store.LoadSettings(ctx)
	if err != nil {
		return false, false
	}
	archiveDir, _ := s.EffectiveArchiveDir(cfg)
	fullPath := joinNormalized(ev.Root, ev.RelPath)

	sources, err := s.store.ListSources(ctx)
	if err != nil {
		return false, false
	}
	var matched *Source
	var relFromSource string
	for i := range sources {
		if !sources[i].Enabled {
			continue
		}
		if isWithinRoot(sources[i].RootPath, fullPath) {
			if rel, rerr := filepath.Rel(sources[i].RootPath, fullPath); rerr == nil && rel != "." {
				matched = &sources[i]
				relFromSource = rel
				break
			}
		}
	}

	// If a new <UID>/ScreenShots directory appeared under Selfie, re-discover sources!
	if matched == nil && cfg.AutoDiscoverGame {
		updated, serr := s.syncSourcesLocked(ctx, gameRoot, cfg)
		if serr == nil {
			for i := range updated {
				if !updated[i].Enabled {
					continue
				}
				if isWithinRoot(updated[i].RootPath, fullPath) {
					if rel, rerr := filepath.Rel(updated[i].RootPath, fullPath); rerr == nil && rel != "." {
						matched = &updated[i]
						relFromSource = rel
						break
					}
				}
			}
		}
	}
	if matched == nil {
		return false, false
	}

	if ev.Kind == WatchFileRemoved {
		normRel, rerr := NormalizeRelativePath(relFromSource)
		if rerr != nil {
			return false, false
		}
		if merr := s.store.MarkLocationUnavailable(ctx, matched.ID, normRel); merr == nil {
			return true, false
		}
		return false, false
	}

	ch, full, serr := s.scanner.ScanSingleFile(ctx, *matched, relFromSource, ScanOptions{
		ArchiveDir:  archiveDir,
		AutoArchive: cfg.AutoArchive,
	})
	if errors.Is(serr, ErrDiskFull) {
		return true, true
	}
	return ch, full
}

// Close releases all background watcher resources.
func (s *Service) Close() {
	s.StopWatching()
}
