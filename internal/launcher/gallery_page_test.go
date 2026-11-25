// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

type fakeGalleryBackend struct {
	mu               sync.Mutex
	assets           []GalleryAsset
	uids             []string
	hasUnknown       bool
	sources          []GallerySource
	albums           []GalleryAlbum
	tags             []GalleryTag
	settings         GallerySettings
	archiveDir       string
	archiveFreeBytes int64
	thumbBytes       int64
	loadErr          error
	scanErr          error
	scanDiskFull     bool
	watching         bool
	copyCount        int
	exportSingle     string
	exportBatch      int
	clearedThumbs    int
	rebuiltCount     int
	prunedMissing    int
	archivedOne      int64
	archivedBatch    int
	deletedArch      int64
	deletedArchBatch int
	customBGSet      int64
	customBGCleared  int
	nextAlbumID      int64
}

func newFakeGalleryBackend(assets ...GalleryAsset) *fakeGalleryBackend {
	uidSet := make(map[string]bool)
	var uids []string
	hasUnknown := false
	for _, a := range assets {
		if len(a.UIDs) == 0 {
			hasUnknown = true
		}
		for _, u := range a.UIDs {
			if !uidSet[u] {
				uidSet[u] = true
				uids = append(uids, u)
			}
		}
	}
	return &fakeGalleryBackend{
		assets:           assets,
		uids:             uids,
		hasUnknown:       hasUnknown,
		settings:         GallerySettings{AutoArchive: false, AutoDiscoverGame: true, ThumbCacheMB: 256},
		archiveDir:       `C:\Users\Test\Pictures\Oddward\Gallery`,
		archiveFreeBytes: 120 << 30,
		thumbBytes:       12 << 20,
		nextAlbumID:      10,
	}
}

func (f *fakeGalleryBackend) rebuildTagsLocked() {
	counts := make(map[string]int)
	for _, a := range f.assets {
		for _, t := range a.Tags {
			counts[t]++
		}
	}
	f.tags = nil
	var id int64 = 1
	for name, cnt := range counts {
		f.tags = append(f.tags, GalleryTag{ID: id, Name: name, Count: cnt})
		id++
	}
	for i := range f.albums {
		cnt := 0
		for _, a := range f.assets {
			if slices.Contains(a.AlbumIDs, f.albums[i].ID) {
				cnt++
			}
		}
		f.albums[i].Count = cnt
	}
}

func (f *fakeGalleryBackend) LoadSnapshot(_ context.Context, query GalleryQuery) (GallerySnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.loadErr != nil {
		return GallerySnapshot{}, f.loadErr
	}
	f.rebuildTagsLocked()
	var filtered []GalleryAsset
	var stats GalleryStats
	stats.ThumbCacheBytes = f.thumbBytes
	stats.ArchiveFreeBytes = f.archiveFreeBytes
	for _, a := range f.assets {
		stats.TotalAssets++
		if a.IsVideo() {
			stats.VideoCount++
		} else {
			stats.PhotoCount++
		}
		if a.Favorited {
			stats.FavoriteCount++
		}
		if a.Invalid {
			stats.InvalidCount++
		} else {
			if a.HasCached {
				stats.CachedCount++
			}
			if a.HasExported || a.HasCustom {
				stats.ExportedCount++
			}
			if a.HasArchived {
				stats.ArchivedCount++
				stats.ArchiveCount++
				stats.ArchiveBytes += a.ByteSize
				if a.IsVideo() {
					stats.ArchiveVideoCount++
					stats.ArchiveVideoBytes += a.ByteSize
				} else {
					stats.ArchivePhotoCount++
					stats.ArchivePhotoBytes += a.ByteSize
				}
			}
			if !a.Available {
				stats.MissingCount++
			}
		}
		switch query.Scope {
		case state.GalleryScopeCached:
			if !a.HasCached {
				continue
			}
		case state.GalleryScopeExported:
			if !a.HasExported && !a.HasCustom {
				continue
			}
		case state.GalleryScopeArchived:
			if !a.HasArchived {
				continue
			}
		case state.GalleryScopeFavorites:
			if !a.Favorited {
				continue
			}
		case state.GalleryScopeVideos:
			if !a.IsVideo() {
				continue
			}
		}
		if query.UID == state.GalleryUIDUnknown && len(a.UIDs) > 0 {
			continue
		} else if query.UID != "" && query.UID != state.GalleryUIDUnknown {
			if !slices.Contains(a.UIDs, query.UID) {
				continue
			}
		}
		if query.AlbumID > 0 && !slices.Contains(a.AlbumIDs, query.AlbumID) {
			continue
		}
		if query.Tag != "" {
			found := false
			for _, t := range a.Tags {
				if strings.EqualFold(t, query.Tag) {
					found = true
					break
				}
			}
			if !found {
				continue
			}
		}
		if q := strings.ToLower(strings.TrimSpace(query.Search)); q != "" {
			hay := strings.ToLower(a.FileName + " " + strings.Join(a.Tags, " ") + " " + strings.Join(a.UIDs, " "))
			if !strings.Contains(hay, q) {
				continue
			}
		}
		filtered = append(filtered, a)
	}
	return GallerySnapshot{
		Assets:        filtered,
		UIDs:          f.uids,
		HasUnknownUID: f.hasUnknown,
		Sources:       f.sources,
		Albums:        slices.Clone(f.albums),
		Tags:          slices.Clone(f.tags),
		Stats:         stats,
		Settings:      f.settings,
		ArchiveDir:    f.archiveDir,
	}, nil
}

