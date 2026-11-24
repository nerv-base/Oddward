// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

type galleryRowKind uint8

const (
	galleryRowHeader galleryRowKind = iota
	galleryRowCards
)

// galleryRow is one virtualized row in the date-grouped photo grid.
type galleryRow struct {
	kind  galleryRowKind
	date  string
	count int
	items []int // indices into galleryRuntime.assets
}

// galleryViewerState keeps interactive pan/zoom state on the UI thread.
type galleryViewerState struct {
	assetID    int64
	fit        bool
	zoom       float32
	panX, panY float32
	dragging   bool
	dragLastX  float32
	dragLastY  float32
}

func (v *galleryViewerState) reset(assetID int64) {
	v.assetID = assetID
	v.fit = true
	v.zoom = 1
	v.panX, v.panY = 0, 0
	v.dragging = false
}

type galleryResult struct {
	effect   state.Effect
	snapshot GallerySnapshot
	scan     GalleryScanSummary
	archive  GalleryArchiveSummary
	export   GalleryExportSummary
	dedup    bool
	path     string
	err      error
}

// galleryRuntime holds all non-comparable UI-thread gallery projections,
// virtual list state, selection set, bitmap cache and bounded async channels.
type galleryRuntime struct {
	assets        []GalleryAsset
	uids          []string
	hasUnknownUID bool
	sources       []GallerySource
	selected      map[int64]bool
	listState     ui.ListState
	rows          []galleryRow
	rowsCols      int
	rowsRev       uint64
	builtRev      uint64
	images        galleryImageCache
	viewer        galleryViewerState
	settingsTab   int

	out         chan galleryResult
	watchNotify chan bool
	scanCancel  context.CancelFunc
	watching    bool
	watchRoot   string
	dpiScale    float32
}

func (s *Shell) initGallery() {
	if s.gallery.out != nil {
		return
	}
	s.gallery.out = make(chan galleryResult, 16)
	s.gallery.watchNotify = make(chan bool, 4)
	s.gallery.viewer.fit = true
	s.gallery.viewer.zoom = 1
}

func (s *Shell) disposeGallery() {
	if s.gallery.scanCancel != nil {
		s.gallery.scanCancel()
		s.gallery.scanCancel = nil
	}
	if s.GalleryBackend != nil && s.gallery.watching {
		s.GalleryBackend.StopWatching()
		s.gallery.watching = false
	}
	s.gallery.images.releaseAllBitmaps()
}

func (s *Shell) galleryAssetByID(id int64) (GalleryAsset, bool) {
	for _, a := range s.gallery.assets {
		if a.ID == id {
			return a, true
		}
	}
	return GalleryAsset{}, false
}

func (s *Shell) galleryAssetIndex(id int64) int {
	for i, a := range s.gallery.assets {
		if a.ID == id {
			return i
		}
	}
	return -1
}

func (s *Shell) gallerySelectedAssets() []GalleryAsset {
	if len(s.gallery.selected) == 0 {
		return nil
	}
	out := make([]GalleryAsset, 0, len(s.gallery.selected))
	for _, a := range s.gallery.assets {
		if s.gallery.selected[a.ID] {
			out = append(out, a)
		}
	}
	return out
}

func (s *Shell) toggleGallerySelect(id int64) {
	if s.gallery.selected == nil {
		s.gallery.selected = make(map[int64]bool)
	}
	if s.gallery.selected[id] {
		delete(s.gallery.selected, id)
	} else {
		s.gallery.selected[id] = true
	}
	s.Dispatch(state.Event{
		Kind: state.GallerySelectionChanged,
		Gallery: state.GalleryPayload{
			SelectedCount: len(s.gallery.selected),
			FocusedID:     id,
		},
	})
}

