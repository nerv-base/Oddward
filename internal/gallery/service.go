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
	ThumbCacheDir       string
	ContentRootOverride string
	PicturesDirOverride string
	ArchiveDirOverride  string
	WatcherOptions      WatcherOptions
}

// Snapshot is the read model returned to callers for rendering.
type Snapshot struct {
	Assets        []Asset
	UIDs          []string
	HasUnknownUID bool
	Sources       []Source
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
	if s.thumbs != nil {
		stats.ThumbCacheBytes = s.thumbs.UsageBytes()
	}
	return Snapshot{
		Assets:        assets,
		UIDs:          uids,
		HasUnknownUID: hasUnknown,
		Sources:       sources,
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

// DecodeOriginal decodes the full-resolution image for viewer inspection.
func (s *Service) DecodeOriginal(ctx context.Context, sourcePath string) (*image.RGBA, error) {
	return DecodeScaled(ctx, sourcePath, 0)
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