func (f *fakeGalleryBackend) SyncAndScan(_ context.Context, _ string) (GalleryScanSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.scanErr != nil {
		return GalleryScanSummary{}, f.scanErr
	}
	return GalleryScanSummary{FilesSeen: len(f.assets), DiskFull: f.scanDiskFull}, nil
}

func (f *fakeGalleryBackend) RebuildIndex(_ context.Context, _ string) (GalleryScanSummary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rebuiltCount++
	return GalleryScanSummary{FilesSeen: len(f.assets)}, nil
}

func (f *fakeGalleryBackend) StartWatching(_ context.Context, _ string, _ func(bool)) error {
	f.mu.Lock()
	f.watching = true
	f.mu.Unlock()
	return nil
}

func (f *fakeGalleryBackend) StopWatching() {
	f.mu.Lock()
	f.watching = false
	f.mu.Unlock()
}

func (f *fakeGalleryBackend) SaveSettings(_ context.Context, cfg GallerySettings) error {
	f.mu.Lock()
	f.settings = cfg
	f.mu.Unlock()
	return nil
}

func (f *fakeGalleryBackend) AddCustomSource(_ context.Context, dir string) error {
	f.mu.Lock()
	f.sources = append(f.sources, GallerySource{ID: int64(len(f.sources) + 1), RootPath: dir, SourceType: GallerySourceCustom, Enabled: true})
	f.mu.Unlock()
	return nil
}

func (f *fakeGalleryBackend) RemoveCustomSource(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, s := range f.sources {
		if s.ID == id {
			f.sources = append(f.sources[:i], f.sources[i+1:]...)
			return nil
		}
	}
	return nil
}

func (f *fakeGalleryBackend) ArchiveExisting(_ context.Context) (GalleryArchiveSummary, error) {
	return GalleryArchiveSummary{Archived: 2}, nil
}

func (f *fakeGalleryBackend) ArchiveAsset(_ context.Context, a GalleryAsset) (bool, error) {
	f.mu.Lock()
	f.archivedOne = a.ID
	f.mu.Unlock()
	return false, nil
}

func (f *fakeGalleryBackend) ArchiveBatch(_ context.Context, assets []GalleryAsset) (GalleryArchiveSummary, error) {
	f.mu.Lock()
	f.archivedBatch += len(assets)
	f.mu.Unlock()
	return GalleryArchiveSummary{Archived: len(assets)}, nil
}

func (f *fakeGalleryBackend) DeleteArchiveCopy(_ context.Context, id int64) error {
	f.mu.Lock()
	f.deletedArch = id
	for i := range f.assets {
		if f.assets[i].ID == id {
			f.assets[i].HasArchived = false
		}
	}
	f.mu.Unlock()
	return nil
}

func (f *fakeGalleryBackend) DeleteArchiveCopies(_ context.Context, ids []int64) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	removed := 0
	for _, id := range ids {
		for i := range f.assets {
			if f.assets[i].ID == id && f.assets[i].HasArchived {
				f.assets[i].HasArchived = false
				removed++
			}
		}
	}
	f.deletedArchBatch += removed
	return removed, nil
}

func (f *fakeGalleryBackend) PruneMissingUnarchived(_ context.Context) (int, error) {
	f.mu.Lock()
	f.prunedMissing++
	f.mu.Unlock()
	return 1, nil
}

func (f *fakeGalleryBackend) SetFavorites(_ context.Context, ids []int64, on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.assets {
		if slices.Contains(ids, f.assets[i].ID) {
			f.assets[i].Favorited = on
		}
	}
	return nil
}

func (f *fakeGalleryBackend) CreateAlbum(_ context.Context, name string) (GalleryAlbum, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextAlbumID++
	al := GalleryAlbum{ID: f.nextAlbumID, Name: name}
	f.albums = append(f.albums, al)
	return al, nil
}

func (f *fakeGalleryBackend) RenameAlbum(_ context.Context, albumID int64, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.albums {
		if f.albums[i].ID == albumID {
			f.albums[i].Name = name
			return nil
		}
	}
	return errors.New("not found")
}

func (f *fakeGalleryBackend) DeleteAlbum(_ context.Context, albumID int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.albums {
		if f.albums[i].ID == albumID {
			f.albums = append(f.albums[:i], f.albums[i+1:]...)
			break
		}
	}
	for i := range f.assets {
		f.assets[i].AlbumIDs = slices.DeleteFunc(f.assets[i].AlbumIDs, func(id int64) bool { return id == albumID })
	}
	return nil
}

func (f *fakeGalleryBackend) AddAssetsToAlbum(_ context.Context, albumID int64, ids []int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.assets {
		if slices.Contains(ids, f.assets[i].ID) && !slices.Contains(f.assets[i].AlbumIDs, albumID) {
			f.assets[i].AlbumIDs = append(f.assets[i].AlbumIDs, albumID)
		}
	}
	return nil
}