func (s *Shell) galleryDateRows(cols int, loc *time.Location) []galleryRow {
	if cols < 1 {
		cols = 1
	}
	if s.gallery.rowsCols == cols && s.gallery.builtRev == s.gallery.rowsRev && s.gallery.rows != nil {
		return s.gallery.rows
	}
	if loc == nil {
		loc = time.Local
	}
	assets := s.gallery.assets
	if len(assets) == 0 {
		s.gallery.rows = nil
		s.gallery.rowsCols = cols
		s.gallery.builtRev = s.gallery.rowsRev
		return nil
	}

	var rows []galleryRow
	i := 0
	for i < len(assets) {
		day := formatGalleryDateGroup(assets[i].DisplayTime, loc)
		start := i
		for i < len(assets) && formatGalleryDateGroup(assets[i].DisplayTime, loc) == day {
			i++
		}
		groupCount := i - start
		rows = append(rows, galleryRow{
			kind:  galleryRowHeader,
			date:  day,
			count: groupCount,
		})
		for j := start; j < i; j += cols {
			end := min(j+cols, i)
			items := make([]int, end-j)
			for k := range items {
				items[k] = j + k
			}
			rows = append(rows, galleryRow{
				kind:  galleryRowCards,
				date:  day,
				items: items,
			})
		}
	}
	s.gallery.rows = rows
	s.gallery.rowsCols = cols
	s.gallery.builtRev = s.gallery.rowsRev
	return rows
}

func formatGalleryDateGroup(t time.Time, loc *time.Location) string {
	if t.IsZero() {
		return "未知日期"
	}
	local := t.In(loc)
	return fmt.Sprintf("%04d年%02d月%02d日", local.Year(), local.Month(), local.Day())
}