func (f *fakeGalleryBackend) RemoveAssetsFromAlbum(_ context.Context, albumID int64, ids []int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.assets {
		if slices.Contains(ids, f.assets[i].ID) {
			f.assets[i].AlbumIDs = slices.DeleteFunc(f.assets[i].AlbumIDs, func(id int64) bool { return id == albumID })
		}
	}
	return nil
}

func (f *fakeGalleryBackend) AddTagToAssets(_ context.Context, tag string, ids []int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.assets {
		if slices.Contains(ids, f.assets[i].ID) && !slices.Contains(f.assets[i].Tags, tag) {
			f.assets[i].Tags = append(f.assets[i].Tags, tag)
		}
	}
	return nil
}

func (f *fakeGalleryBackend) RemoveTagFromAsset(_ context.Context, assetID int64, tag string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.assets {
		if f.assets[i].ID == assetID {
			f.assets[i].Tags = slices.DeleteFunc(f.assets[i].Tags, func(t string) bool { return t == tag })
		}
	}
	return nil
}

func (f *fakeGalleryBackend) SetCustomBackground(ctx context.Context, asset GalleryAsset) (image.Image, error) {
	f.mu.Lock()
	f.customBGSet = asset.ID
	f.mu.Unlock()
	return f.DecodeOriginal(ctx, asset)
}

func (f *fakeGalleryBackend) LoadCustomBackground(_ context.Context) (image.Image, error) {
	return nil, nil
}

func (f *fakeGalleryBackend) ClearCustomBackground(_ context.Context) error {
	f.mu.Lock()
	f.customBGCleared++
	f.mu.Unlock()
	return nil
}

func (f *fakeGalleryBackend) ClearThumbCache(_ context.Context) error {
	f.mu.Lock()
	f.clearedThumbs++
	f.thumbBytes = 0
	f.mu.Unlock()
	return nil
}

func (f *fakeGalleryBackend) DecodeThumbnail(_ context.Context, a GalleryAsset, edge int) (*image.RGBA, bool, error) {
	w, h := min(edge, 320), min(edge*9/16, 180)
	if a.Width > 0 && a.Height > 0 {
		if a.Width >= a.Height {
			w = min(edge, 384)
			h = max(1, w*a.Height/a.Width)
		} else {
			h = min(edge, 384)
			w = max(1, h*a.Width/a.Height)
		}
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	// Paint a subtle synthetic city-night horizon gradient (distinct per asset)
	// so visual previews clearly demonstrate ui.Contain framing without any
	// copyrighted game media.
	palettes := [][2][3]uint8{
		{{22, 34, 62}, {190, 128, 84}},
		{{32, 24, 58}, {112, 168, 210}},
		{{20, 42, 48}, {214, 174, 96}},
		{{44, 24, 48}, {228, 124, 152}},
		{{18, 36, 52}, {92, 198, 184}},
		{{36, 30, 24}, {216, 152, 86}},
	}
	pal := palettes[int(a.ID)%len(palettes)]
	horizon := h * 58 / 100
	for y := 0; y < h; y++ {
		t := float64(y) / float64(max(h-1, 1))
		r := uint8(float64(pal[0][0])*(1-t) + float64(pal[1][0])*t)
		g := uint8(float64(pal[0][1])*(1-t) + float64(pal[1][1])*t)
		b := uint8(float64(pal[0][2])*(1-t) + float64(pal[1][2])*t)
		if y >= horizon {
			r = uint8(int(r) * 35 / 100)
			g = uint8(int(g) * 35 / 100)
			b = uint8(int(b) * 45 / 100)
		}
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
		}
	}
	return img, true, nil
}

func (f *fakeGalleryBackend) DecodeOriginal(ctx context.Context, a GalleryAsset) (*image.RGBA, error) {
	px, _, err := f.DecodeThumbnail(ctx, a, 320)
	return px, err
}

func (f *fakeGalleryBackend) ReadClipboardPNG(_ context.Context, _ GalleryAsset) ([]byte, error) {
	f.mu.Lock()
	f.copyCount++
	f.mu.Unlock()
	return []byte{0x89, 'P', 'N', 'G'}, nil
}

func (f *fakeGalleryBackend) ExportSingle(_ context.Context, _ GalleryAsset, dest string) error {
	f.mu.Lock()
	f.exportSingle = dest
	f.mu.Unlock()
	return nil
}

func (f *fakeGalleryBackend) ExportBatch(_ context.Context, assets []GalleryAsset, _ string) (GalleryExportSummary, error) {
	f.mu.Lock()
	f.exportBatch = len(assets)
	f.mu.Unlock()
	return GalleryExportSummary{Exported: len(assets)}, nil
}