func (s *Shell) applyGalleryEffect(f state.Effect) bool {
	if f.Kind < state.EffectGalleryLoad || f.Kind > state.EffectGalleryViewerStep {
		return false
	}
	s.initGallery()

	switch f.Kind {
	case state.EffectGallerySelectAll:
		if s.gallery.selected == nil {
			s.gallery.selected = make(map[int64]bool, len(s.gallery.assets))
		}
		for _, a := range s.gallery.assets {
			s.gallery.selected[a.ID] = true
		}
		return true

	case state.EffectGalleryViewerStep:
		idx := s.galleryAssetIndex(f.Gallery.AssetID)
		if idx < 0 || len(s.gallery.assets) == 0 {
			return true
		}
		nextIdx := idx + f.Gallery.Step
		if nextIdx < 0 || nextIdx >= len(s.gallery.assets) {
			return true
		}
		nextID := s.gallery.assets[nextIdx].ID
		s.gallery.viewer.reset(nextID)
		s.Dispatch(state.Event{Kind: state.GalleryOpenViewer, Gallery: state.GalleryPayload{AssetID: nextID}})
		return true

	case state.EffectGalleryOpenFolder:
		if s.OpenFolder != nil && f.Path != "" {
			s.OpenFolder(f.Path)
		}
		return true

	case state.EffectGalleryRevealAsset:
		if asset, ok := s.galleryAssetByID(f.Gallery.AssetID); ok {
			path := asset.SourcePath
			if path == "" {
				path = asset.PrimaryPath
			}
			if path != "" {
				if s.RevealFile != nil {
					s.RevealFile(path)
				} else if s.OpenFolder != nil {
					s.OpenFolder(filepath.Dir(path))
				}
			}
		}
		return true

	case state.EffectGalleryWatchSync:
		s.syncGalleryWatcher(f.Enabled, f.Gallery.GameRoot)
		return true
	}

	// Async effects run off the UI thread and report through s.gallery.out.
	backend, ctx, out, wake := s.GalleryBackend, s.ctx, s.gallery.out, s.RequestFrame
	if f.Kind == state.EffectGalleryScan || f.Kind == state.EffectGalleryRebuildIndex {
		if s.gallery.scanCancel != nil {
			s.gallery.scanCancel()
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithCancel(s.ctx)
		s.gallery.scanCancel = cancel
	}

	// Snapshot asset or batch arguments on UI thread before spawning goroutine.
	var targetAsset GalleryAsset
	var batchAssets []GalleryAsset
	if f.Gallery.AssetID > 0 {
		targetAsset, _ = s.galleryAssetByID(f.Gallery.AssetID)
	}
	if f.Kind == state.EffectGalleryExportBatch {
		batchAssets = s.gallerySelectedAssets()
	}

	pickDir := s.PickGalleryDirectory
	pickExport := s.PickGalleryExportFile
	writeClip := s.WriteClipboardImage

	go func() {
		res := galleryResult{effect: f}
		if ctx.Err() != nil {
			return
		}
		switch f.Kind {
		case state.EffectGalleryPickArchiveDir:
			if pickDir != nil {
				res.path, res.err = pickDir(ctx, "选择海特洛相册归档目录")
			}
		case state.EffectGalleryPickCustomSource:
			if pickDir != nil {
				res.path, res.err = pickDir(ctx, "添加自定义媒体目录")
			}
		case state.EffectGalleryPickExportDir:
			if pickDir != nil {
				res.path, res.err = pickDir(ctx, "选择批量导出文件夹")
			}
		case state.EffectGalleryPickExportSingle:
			if pickExport != nil {
				suggested := targetAsset.FileName
				if suggested == "" {
					suggested = "hethereau-photo.png"
				}
				res.path, res.err = pickExport(ctx, suggested)
			}
		default:
			if backend == nil {
				res.err = errors.New("相册后端服务不可用")
				break
			}
			switch f.Kind {
			case state.EffectGalleryLoad:
				res.snapshot, res.err = backend.LoadSnapshot(ctx, f.Gallery.Scope, f.Gallery.UID, f.Gallery.Sort)
			case state.EffectGalleryScan:
				res.scan, res.err = backend.SyncAndScan(ctx, f.Gallery.GameRoot)
			case state.EffectGalleryRebuildIndex:
				res.scan, res.err = backend.RebuildIndex(ctx, f.Gallery.GameRoot)
			case state.EffectGallerySaveSettings:
				res.err = backend.SaveSettings(ctx, GallerySettings{
					AutoArchive:      f.Gallery.AutoArchive,
					AutoDiscoverGame: f.Gallery.AutoDiscoverGame,
					ArchiveDir:       f.Gallery.ArchiveDir,
					ThumbCacheMB:     f.Gallery.ThumbCacheMB,
				})
			case state.EffectGalleryAddCustomSource:
				res.err = backend.AddCustomSource(ctx, f.Path)
			case state.EffectGalleryRemoveCustomSource:
				res.err = backend.RemoveCustomSource(ctx, f.Gallery.SourceID)
			case state.EffectGalleryArchiveExisting:
				res.archive, res.err = backend.ArchiveExisting(ctx)
			case state.EffectGalleryArchiveAsset:
				res.dedup, res.err = backend.ArchiveAsset(ctx, targetAsset)
			case state.EffectGalleryDeleteArchive:
				res.err = backend.DeleteArchiveCopy(ctx, f.Gallery.AssetID)
			case state.EffectGalleryClearThumbCache:
				res.err = backend.ClearThumbCache(ctx)
			case state.EffectGalleryCopyImage:
				if writeClip == nil {
					res.err = errors.New("系统剪贴板不可用")
					break
				}
				pngBytes, err := backend.ReadClipboardPNG(ctx, targetAsset)
				if err != nil {
					res.err = err
					break
				}
				res.err = writeClip(pngBytes)
			case state.EffectGalleryExportSingle:
				res.err = backend.ExportSingle(ctx, targetAsset, f.Path)
			case state.EffectGalleryExportBatch:
				res.export, res.err = backend.ExportBatch(ctx, batchAssets, f.Path)
			}
		}

		if ctx.Err() != nil || errors.Is(res.err, context.Canceled) {
			return
		}
		select {
		case out <- res:
			if wake != nil && s.ctx.Err() == nil {
				wake()
			}
		case <-s.ctx.Done():
		}
	}()
	return true
}

func (s *Shell) syncGalleryWatcher(wantActive bool, gameRoot string) {
	if s.GalleryBackend == nil {
		return
	}
	if !wantActive {
		if s.gallery.watching {
			s.GalleryBackend.StopWatching()
			s.gallery.watching = false
			s.gallery.watchRoot = ""
		}
		return
	}
	if s.gallery.watching && strings.EqualFold(s.gallery.watchRoot, gameRoot) {
		return
	}
	notifyCh := s.gallery.watchNotify
	wake := s.RequestFrame
	ctx := s.ctx
	err := s.GalleryBackend.StartWatching(ctx, gameRoot, func(diskFull bool) {
		select {
		case notifyCh <- diskFull:
		default:
		}
		if wake != nil && ctx.Err() == nil {
			wake()
		}
	})
	if err == nil {
		s.gallery.watching = true
		s.gallery.watchRoot = gameRoot
	}
}

func (s *Shell) syncGallery() {
	s.initGallery()

	// Keep Gallery informed when the user locates or switches the game root.
	if s.Model.Managed.Root != s.Model.Gallery.GameRoot {
		s.Dispatch(state.Event{Kind: state.GalleryGameRootChanged, Path: s.Model.Managed.Root})
	}

	// Drain watcher notifications.
	for {
		select {
		case diskFull := <-s.gallery.watchNotify:
			s.Dispatch(state.Event{Kind: state.GalleryWatchChanged, Gallery: state.GalleryPayload{DiskFull: diskFull}})
		default:
			goto drainResults
		}
	}

drainResults:
	for {
		select {
		case r := <-s.gallery.out:
			if s.Model.Closed {
				continue
			}
			s.handleGalleryResult(r)
		default:
			goto syncImages
		}
	}

syncImages:
	if !s.Model.Gallery.SelectMode && len(s.gallery.selected) > 0 {
		clear(s.gallery.selected)
	}
	s.syncGalleryImages()
}

func (s *Shell) handleGalleryResult(r galleryResult) {
	f := r.effect
	if r.err != nil {
		s.Dispatch(state.Event{
			Kind:    state.GalleryActionFailed,
			Token:   f.Token,
			Message: state.BriefError(r.err),
		})
		return
	}
	switch f.Kind {
	case state.EffectGalleryLoad:
		if f.Token != s.Model.Gallery.Token {
			return
		}
		s.gallery.assets = r.snapshot.Assets
		s.gallery.uids = r.snapshot.UIDs
		s.gallery.hasUnknownUID = r.snapshot.HasUnknownUID
		s.gallery.sources = r.snapshot.Sources
		s.gallery.rowsRev++
		// Drop selections for assets no longer in the filtered slice.
		if len(s.gallery.selected) > 0 {
			valid := make(map[int64]bool, len(r.snapshot.Assets))
			for _, a := range r.snapshot.Assets {
				valid[a.ID] = true
			}
			for id := range s.gallery.selected {
				if !valid[id] {
					delete(s.gallery.selected, id)
				}
			}
		}
		s.Dispatch(state.Event{
			Kind:  state.GallerySnapshotLoaded,
			Token: f.Token,
			Gallery: state.GalleryPayload{
				AutoArchive:      r.snapshot.Settings.AutoArchive,
				AutoDiscoverGame: r.snapshot.Settings.AutoDiscoverGame,
				ArchiveDir:       r.snapshot.Settings.ArchiveDir,
				EffectiveArchive: r.snapshot.ArchiveDir,
				ThumbCacheMB:     r.snapshot.Settings.ThumbCacheMB,
				TotalCount:       r.snapshot.Stats.TotalAssets,
				FilteredCount:    len(r.snapshot.Assets),
				CachedCount:      r.snapshot.Stats.CachedCount,
				ExportedCount:    r.snapshot.Stats.ExportedCount,
				ArchivedCount:    r.snapshot.Stats.ArchivedCount,
				MissingCount:     r.snapshot.Stats.MissingCount,
				InvalidCount:     r.snapshot.Stats.InvalidCount,
				ArchiveBytes:     r.snapshot.Stats.ArchiveBytes,
				ThumbCacheBytes:  r.snapshot.Stats.ThumbCacheBytes,
				Watching:         s.gallery.watching,
			},
		})
		// Ensure watcher is active if user opened Gallery or auto-archive is enabled.
		if s.Model.Page == state.Gallery || r.snapshot.Settings.AutoArchive {
			s.syncGalleryWatcher(true, s.Model.Gallery.GameRoot)
		}

	case state.EffectGalleryScan:
		msg := ""
		if r.scan.Archived > 0 {
			msg = fmt.Sprintf("扫描完成 · 自动归档 %d 张照片", r.scan.Archived)
		}
		s.Dispatch(state.Event{
			Kind:    state.GalleryScanCompleted,
			Token:   f.Token,
			Message: msg,
			Gallery: state.GalleryPayload{DiskFull: r.scan.DiskFull},
		})

	case state.EffectGalleryRebuildIndex:
		s.gallery.images.generation++
		s.Dispatch(state.Event{
			Kind:    state.GalleryScanCompleted,
			Token:   s.Model.Gallery.ScanToken,
			Message: fmt.Sprintf("索引已重建 · 共发现 %d 项媒体", r.scan.FilesSeen),
			Gallery: state.GalleryPayload{DiskFull: r.scan.DiskFull},
		})

	case state.EffectGalleryPickArchiveDir:
		s.Dispatch(state.Event{Kind: state.GalleryArchiveDirPicked, Token: f.Token, Path: r.path})

	case state.EffectGalleryPickCustomSource:
		s.Dispatch(state.Event{Kind: state.GalleryCustomSourcePicked, Token: f.Token, Path: r.path})

	case state.EffectGalleryPickExportSingle:
		s.Dispatch(state.Event{Kind: state.GalleryExportSinglePicked, Token: f.Token, Path: r.path, Gallery: state.GalleryPayload{AssetID: f.Gallery.AssetID}})

	case state.EffectGalleryPickExportDir:
		s.Dispatch(state.Event{Kind: state.GalleryExportBatchPicked, Token: f.Token, Path: r.path})

	case state.EffectGallerySaveSettings:
		s.Dispatch(state.Event{
			Kind:    state.GalleryActionCompleted,
			Token:   f.Token,
			Message: "相册设置已保存",
			Enabled: true,
			Focused: true, // rescan & resync watcher on settings change
		})

	case state.EffectGalleryAddCustomSource:
		s.Dispatch(state.Event{
			Kind:    state.GalleryActionCompleted,
			Token:   f.Token,
			Message: "已添加自定义媒体目录",
			Enabled: true,
			Focused: true,
		})

	case state.EffectGalleryRemoveCustomSource:
		s.Dispatch(state.Event{
			Kind:    state.GalleryActionCompleted,
			Token:   f.Token,
			Message: "已移除自定义目录（本地文件未删除）",
			Enabled: true,
		})

	case state.EffectGalleryArchiveExisting:
		msg := fmt.Sprintf("已归档 %d 张游戏照片", r.archive.Archived)
		if r.archive.Archived == 0 && r.archive.Dedups == 0 {
			msg = "所有可用游戏照片均已归档"
		} else if r.archive.Dedups > 0 {
			msg += fmt.Sprintf("（去重跳过 %d 张）", r.archive.Dedups)
		}
		if r.archive.DiskFull {
			msg = "磁盘空间不足，归档已提前停止；源文件保持完好"
		}
		s.Dispatch(state.Event{
			Kind:    state.GalleryActionCompleted,
			Token:   f.Token,
			Message: msg,
			Enabled: true,
			Gallery: state.GalleryPayload{DiskFull: r.archive.DiskFull, Warn: r.archive.DiskFull || r.archive.Failed > 0},
		})

	case state.EffectGalleryArchiveAsset:
		msg := "已归档到 Oddward 相册目录"
		if r.dedup {
			msg = "该照片已有相同内容的归档副本"
		}
		s.Dispatch(state.Event{Kind: state.GalleryActionCompleted, Token: f.Token, Message: msg, Enabled: true})

	case state.EffectGalleryDeleteArchive:
		s.Dispatch(state.Event{Kind: state.GalleryActionCompleted, Token: f.Token, Message: "已删除 Oddward 归档副本", Enabled: true})

	case state.EffectGalleryClearThumbCache:
		s.gallery.images.generation++
		s.gallery.images.releaseAllBitmaps()
		s.Dispatch(state.Event{Kind: state.GalleryActionCompleted, Token: f.Token, Message: "缩略图缓存已清理", Enabled: true})

	case state.EffectGalleryCopyImage:
		s.Dispatch(state.Event{Kind: state.GalleryActionCompleted, Token: f.Token, Message: "图片已复制到剪贴板"})

	case state.EffectGalleryExportSingle:
		s.Dispatch(state.Event{Kind: state.GalleryActionCompleted, Token: f.Token, Message: "图片已导出"})

	case state.EffectGalleryExportBatch:
		msg := fmt.Sprintf("已导出 %d 张照片", r.export.Exported)
		if r.export.Failed > 0 {
			msg += fmt.Sprintf("，%d 张失败", r.export.Failed)
		}
		s.Dispatch(state.Event{
			Kind:    state.GalleryActionCompleted,
			Token:   f.Token,
			Message: msg,
			Gallery: state.GalleryPayload{Warn: r.export.Failed > 0},
		})
	}
}