func sampleGalleryAssets() []GalleryAsset {
	day1 := time.Date(2026, 11, 24, 16, 52, 2, 0, time.FixedZone("CST", 8*3600))
	day2 := time.Date(2026, 11, 23, 19, 10, 0, 0, time.FixedZone("CST", 8*3600))
	return []GalleryAsset{
		{
			ID: 1, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			MIMEType: "image/png", ByteSize: 1_825_842, Width: 1280, Height: 720,
			ModifiedAt: day1, DisplayTime: day1, FileName: "26_11_24_16_52_02.png",
			PrimaryPath: `D:\Games\NTE\Selfie\100001\ScreenShots\26_11_24_16_52_02.png`,
			SourcePath:  `D:\Games\NTE\Selfie\100001\ScreenShots\26_11_24_16_52_02.png`,
			UIDs:        []string{"100001"}, HasCached: true, HasArchived: true, Available: true,
		},
		{
			ID: 2, SHA256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			MIMEType: "image/png", ByteSize: 4_885_173, Width: 2642, Height: 1555,
			ModifiedAt: day1.Add(-5 * time.Minute), DisplayTime: day1.Add(-5 * time.Minute),
			FileName:    "26_11_24_16_47_20.png",
			PrimaryPath: `C:\Users\Test\Pictures\NevernessToEverness\26_11_24_16_47_20.png`,
			SourcePath:  `C:\Users\Test\Pictures\NevernessToEverness\26_11_24_16_47_20.png`,
			HasExported: true, Available: true,
		},
		{
			ID: 3, SHA256: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
			MIMEType: "image/png", ByteSize: 1_810_000, Width: 1280, Height: 720,
			ModifiedAt: day2, DisplayTime: day2, FileName: "26_11_23_19_10_00.png",
			PrimaryPath: `D:\Games\NTE\Selfie\200002\ScreenShots\26_11_23_19_10_00.png`,
			SourcePath:  `D:\Games\NTE\Selfie\200002\ScreenShots\26_11_23_19_10_00.png`,
			UIDs:        []string{"200002"}, HasCached: true, Available: true,
		},
		{
			ID: 4, SHA256: "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd",
			MIMEType: "application/octet-stream", ByteSize: 512, Width: 0, Height: 0,
			ModifiedAt: day2.Add(-time.Minute), DisplayTime: day2.Add(-time.Minute),
			FileName: "corrupt.png", Invalid: true, InvalidReason: "图片已损坏",
		},
	}
}

func settleGallery(t *testing.T, tt *ui.Tester, pred func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tt.Frame()
		if pred() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for gallery async UI state")
}

func TestGalleryEmptyScanningFailedAndMultiUIDFlows(t *testing.T) {
	// 1. Empty state.
	s := NewShell(settled(state.Ready))
	t.Cleanup(s.Dispose)
	s.GalleryBackend = newFakeGalleryBackend()
	tt := ui.NewTester(s.View, 1280, 720)
	if clickFailed(tt, "相册") || s.Model.Page != state.Gallery {
		t.Fatal("failed to navigate to 相册")
	}
	settleGallery(t, tt, func() bool { return s.Model.Gallery.Phase == state.GalleryReady })
	tt.Frame()
	if !tt.HasText("海特洛相册") || !tt.HasText("还没有发现摄影作品") {
		t.Fatalf("empty gallery text missing; texts=%v", tt.Texts())
	}

	// 2. Failure state.
	sFail := NewShell(settled(state.Ready))
	t.Cleanup(sFail.Dispose)
	fbFail := newFakeGalleryBackend()
	fbFail.scanErr = errors.New("permission denied")
	fbFail.loadErr = errors.New("permission denied")
	sFail.GalleryBackend = fbFail
	ttFail := ui.NewTester(sFail.View, 1280, 720)
	if clickFailed(ttFail, "相册") {
		t.Fatal("nav to gallery failed")
	}
	settleGallery(t, ttFail, func() bool { return sFail.Model.Gallery.Phase == state.GalleryFailed })
	ttFail.Frame()
	if !ttFail.HasText("无法读取相册索引") {
		t.Fatal("failed gallery state must show retry card")
	}

	// 3. Multi-UID, date grouping, high-DPI, reduced motion & accessibility.
	sMulti := NewShell(settled(state.Ready))
	t.Cleanup(sMulti.Dispose)
	sMulti.WishLocation = time.FixedZone("CST", 8*3600)
	sMulti.GalleryBackend = newFakeGalleryBackend(sampleGalleryAssets()...)
	ttMulti := ui.NewTester(sMulti.View, 1280, 720)
	ttMulti.SetScale(2.0) // High DPI test
	ttMulti.SetPreferences(ui.Preferences{ReduceMotion: true, HighContrast: true})
	if clickFailed(ttMulti, "相册") {
		t.Fatal("nav to gallery failed")
	}
	settleGallery(t, ttMulti, func() bool {
		return sMulti.Model.Gallery.Phase == state.GalleryReady && len(sMulti.gallery.assets) == 4
	})
	ttMulti.Frame()
	// Default mode is the Bravais infinite gallery canvas.
	if sMulti.Model.Gallery.ViewMode != state.GalleryViewCanvas {
		t.Fatalf("default ViewMode = %v, want GalleryViewCanvas", sMulti.Model.Gallery.ViewMode)
	}
	if _, ok := ttMulti.AccessByRole(ui.RoleTab, "游戏缓存"); !ok {
		t.Fatal("missing accessible scope tab 游戏缓存")
	}
	if _, ok := ttMulti.AccessByRole(ui.RoleMenuButton, "UID 筛选"); !ok {
		t.Fatal("missing accessible UID 筛选 menu button")
	}
	if _, ok := ttMulti.AccessByRole(ui.RoleMenuButton, "排序方式"); !ok {
		t.Fatal("missing accessible 排序方式 menu button")
	}
	if _, ok := ttMulti.AccessByRole(ui.RoleButton, "相册设置"); !ok {
		t.Fatal("missing accessible 相册设置 button")
	}
	// Switch to 日期列表 and verify date headers render cleanly.
	if clickFailed(ttMulti, "日期列表") || sMulti.Model.Gallery.ViewMode != state.GalleryViewTimeline {
		t.Fatal("failed to switch to 日期列表 view")
	}
	ttMulti.Frame()
	if !ttMulti.HasText("2026年11月24日") || !ttMulti.HasText("2026年11月23日") {
		t.Fatalf("date group headers missing in timeline view; texts=%v", ttMulti.Texts())
	}
	if clickFailed(ttMulti, "无限画廊") || sMulti.Model.Gallery.ViewMode != state.GalleryViewCanvas {
		t.Fatal("failed to switch back to 无限画廊 view")
	}
}

func TestGalleryViewerShortcutsBatchSelectionAndSettings(t *testing.T) {
	s := NewShell(settled(state.Ready))
	t.Cleanup(s.Dispose)
	fb := newFakeGalleryBackend(sampleGalleryAssets()...)
	s.GalleryBackend = fb
	s.WishLocation = time.FixedZone("CST", 8*3600)

	var copiedBytes []byte
	s.WriteClipboardImage = func(b []byte) error {
		copiedBytes = b
		return nil
	}
	s.PickGalleryExportFile = func(_ context.Context, suggested string) (string, error) {
		return `C:\Export\` + suggested, nil
	}
	s.PickGalleryDirectory = func(_ context.Context, _ string) (string, error) {
		return `C:\ExportBatch`, nil
	}

	tt := ui.NewTester(s.View, 1280, 720)
	if clickFailed(tt, "相册") {
		t.Fatal("failed to open gallery")
	}
	settleGallery(t, tt, func() bool { return s.Model.Gallery.Phase == state.GalleryReady && len(s.gallery.assets) == 4 })
	tt.Frame()

	// Space opens quick preview on first asset.
	tt.Key(0, ui.KeySpace)
	tt.Frame()
	if !s.Model.Gallery.ViewerOpen || s.Model.Gallery.ViewerID != 1 {
		t.Fatalf("Space must open viewer on first asset: open=%v id=%d", s.Model.Gallery.ViewerOpen, s.Model.Gallery.ViewerID)
	}

	// Right steps to asset 2; Left returns to asset 1.
	tt.Key(0, ui.KeyRight)
	tt.Frame()
	if s.Model.Gallery.ViewerID != 2 {
		t.Fatalf("Right must step viewer to asset 2, got %d", s.Model.Gallery.ViewerID)
	}
	tt.Key(0, ui.KeyLeft)
	tt.Frame()
	if s.Model.Gallery.ViewerID != 1 {
		t.Fatalf("Left must step viewer back to asset 1, got %d", s.Model.Gallery.ViewerID)
	}

	// Zoom controls: Actual Size (1) sets fit=false, zoom=1; '+' zooms in.
	tt.Key(0, ui.Key1)
	tt.Frame()
	if s.gallery.viewer.fit || s.gallery.viewer.zoom != 1 {
		t.Fatalf("Key1 must switch to 100%% actual size: fit=%v zoom=%v", s.gallery.viewer.fit, s.gallery.viewer.zoom)
	}
	tt.Key(0, ui.KeyEqual)
	tt.Frame()
	if s.gallery.viewer.zoom <= 1 {
		t.Fatalf("zoom in failed: zoom=%v", s.gallery.viewer.zoom)
	}

	// Ctrl+C copies image to clipboard via injected handler.
	tt.Key(ui.Ctrl, ui.KeyC)
	settleGallery(t, tt, func() bool { return len(copiedBytes) > 0 })

	// Ctrl+S triggers single export.
	tt.Key(ui.Ctrl, ui.KeyS)
	settleGallery(t, tt, func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return fb.exportSingle != ""
	})

	// Esc closes viewer.
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if s.Model.Gallery.ViewerOpen {
		t.Fatal("Esc must close viewer")
	}

	// Ctrl+A selects all and allows batch export.
	tt.Key(ui.Ctrl, ui.KeyA)
	tt.Frame()
	if !s.Model.Gallery.SelectMode || s.Model.Gallery.SelectedCnt != 4 {
		t.Fatalf("Ctrl+A failed: selectMode=%v selected=%d", s.Model.Gallery.SelectMode, s.Model.Gallery.SelectedCnt)
	}
	tt.Key(ui.Ctrl, ui.KeyS)
	settleGallery(t, tt, func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return fb.exportBatch == 4
	})
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if s.Model.Gallery.SelectMode {
		t.Fatal("Esc must exit select mode")
	}

	// Centered settings dialog: verify AutoArchive defaults OFF, toggles cleanly,
	// and tabs switch cleanly across archive/sources/cache panes.
	if clickFailed(tt, "相册设置") || !s.Model.Gallery.SettingsOpen {
		t.Fatal("failed to open 相册设置")
	}
	dlg, ok := tt.AccessByRole(ui.RoleDialog, "相册设置")
	if !ok {
		t.Fatal("missing accessible 相册设置 dialog")
	}
	// Centered dialog should be roughly symmetric horizontally rather than a right-edge drawer.
	leftMargin := dlg.Bounds.X
	rightMargin := 1280 - (dlg.Bounds.X + dlg.Bounds.W)
	if leftMargin < 100 || rightMargin < 100 {
		t.Fatalf("相册设置 dialog must be centered, got bounds=%+v", dlg.Bounds)
	}
	sw, ok := tt.AccessByRole(ui.RoleSwitch, "自动归档游戏内拍照")
	if !ok {
		t.Fatal("auto-archive switch missing accessible role/label")
	}
	if sw.Checked || s.Model.Gallery.AutoArchive {
		t.Fatal("auto-archive switch must default OFF")
	}
	if clickFailed(tt, "自动归档游戏内拍照") {
		t.Fatal("failed to toggle auto-archive switch")
	}
	settleGallery(t, tt, func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return fb.settings.AutoArchive
	})
	if clickFailed(tt, "媒体来源") || !tt.HasText("自定义媒体来源") {
		t.Fatal("failed to switch to 媒体来源 tab")
	}
	if clickFailed(tt, "缓存与索引") || !tt.HasText("缩略图磁盘缓存") {
		t.Fatal("failed to switch to 缓存与索引 tab")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()
	if s.Model.Gallery.SettingsOpen {
		t.Fatal("Esc must close gallery settings")
	}
}

func TestGalleryP1FavoritesAlbumsTagsVideoViewerAndCustomBackground(t *testing.T) {
	day1 := time.Date(2026, 11, 24, 16, 52, 2, 0, time.FixedZone("CST", 8*3600))
	assets := append(sampleGalleryAssets(), GalleryAsset{
		ID: 5, SHA256: "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee",
		MediaType: GalleryMediaVideo, MIMEType: "video/mp4", ByteSize: 25 << 20,
		Width: 1920, Height: 1080, DurationMS: 130_840, VideoCodec: "H.264", AudioCodec: "AAC",
		PosterPath: `D:\Games\NTE\Selfie\100001\FFMpeg\26_11_24_16_49_20_KeyFrame.jpg`,
		ModifiedAt: day1.Add(-2 * time.Minute), DisplayTime: day1.Add(-2 * time.Minute),
		FileName:    "26_11_24_16_49_0123.mp4",
		PrimaryPath: `D:\Games\NTE\Selfie\100001\FFMpeg\26_11_24_16_49_0123.mp4`,
		SourcePath:  `D:\Games\NTE\Selfie\100001\FFMpeg\26_11_24_16_49_0123.mp4`,
		UIDs:        []string{"100001"}, HasCached: true, HasArchived: true, Available: true,
		Tags: []string{"夜景"},
	})
	assets[0].Favorited = true
	assets[0].Tags = []string{"霓虹"}

	s := NewShell(settled(state.Ready))
	t.Cleanup(s.Dispose)
	fb := newFakeGalleryBackend(assets...)
	fb.albums = []GalleryAlbum{{ID: 1, Name: "海特洛夜景", Count: 1}}
	s.GalleryBackend = fb
	s.WishLocation = time.FixedZone("CST", 8*3600)

	tt := ui.NewTester(s.View, 1280, 720)
	if clickFailed(tt, "相册") {
		t.Fatal("failed to open gallery")
	}
	settleGallery(t, tt, func() bool { return s.Model.Gallery.Phase == state.GalleryReady && len(s.gallery.assets) == 5 })
	tt.Frame()

	// Verify P1 scope tabs (收藏, 视频) and search input exist with accessible roles.
	if _, ok := tt.AccessByRole(ui.RoleTab, "收藏"); !ok {
		t.Fatal("missing accessible 收藏 scope tab")
	}
	if _, ok := tt.AccessByRole(ui.RoleTab, "视频"); !ok {
		t.Fatal("missing accessible 视频 scope tab")
	}
	if _, ok := tt.AccessByRole(ui.RoleMenuButton, "相册筛选"); !ok {
		t.Fatal("missing accessible 相册筛选 menu button")
	}
	if _, ok := tt.AccessByRole(ui.RoleMenuButton, "标签筛选"); !ok {
		t.Fatal("missing accessible 标签筛选 menu button")
	}

	// Switch to 视频 tab -> only video asset visible, open video viewer.
	if clickFailed(tt, "视频") {
		t.Fatal("failed to click 视频 tab")
	}
	settleGallery(t, tt, func() bool { return len(s.gallery.assets) == 1 && s.gallery.assets[0].IsVideo() })
	tt.Frame()
	if !tt.HasText("02:11") {
		t.Fatalf("video duration pill missing from tile; texts=%v", tt.Texts())
	}

	// Open video viewer by clicking the video tile -> displays verified codec metadata and hides photo-only actions.
	if clickFailed(tt, "26_11_24_16_49_0123.mp4") {
		t.Fatal("failed to click video tile")
	}
	tt.Frame()
	if !s.Model.Gallery.ViewerOpen || s.Model.Gallery.ViewerID != 5 {
		t.Fatalf("clicking video tile failed to open video viewer: %+v", s.Model.Gallery)
	}
	if !tt.HasText("MP4 · H.264/AAC · 02:11") {
		t.Fatalf("video codec pill missing in viewer; texts=%v", tt.Texts())
	}
	if _, ok := tt.AccessByRole(ui.RoleButton, "设为背景"); ok {
		t.Fatal("video viewer must not offer 设为背景")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()

	// Switch back to 全部媒体, open photo viewer, and set photo as custom static background.
	if clickFailed(tt, "全部媒体") {
		t.Fatal("failed to click 全部媒体")
	}
	settleGallery(t, tt, func() bool { return len(s.gallery.assets) == 5 })
	tt.Frame()
	s.Dispatch(state.Event{Kind: state.GalleryOpenViewer, Gallery: state.GalleryPayload{AssetID: 1}})
	tt.Frame()
	if clickFailed(tt, "设为背景") {
		t.Fatal("failed to click 设为背景 in photo viewer")
	}
	settleGallery(t, tt, func() bool { return s.Model.CustomBackground && s.Poster != nil })
	tt.Key(0, ui.KeyEscape)
	tt.Frame()

	// Navigate to 启动器设置 -> 外观 and restore official background.
	s.Dispatch(state.Event{Kind: state.Navigate, Page: state.LauncherSettings})
	s.selectSettingsSection(state.SettingsAppearance)
	tt.Frame()
	if !tt.HasText("已启用自定义静态背景") {
		t.Fatalf("appearance settings must show custom background status; texts=%v", tt.Texts())
	}
	if clickFailed(tt, "恢复官方背景") {
		t.Fatal("failed to click 恢复官方背景")
	}
	settleGallery(t, tt, func() bool { return !s.Model.CustomBackground })

	// Return to gallery and verify centered album & tag modals + batch delete archive confirmation.
	s.Dispatch(state.Event{Kind: state.Navigate, Page: state.Gallery})
	settleGallery(t, tt, func() bool { return len(s.gallery.assets) == 5 })
	s.Dispatch(state.Event{Kind: state.GalleryOpenAlbumModal, Gallery: state.GalleryPayload{AssetID: 1}})
	tt.Frame()
	if dlg, ok := tt.AccessByRole(ui.RoleDialog, "管理自定义相册"); !ok || dlg.Bounds.X < 100 {
		t.Fatalf("album modal must be centered dialog, ok=%v bounds=%+v", ok, dlg.Bounds)
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()

	s.Dispatch(state.Event{Kind: state.GalleryOpenTagModal, Gallery: state.GalleryPayload{AssetID: 1}})
	tt.Frame()
	if dlg, ok := tt.AccessByRole(ui.RoleDialog, "管理标签"); !ok || dlg.Bounds.X < 100 {
		t.Fatalf("tag modal must be centered dialog, ok=%v bounds=%+v", ok, dlg.Bounds)
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()

	// Multi-select all -> batch delete archive requires confirmation -> confirm deletes only archive copies.
	tt.Key(ui.Ctrl, ui.KeyA)
	tt.Frame()
	if clickFailed(tt, "删除归档") {
		t.Fatal("failed to open batch delete archive confirmation")
	}
	tt.Frame()
	if _, ok := tt.AccessByRole(ui.RoleDialog, "批量删除归档副本"); !ok {
		t.Fatal("missing centered batch delete archive confirmation dialog")
	}
	if clickFailed(tt, "删除归档副本") {
		t.Fatal("failed to confirm batch delete archive")
	}
	settleGallery(t, tt, func() bool {
		fb.mu.Lock()
		defer fb.mu.Unlock()
		return fb.deletedArchBatch == 2
	})
}

func TestGalleryInfiniteCanvasPanZoomAndClick(t *testing.T) {
	// Build 12 works so the Bravais lattice extends beyond the 1280x720 viewport in 2D.
	day := time.Date(2026, 11, 24, 16, 0, 0, 0, time.FixedZone("CST", 8*3600))
	assets := make([]GalleryAsset, 12)
	for i := range assets {
		ts := day.Add(-time.Duration(i) * time.Minute)
		assets[i] = GalleryAsset{
			ID: int64(i + 1), SHA256: fmt.Sprintf("%064x", i+1),
			MIMEType: "image/png", ByteSize: 1_800_000, Width: 1920, Height: 1080,
			ModifiedAt: ts, DisplayTime: ts, FileName: fmt.Sprintf("26_11_24_16_%02d_00.png", 50-i),
			PrimaryPath: fmt.Sprintf(`D:\Games\NTE\Selfie\100001\ScreenShots\26_11_24_16_%02d_00.png`, 50-i),
			UIDs:        []string{"100001"}, HasCached: true, HasArchived: i%2 == 0, Available: true,
		}
	}

	s := NewShell(settled(state.Ready))
	t.Cleanup(s.Dispose)
	s.GalleryBackend = newFakeGalleryBackend(assets...)
	s.WishLocation = time.FixedZone("CST", 8*3600)

	tt := ui.NewTester(s.View, 1280, 720)
	if clickFailed(tt, "相册") {
		t.Fatal("failed to open gallery")
	}
	settleGallery(t, tt, func() bool { return s.Model.Gallery.Phase == state.GalleryReady && len(s.gallery.assets) == 12 })
	tt.Frame()

	if s.Model.Gallery.ViewMode != state.GalleryViewCanvas {
		t.Fatalf("ViewMode = %v, want GalleryViewCanvas", s.Model.Gallery.ViewMode)
	}
	initX, initY, initZoom := s.gallery.canvas.camX, s.gallery.canvas.camY, s.gallery.canvas.zoom
	if initZoom != 1 {
		t.Fatalf("initial canvas zoom = %v, want 1", initZoom)
	}

	// Left-drag pans the Bravais gallery canvas in 2D without opening the viewer.
	tt.Move(640, 360)
	tt.Press(640, 360)
	tt.Advance(20 * time.Millisecond)
	tt.Move(520, 290)
	tt.Advance(20 * time.Millisecond)
	tt.Release(520, 290)
	tt.Frame()
	if s.Model.Gallery.ViewerOpen {
		t.Fatal("dragging canvas must not open viewer")
	}
	if s.gallery.canvas.camX == initX && s.gallery.canvas.camY == initY {
		t.Fatalf("drag did not move camera: before=(%v,%v) after=(%v,%v)", initX, initY, s.gallery.canvas.camX, s.gallery.canvas.camY)
	}

	// Wheel scrolls the canvas camera smoothly back up from the bottom edge.
	beforeWheelY := s.gallery.canvas.camY
	tt.Wheel(640, 360, 0, -60)
	tt.Frame()
	if s.gallery.canvas.camY >= beforeWheelY {
		t.Fatalf("wheel scroll up did not move canvas camera: before=%v after=%v", beforeWheelY, s.gallery.canvas.camY)
	}

	// Pinch zoom and HUD zoom buttons adjust scale smoothly.
	tt.Pinch(640, 360, 1.2)
	tt.Frame()
	if s.gallery.canvas.zoom <= 1.05 {
		t.Fatalf("pinch zoom did not increase scale: %v", s.gallery.canvas.zoom)
	}
	if clickFailed(tt, "放大画廊") {
		t.Fatal("failed to click 放大画廊")
	}
	tt.Frame()
	if s.gallery.canvas.zoom <= 1.25 {
		t.Fatalf("HUD zoom-in did not increase scale further: %v", s.gallery.canvas.zoom)
	}

	// Reset via recenter button restores 100% zoom.
	if clickFailed(tt, "重置画廊视角") {
		t.Fatal("failed to click 重置画廊视角")
	}
	tt.Frame()
	if s.gallery.canvas.zoom != 1 {
		t.Fatalf("recenter must restore 100%% zoom, got %v", s.gallery.canvas.zoom)
	}

	// Clicking a visible tile without dragging opens the viewer on that exact asset.
	firstLabel := galleryAssetAccessibleLabel(assets[0])
	if clickFailed(tt, firstLabel) {
		t.Fatalf("failed to click first canvas tile (%s)", firstLabel)
	}
	tt.Frame()
	if !s.Model.Gallery.ViewerOpen || s.Model.Gallery.ViewerID != assets[0].ID {
		t.Fatalf("clicking canvas tile failed to open viewer: open=%v id=%d", s.Model.Gallery.ViewerOpen, s.Model.Gallery.ViewerID)
	}
}

func TestGalleryBitmapLRUEnforcesByteBudget(t *testing.T) {
	c := galleryImageCache{budgetBytes: 100 * 100 * 4 * 2} // room for 2 bitmaps of 100x100 RGBA
	makeBmp := func() *ui.Bitmap {
		return ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 100, 100)))
	}
	c.remember(galleryThumbKey{assetID: 1, edge: 100}, makeBmp())
	c.remember(galleryThumbKey{assetID: 2, edge: 100}, makeBmp())
	c.remember(galleryThumbKey{assetID: 3, edge: 100}, makeBmp())

	if len(c.cache) != 2 {
		t.Fatalf("LRU cache len = %d, want 2", len(c.cache))
	}
	if _, evicted := c.cache[galleryThumbKey{assetID: 1, edge: 100}]; evicted {
		t.Fatal("oldest bitmap (id=1) should have been evicted")
	}
	if c.cachedBytes > c.budget() {
		t.Fatalf("cachedBytes %d exceeded budget %d", c.cachedBytes, c.budget())
	}
}

func BenchmarkGalleryGridScroll10000(b *testing.B) {
	assets := make([]GalleryAsset, 10_000)
	base := time.Date(2026, 11, 24, 17, 0, 0, 0, time.UTC)
	for i := range assets {
		ts := base.Add(-time.Duration(i) * time.Minute)
		assets[i] = GalleryAsset{
			ID: int64(i + 1), SHA256: fmt.Sprintf("%064x", i+1),
			MIMEType: "image/png", ByteSize: 1_800_000, Width: 1280, Height: 720,
			ModifiedAt: ts, DisplayTime: ts, FileName: fmt.Sprintf("26_11_24_%05d.png", i),
			HasCached: true, Available: true,
		}
	}
	s := NewShell(settled(state.Ready))
	b.Cleanup(s.Dispose)
	s.Model.Page = state.Gallery
	s.Model.Gallery.Initialized = true
	s.Model.Gallery.Phase = state.GalleryReady
	s.Model.Gallery.TotalCount = len(assets)
	s.Model.Gallery.FilteredCount = len(assets)
	s.gallery.assets = assets
	s.gallery.rowsRev++

	tt := ui.NewTester(s.View, 1280, 720)
	tt.Frame()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		tt.Wheel(640, 400, 0, 240)
		tt.Frame()
	}
}
