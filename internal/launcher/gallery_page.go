// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"fmt"
	"math"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func (s *Shell) galleryPage(c *ui.Context) {
	g := s.Model.Gallery
	tone := s.tone()
	prefs := c.Preferences()
	opaque := prefs.HighContrast || prefs.ReduceTransparency
	width, height := c.Size()
	surfaceW := max(width-railWidth-bezelWidth, 480)
	surfaceH := max(height-2*bezelWidth, 360)
	available := max(surfaceW-2*galleryInset, 420)
	isCanvas := g.ViewMode == state.GalleryViewCanvas

	// Handle page-level keyboard shortcuts when no modal is open and the search
	// field does not hold focus (so Space / Ctrl+A / Ctrl+C work normally inside search).
	// Evaluate every Shortcut call unconditionally so registrations stay active.
	if !g.ModalOpen() && s.Model.Overlay == state.NoOverlay && !s.gallery.searchFocused {
		selectAll := c.Shortcut(ui.Ctrl, ui.KeyA) && !c.ShortcutRepeated()
		copyImg := c.Shortcut(ui.Ctrl, ui.KeyC) && !c.ShortcutRepeated()
		exportKey := c.Shortcut(ui.Ctrl, ui.KeyS) && !c.ShortcutRepeated()
		previewKey := c.Shortcut(0, ui.KeySpace) && !c.ShortcutRepeated()
		resetCanvas := c.Shortcut(0, ui.Key0) && !c.ShortcutRepeated() && isCanvas
		escSelect := c.Shortcut(0, ui.KeyEscape) && !c.ShortcutRepeated() && g.SelectMode
		switch {
		case selectAll:
			s.Dispatch(state.Event{Kind: state.GallerySelectAll})
		case copyImg:
			if id := s.galleryShortcutTargetID(); id > 0 {
				if a, ok := s.galleryAssetByID(id); ok && !a.IsVideo() {
					s.Dispatch(state.Event{Kind: state.GalleryCopyImage, Gallery: state.GalleryPayload{AssetID: id}})
				}
			}
		case exportKey:
			if g.SelectMode && g.SelectedCnt > 0 {
				s.Dispatch(state.Event{Kind: state.GalleryExportSelected})
			} else if id := s.galleryShortcutTargetID(); id > 0 {
				s.Dispatch(state.Event{Kind: state.GalleryExportSingle, Gallery: state.GalleryPayload{AssetID: id}})
			}
		case previewKey:
			if id := s.galleryShortcutTargetID(); id > 0 {
				s.gallery.viewer.reset(id)
				s.Dispatch(state.Event{Kind: state.GalleryOpenViewer, Gallery: state.GalleryPayload{AssetID: id}})
			}
		case resetCanvas:
			q := s.gallery.canvas.ensureQuilt(s.gallery.assets, s.gallery.rowsRev)
			s.gallery.canvas.recenter(q, surfaceW, surfaceH)
		case escSelect:
			s.Dispatch(state.Event{Kind: state.Dismiss})
		}
	}

	surface := canvas(c, "gallery-surface").Draw(func(p *ui.Painter, _ ui.Rect) {
		if sc := p.Scale(); sc > 0 {
			s.gallery.dpiScale = sc
		}
	})
	if opaque {
		surface.Background(s.theme.Surface)
	} else {
		surface.Material(frostedSurface{Tint: ui.RGBA(10, 11, 14, .64)})
	}
	surface.Children(func() {
		if !opaque {
			ui.Box(c).Absolute().Right(wishBloomRight).Bottom(wishBloomBottom).Size(wishBloomSize, wishBloomSize).PassThrough().RadialGradient(ui.RadialGradient{
				Radius: wishBloomSize / 2,
				Stops:  []ui.GradientStop{{Color: tone.Alpha(wishHighlightOpacity), At: 0}, {Color: ui.Transparent, At: 1}},
			})
		}

		gridTop := galleryGridTop
		if g.Notice != "" {
			gridTop = galleryGridTopNotice
		}
		gridBottom := galleryBottomInset + galleryStatusBarHeight + 8

		// Main body: painted BEFORE floating chrome in infinite canvas mode so
		// works glide edge-to-edge underneath the soft top veil.
		switch {
		case g.Phase == state.GalleryFailed && len(s.gallery.assets) == 0:
			s.galleryEmptyCard(c, gridTop, "无法读取相册索引", g.Error, true)
		case len(s.gallery.assets) == 0:
			title, body := "还没有发现摄影作品", "在《异环》中拍照或录屏，或保存到系统图片/视频目录后，海特洛相册会自动发现并整理你的作品。也可以手动添加自定义文件夹。"
			if g.Phase == state.GalleryScanning {
				title, body = "正在扫描媒体目录", "正在读取游戏拍照、录像缓存与系统导出目录的元数据…"
			} else if g.TotalCount > 0 {
				title, body = "当前筛选条件下没有媒体", "清空搜索词，或切换分类、相册、标签与 UID 筛选器可查看全部已索引作品。"
			}
			s.galleryEmptyCard(c, gridTop, title, body, false)
		case isCanvas:
			s.galleryInfiniteCanvas(c, surfaceW, surfaceH, opaque, tone)
			if !opaque {
				// Soft top gradient veil keeps floating header crisp over any bright screenshot.
				ui.Box(c).Absolute().Left(0).Right(0).Top(0).Height(168).PassThrough().LinearGradient(ui.LinearGradient{
					Angle: 180,
					Stops: []ui.GradientStop{
						{Color: ui.RGBA(10, 11, 14, .90), At: 0},
						{Color: ui.RGBA(10, 11, 14, .64), At: .66},
						{Color: ui.Transparent, At: 1},
					},
				})
				ui.Box(c).Absolute().Left(0).Right(0).Bottom(0).Height(88).PassThrough().LinearGradient(ui.LinearGradient{
					Angle: 0,
					Stops: []ui.GradientStop{
						{Color: ui.RGBA(10, 11, 14, .78), At: 0},
						{Color: ui.Transparent, At: 1},
					},
				})
			}
		default:
			s.galleryVirtualGrid(c, available, gridTop, gridBottom, opaque, tone)
		}

		// Floating top header + scope tabs.
		ui.Column(c.Key("gallery-header")).Absolute().Left(galleryInset).Right(galleryInset).Top(galleryHeaderTop).Gap(10).Children(func() {
			s.galleryToolbar(c, g, tone, opaque)
			s.galleryScopeBar(c, g, tone)
			if g.Notice != "" {
				color := muted
				if g.NoticeWarn {
					color = s.wishWarningColor()
				}
				ui.Text(c, g.Notice).FontSize(12).TextColor(color).SingleLine().Tooltip(g.Notice)
			}
		})

		// Bottom floating corner capsules (status on left, canvas zoom/reset on right).
		s.galleryStatusBar(c, g, tone, surfaceW, surfaceH, isCanvas && len(s.gallery.assets) > 0)
	})

	if s.galleryImagesEnabled() && !s.gallery.images.active && len(s.gallery.images.wanted) > 0 {
		c.Invalidate()
	}

	// Centered gallery modals (viewer, organization dialogs, settings, delete confirmations).
	s.galleryViewerOverlay(c)
	s.galleryAlbumModalOverlay(c)
	s.galleryTagModalOverlay(c)
	s.gallerySettingsOverlay(c)
	s.galleryDeleteArchiveConfirm(c)
	s.galleryBatchDeleteArchiveConfirm(c)
}

func (s *Shell) galleryShortcutTargetID() int64 {
	if s.Model.Gallery.ViewMode == state.GalleryViewCanvas && s.gallery.canvas.hoveredID > 0 {
		if _, ok := s.galleryAssetByID(s.gallery.canvas.hoveredID); ok {
			return s.gallery.canvas.hoveredID
		}
	}
	if s.Model.Gallery.FocusedID > 0 {
		if _, ok := s.galleryAssetByID(s.Model.Gallery.FocusedID); ok {
			return s.Model.Gallery.FocusedID
		}
	}
	if len(s.gallery.selected) > 0 {
		for _, a := range s.gallery.assets {
			if s.gallery.selected[a.ID] {
				return a.ID
			}
		}
	}
	if len(s.gallery.assets) > 0 {
		return s.gallery.assets[0].ID
	}
	return 0
}

func (s *Shell) gallerySmallButton(c *ui.Context, key, label string, disabled bool) ui.Element {
	b := s.quietButton(c, key, label, disabled).Height(galleryControlHeight).Radius(10).Padding(0, 10)
	if prefs := c.Preferences(); prefs.HighContrast || prefs.ReduceTransparency {
		b.Background(s.theme.Surface).Border(1, muted)
	}
	return b
}

func (s *Shell) galleryToolbar(c *ui.Context, g state.GalleryState, tone ui.Color, opaque bool) {
	ui.Row(c.Key("gallery-toolbar")).FillWidth().Height(galleryToolbarHeight).AlignItems(ui.Center).Gap(6).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(10).Grow(1).MinWidth(0).Children(func() {
			ui.Text(c, "海特洛相册").FontSize(24).FontWeight(700).SingleLine()
			countText := fmt.Sprintf("%d 项", g.TotalCount)
			if g.FilteredCount != g.TotalCount {
				countText = fmt.Sprintf("%d / %d", g.FilteredCount, g.TotalCount)
			}
			mono(c, countText, 12).TextColor(muted)
		})

		if g.SelectMode {
			s.gallery.searchFocused = false
			selAssets := s.gallerySelectedAssets()
			allFav := len(selAssets) > 0
			hasArch := false
			hasUnarch := false
			for _, a := range selAssets {
				if !a.Favorited {
					allFav = false
				}
				if a.HasArchived {
					hasArch = true
				} else if a.Available && !a.Invalid {
					hasUnarch = true
				}
			}

			ui.Text(c, fmt.Sprintf("已选 %d", g.SelectedCnt)).FontSize(13).FontWeight(600).TextColor(tone).SingleLine()
			s.gallerySmallButton(c, "gallery-select-all", "全选", g.FilteredCount == 0).OnClick(func() {
				s.Dispatch(state.Event{Kind: state.GallerySelectAll})
			})
			favLabel := "收藏"
			if allFav {
				favLabel = "取消收藏"
			}
			s.gallerySmallButton(c, "gallery-batch-fav", favLabel, g.Busy || g.SelectedCnt == 0).OnClick(func() {
				s.Dispatch(state.Event{Kind: state.GalleryBatchFavorite, Enabled: !allFav})
			})
			s.gallerySmallButton(c, "gallery-batch-album", "加入相册 ▾", g.Busy || g.SelectedCnt == 0).Label("批量加入相册").Menu(func(m *ui.Menu) {
				for _, al := range s.gallery.albums {
					al := al
					if m.Item(fmt.Sprintf("%s (%d)", al.Name, al.Count)).Chosen() {
						s.Dispatch(state.Event{Kind: state.GalleryAddToAlbum, Gallery: state.GalleryPayload{AlbumID: al.ID}})
					}
				}
				if len(s.gallery.albums) > 0 {
					m.Separator()
				}
				if m.Item("新建或管理相册…").Chosen() {
					s.Dispatch(state.Event{Kind: state.GalleryOpenAlbumModal})
				}
			})
			s.gallerySmallButton(c, "gallery-batch-tag", "标签", g.Busy || g.SelectedCnt == 0).OnClick(func() {
				s.Dispatch(state.Event{Kind: state.GalleryOpenTagModal})
			})
			s.gallerySmallButton(c, "gallery-batch-archive", "归档", g.Busy || !hasUnarch).OnClick(func() {
				s.Dispatch(state.Event{Kind: state.GalleryArchiveSelected})
			})
			s.accentButton(c, "gallery-batch-export", fmt.Sprintf("导出 (%d)", g.SelectedCnt)).
				Height(galleryControlHeight).Radius(10).Padding(0, 12).Disabled(g.Busy || g.SelectedCnt == 0).
				OnClick(func() { s.Dispatch(state.Event{Kind: state.GalleryExportSelected}) })
			s.gallerySmallButton(c, "gallery-batch-del-archive", "删除归档", g.Busy || !hasArch).OnClick(func() {
				s.Dispatch(state.Event{Kind: state.GalleryRequestBatchDeleteArchive})
			})
			s.gallerySmallButton(c, "gallery-exit-select", "取消多选", false).OnClick(func() {
				s.Dispatch(state.Event{Kind: state.GalleryToggleSelectMode})
			})
			return
		}

		// Recessed translucent search field.
		searchBg := ui.RGBA(12, 14, 19, .58)
		if opaque {
			searchBg = s.theme.Surface
		}
		searchBorder := hairline
		if s.gallery.searchFocused {
			searchBorder = tone.Alpha(.65)
		}
		ui.Row(c.Key("gallery-search-box")).Width(gallerySearchWidth).Height(galleryControlHeight).
			Padding(0, 8).Radius(10).AlignItems(ui.Center).Gap(5).
			Background(searchBg).Border(1, searchBorder).Children(func() {
			ui.Box(c).TextColor(muted).Children(func() { glyph(c, "\uE721", 13) })
			inp := ui.TextInput(c.Key("gallery-search-input"), &s.gallery.searchInput).
				Label("搜索相册").Placeholder("搜索文件、标签、相册…").
				Grow(1).MinWidth(0).Height(galleryControlHeight-4).Padding(0, 2).FontSize(12).
				Background(ui.Transparent).Border(0, ui.Transparent).TextColor(paper).
				OnChange(func() {
					s.Dispatch(state.Event{Kind: state.GallerySetSearch, Gallery: state.GalleryPayload{Search: s.gallery.searchInput}})
				}).
				OnSubmit(func() {
					s.Dispatch(state.Event{Kind: state.GallerySetSearch, Gallery: state.GalleryPayload{Search: s.gallery.searchInput}})
				})
			s.gallery.searchFocused = inp.Focused()
			if s.gallery.searchInput != "" {
				clr := ui.ButtonBase(c.Key("gallery-search-clear")).Label("清除搜索").
					Size(20, 20).Radius(10).Justify(ui.Center).TextColor(muted)
				if clr.Hovered() {
					clr.Background(ui.RGBA(255, 255, 255, .10)).TextColor(paper)
				}
				clr.Children(func() { glyph(c, "\uE8BB", 10) }).OnClick(func() {
					s.gallery.searchInput = ""
					s.Dispatch(state.Event{Kind: state.GallerySetSearch, Gallery: state.GalleryPayload{Search: ""}})
				})
			}
		})

		// Album filter / management menu.
		albumLabel := "相册 ▾"
		if g.AlbumID > 0 {
			if name := s.galleryAlbumName(g.AlbumID); name != "" {
				albumLabel = truncateRunes(name, 5) + " ▾"
			}
		}
		s.gallerySmallButton(c, "gallery-album-filter", albumLabel, false).Label("相册筛选").Menu(func(m *ui.Menu) {
			if m.Item("全部相册").Radio(g.AlbumID == 0).Chosen() {
				s.Dispatch(state.Event{Kind: state.GallerySetAlbum, Gallery: state.GalleryPayload{AlbumID: 0}})
			}
			for _, al := range s.gallery.albums {
				al := al
				if m.Item(fmt.Sprintf("%s (%d)", al.Name, al.Count)).Radio(g.AlbumID == al.ID).Chosen() {
					s.Dispatch(state.Event{Kind: state.GallerySetAlbum, Gallery: state.GalleryPayload{AlbumID: al.ID}})
				}
			}
			m.Separator()
			if m.Item("管理 / 新建相册…").Chosen() {
				s.Dispatch(state.Event{Kind: state.GalleryOpenAlbumModal})
			}
		})

		// Tag filter menu.
		tagLabel := "标签 ▾"
		if g.Tag != "" {
			tagLabel = "#" + truncateRunes(g.Tag, 4) + " ▾"
		}
		s.gallerySmallButton(c, "gallery-tag-filter", tagLabel, false).Label("标签筛选").Menu(func(m *ui.Menu) {
			if m.Item("全部标签").Radio(g.Tag == "").Chosen() {
				s.Dispatch(state.Event{Kind: state.GallerySetTag, Gallery: state.GalleryPayload{Tag: ""}})
			}
			if len(s.gallery.tags) == 0 {
				m.Item("暂无已创建标签").Disabled(true)
			}
			for _, t := range s.gallery.tags {
				t := t
				if m.Item(fmt.Sprintf("#%s (%d)", t.Name, t.Count)).Radio(strings.EqualFold(g.Tag, t.Name)).Chosen() {
					s.Dispatch(state.Event{Kind: state.GallerySetTag, Gallery: state.GalleryPayload{Tag: t.Name}})
				}
			}
		})

		// UID filter menu.
		uidLabel := "UID ▾"
		switch g.UID {
		case "":
		case state.GalleryUIDUnknown:
			uidLabel = "未知 ▾"
		default:
			uidLabel = shortUID(g.UID) + " ▾"
		}
		s.gallerySmallButton(c, "gallery-uid-filter", uidLabel, false).Label("UID 筛选").Menu(func(m *ui.Menu) {
			if m.Item("全部 UID").Radio(g.UID == "").Chosen() {
				s.Dispatch(state.Event{Kind: state.GallerySetUID, Gallery: state.GalleryPayload{UID: ""}})
			}
			for _, u := range s.gallery.uids {
				u := u
				if m.Item("UID " + u).Radio(g.UID == u).Chosen() {
					s.Dispatch(state.Event{Kind: state.GallerySetUID, Gallery: state.GalleryPayload{UID: u}})
				}
			}
			if s.gallery.hasUnknownUID {
				if m.Item("未知归属（已保存 / 自定义）").Radio(g.UID == state.GalleryUIDUnknown).Chosen() {
					s.Dispatch(state.Event{Kind: state.GallerySetUID, Gallery: state.GalleryPayload{UID: state.GalleryUIDUnknown}})
				}
			}
		})

		// Sort menu.
		sortLabel := "最新 ▾"
		switch g.Sort {
		case state.GallerySortOldest:
			sortLabel = "最早 ▾"
		case state.GallerySortLargest:
			sortLabel = "最大 ▾"
		}
		s.gallerySmallButton(c, "gallery-sort", sortLabel, false).Label("排序方式").Menu(func(m *ui.Menu) {
			if m.Item("按时间从新到旧").Radio(g.Sort == state.GallerySortNewest).Chosen() {
				s.Dispatch(state.Event{Kind: state.GallerySetSort, Gallery: state.GalleryPayload{Sort: state.GallerySortNewest}})
			}
			if m.Item("按时间从旧到新").Radio(g.Sort == state.GallerySortOldest).Chosen() {
				s.Dispatch(state.Event{Kind: state.GallerySetSort, Gallery: state.GalleryPayload{Sort: state.GallerySortOldest}})
			}
			if m.Item("按文件大小降序").Radio(g.Sort == state.GallerySortLargest).Chosen() {
				s.Dispatch(state.Event{Kind: state.GallerySetSort, Gallery: state.GalleryPayload{Sort: state.GallerySortLargest}})
			}
		})

		// View mode toggle: Bravais infinite gallery wall <-> Date-grouped timeline list.
		toggleLabel := "日期列表"
		toggleIcon := "\uE8FD"
		nextMode := state.GalleryViewTimeline
		if g.ViewMode == state.GalleryViewTimeline {
			toggleLabel = "无限画廊"
			toggleIcon = "\uE80A"
			nextMode = state.GalleryViewCanvas
		}
		viewBtn := ui.ButtonBase(c.Key("gallery-view-mode")).Label(toggleLabel).Tooltip("切换到"+toggleLabel).
			Height(galleryControlHeight).Padding(0, 10).Radius(10).Gap(5).Justify(ui.Center).
			Border(1, hairline).Background(ui.RGBA(255, 255, 255, .05)).TextColor(paper)
		if viewBtn.Hovered() || viewBtn.FocusVisible() {
			viewBtn.Background(ui.RGBA(255, 255, 255, .10)).Border(1, tone.Alpha(.55))
		}
		viewBtn.Children(func() {
			glyph(c, toggleIcon, 13)
			ui.Text(c, toggleLabel).FontSize(12).FontWeight(500).SingleLine()
		}).OnClick(func() {
			s.Dispatch(state.Event{Kind: state.GallerySetViewMode, Gallery: state.GalleryPayload{ViewMode: nextMode}})
		})

		refreshLabel := "刷新"
		if g.Phase == state.GalleryScanning {
			refreshLabel = "扫描中"
		}
		s.gallerySmallButton(c, "gallery-refresh", refreshLabel, g.Phase == state.GalleryScanning || g.Busy).OnClick(func() {
			s.Dispatch(state.Event{Kind: state.GalleryRefresh})
		})

		s.gallerySmallButton(c, "gallery-multi-select", "多选", len(s.gallery.assets) == 0).OnClick(func() {
			s.Dispatch(state.Event{Kind: state.GalleryToggleSelectMode})
		})

		s.gallerySmallButton(c, "gallery-settings-btn", "设置", false).Label("相册设置").Tooltip("相册设置").OnClick(func() {
			s.Dispatch(state.Event{Kind: state.GalleryOpenSettings})
		})
	})
}

func (s *Shell) galleryScopeBar(c *ui.Context, g state.GalleryState, tone ui.Color) {
	tabs := []struct {
		key   string
		label string
		count int
		scope state.GalleryScope
	}{
		{"gallery-tab-all", "全部媒体", g.TotalCount, state.GalleryScopeAll},
		{"gallery-tab-fav", "收藏", g.FavoriteCount, state.GalleryScopeFavorites},
		{"gallery-tab-videos", "视频", g.VideoCount, state.GalleryScopeVideos},
		{"gallery-tab-cached", "游戏缓存", g.CachedCount, state.GalleryScopeCached},
		{"gallery-tab-exported", "已保存", g.ExportedCount, state.GalleryScopeExported},
		{"gallery-tab-archived", "已归档", g.ArchivedCount, state.GalleryScopeArchived},
	}
	ui.Row(c.Key("gallery-scope-tabs")).FillWidth().Height(galleryTabsHeight).AlignItems(ui.Center).Gap(4).
		Role(ui.RoleTabList).Label("相册分类筛选").Children(func() {
		for _, tab := range tabs {
			tab := tab
			selected := g.Scope == tab.scope
			title := fmt.Sprintf("%s %d", tab.label, tab.count)
			b := ui.ButtonBase(c.Key(tab.key)).Label(tab.label).Role(ui.RoleTab).Selected(selected).
				Height(galleryTabsHeight).Padding(0, 10).Radius(8).Justify(ui.Center).
				Background(ui.Transparent).TextColor(muted)
			if selected {
				b.TextColor(paper)
			} else if b.Hovered() || b.FocusVisible() {
				b.TextColor(paper).Background(ui.RGBA(255, 255, 255, .06))
			}
			b.Children(func() {
				ui.Text(c, title).FontSize(13).FontWeight(600).SingleLine()
				if selected {
					ui.Box(c).Absolute().Left(10).Right(10).Bottom(2).Height(2).Radius(1).Background(tone)
				}
			}).OnClick(func() {
				s.Dispatch(state.Event{Kind: state.GallerySetScope, Gallery: state.GalleryPayload{Scope: tab.scope}})
			})
		}

		ui.Box(c).Grow(1)

		// Active removable filter chips for album, tag or UID.
		hasChip := false
		chip := func(key, text string, onClear func()) {
			hasChip = true
			b := ui.ButtonBase(c.Key(key)).Label("清除筛选 "+text).
				Height(24).Padding(0, 8).Radius(12).Gap(4).Justify(ui.Center).
				Background(tone.Alpha(.16)).TextColor(paper)
			b.Children(func() {
				ui.Text(c, text).FontSize(11).FontWeight(600).SingleLine()
				ui.Text(c, "×").FontSize(12).TextColor(tone)
			}).OnClick(onClear)
		}
		if g.AlbumID > 0 {
			if name := s.galleryAlbumName(g.AlbumID); name != "" {
				chip("gallery-chip-album", "相册:"+truncateRunes(name, 8), func() {
					s.Dispatch(state.Event{Kind: state.GallerySetAlbum, Gallery: state.GalleryPayload{AlbumID: 0}})
				})
			}
		}
		if g.Tag != "" {
			chip("gallery-chip-tag", "#"+truncateRunes(g.Tag, 8), func() {
				s.Dispatch(state.Event{Kind: state.GallerySetTag, Gallery: state.GalleryPayload{Tag: ""}})
			})
		}
		if g.UID != "" {
			uText := "UID:" + shortUID(g.UID)
			if g.UID == state.GalleryUIDUnknown {
				uText = "未知UID"
			}
			chip("gallery-chip-uid", uText, func() {
				s.Dispatch(state.Event{Kind: state.GallerySetUID, Gallery: state.GalleryPayload{UID: ""}})
			})
		}
		if !hasChip {
			if g.AutoArchive {
				ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
					ui.Box(c).Size(6, 6).Radius(3).Background(tone)
					ui.Text(c, "自动归档已开启").FontSize(12).TextColor(muted).SingleLine()
				})
			} else {
				ui.Text(c, "自动归档未开启").FontSize(12).TextColor(muted).SingleLine()
			}
		}
	})
}

func (s *Shell) galleryEmptyCard(c *ui.Context, top float32, title, body string, failed bool) {
	if failed && body == "" {
		body = "读取相册数据库或目录时发生错误，请检查目录权限后重试。"
	}
	g := s.Model.Gallery
	hasFilters := g.Scope != state.GalleryScopeAll || g.AlbumID > 0 || g.Tag != "" || g.UID != "" || g.Search != ""

	ui.Column(c.Key("gallery-empty")).Absolute().Left(galleryInset).Right(galleryInset).Top(top).
		Padding(wishEmptyPadding).Gap(16).Radius(wishCardRadius).Border(1, hairline).
		Material(frostedSurface{Tint: ui.RGBA(12, 14, 18, .28)}).Children(func() {
		ui.Text(c, title).FontSize(22).FontWeight(600)
		ui.Text(c, body).FontSize(14).TextColor(muted).LineHeight(1.6)
		ui.Row(c).Gap(10).Children(func() {
			if failed {
				s.accentButton(c, "gallery-empty-retry", "重试扫描").OnClick(func() {
					s.Dispatch(state.Event{Kind: state.GalleryRefresh})
				})
			} else if g.TotalCount > 0 && hasFilters {
				s.accentButton(c, "gallery-empty-reset-filters", "清空筛选条件").OnClick(func() {
					s.gallery.searchInput = ""
					s.Dispatch(state.Event{Kind: state.GallerySetScope, Gallery: state.GalleryPayload{Scope: state.GalleryScopeAll}})
					s.Dispatch(state.Event{Kind: state.GallerySetAlbum, Gallery: state.GalleryPayload{AlbumID: 0}})
					s.Dispatch(state.Event{Kind: state.GallerySetTag, Gallery: state.GalleryPayload{Tag: ""}})
					s.Dispatch(state.Event{Kind: state.GallerySetUID, Gallery: state.GalleryPayload{UID: ""}})
					s.Dispatch(state.Event{Kind: state.GallerySetSearch, Gallery: state.GalleryPayload{Search: ""}})
				})
			} else if g.Phase != state.GalleryScanning {
				s.accentButton(c, "gallery-empty-scan", "立即扫描").OnClick(func() {
					s.Dispatch(state.Event{Kind: state.GalleryRefresh})
				})
				if s.PickGalleryDirectory != nil {
					s.quietButton(c, "gallery-empty-add", "添加自定义文件夹", g.Busy).OnClick(func() {
						s.Dispatch(state.Event{Kind: state.GalleryAddCustomSource})
					})
				}
				s.quietButton(c, "gallery-empty-settings", "相册设置", false).OnClick(func() {
					s.Dispatch(state.Event{Kind: state.GalleryOpenSettings})
				})
			}
		})
	})
}

func (s *Shell) galleryVirtualGrid(c *ui.Context, available, top, bottom float32, opaque bool, tone ui.Color) {
	cols := int((available + galleryTileGap) / (galleryTileMinWidth + galleryTileGap))
	if cols < 2 {
		cols = 2
	}
	if cols > 6 {
		cols = 6
	}
	tileW := (available - float32(cols-1)*galleryTileGap) / float32(cols)
	imgH := tileW * 9 / 16
	tileH := imgH + galleryTileFooterHeight
	thumbEdge := galleryThumbEdge(s.gallery.dpiScale)

	rows := s.galleryDateRows(cols, s.WishLocation)
	ls := &s.gallery.listState
	ls.Header = func(rowIdx int) bool {
		return rowIdx >= 0 && rowIdx < len(rows) && rows[rowIdx].kind == galleryRowHeader
	}
	ls.Key = func(rowIdx int) any {
		if rowIdx < 0 || rowIdx >= len(rows) {
			return rowIdx
		}
		r := rows[rowIdx]
		if r.kind == galleryRowHeader {
			return fmt.Sprintf("date:%d:%s", rowIdx, r.date)
		}
		if len(r.items) > 0 && r.items[0] < len(s.gallery.assets) {
			return fmt.Sprintf("row:%d:%d", cols, s.gallery.assets[r.items[0]].ID)
		}
		return rowIdx
	}

	ui.List(c.Key("gallery-grid"), ls, len(rows), func(rowIdx int) {
		if rowIdx < 0 || rowIdx >= len(rows) {
			return
		}
		row := rows[rowIdx]
		if row.kind == galleryRowHeader {
			hdr := ui.Row(c.Key(fmt.Sprintf("gallery-date-%d-%s", rowIdx, row.date))).FillWidth().Height(galleryDateHeaderHeight).
				Padding(0, 4).AlignItems(ui.Center).Gap(8)
			if opaque {
				hdr.Background(s.theme.Surface)
			} else {
				hdr.Background(ui.RGBA(14, 15, 20, .86))
			}
			hdr.Children(func() {
				ui.Text(c, row.date).FontSize(14).FontWeight(600).TextColor(paper).SingleLine()
				mono(c, fmt.Sprintf("%d 项", row.count), 12).TextColor(muted)
				ui.Box(c).Grow(1).Height(1).Background(hairline)
			})
			return
		}
		ui.Row(c).FillWidth().Height(tileH+galleryTileGap).Padding(0, 0, galleryTileGap, 0).Gap(galleryTileGap).AlignItems(ui.Start).Children(func() {
			for col := 0; col < cols; col++ {
				if col < len(row.items) && row.items[col] < len(s.gallery.assets) {
					asset := s.gallery.assets[row.items[col]]
					s.galleryTile(c, asset, tileW, imgH, tileH, thumbEdge, opaque, tone)
				} else {
					ui.Box(c).Width(tileW).Height(tileH)
				}
			}
		})
	}).Absolute().Left(galleryInset).Right(galleryInset).Top(top).Bottom(bottom).Scrollbars(true).Label("摄影作品网格").AutoFocus()

	first, last := ls.Visible()
	if first >= 0 && last >= first && len(rows) > 0 {
		lo := max(0, first-2)
		hi := min(len(rows)-1, last+2)
		for ri := lo; ri <= hi; ri++ {
			if rows[ri].kind != galleryRowCards {
				continue
			}
			for _, idx := range rows[ri].items {
				if idx >= 0 && idx < len(s.gallery.assets) {
					s.prefetchGalleryThumb(s.gallery.assets[idx], thumbEdge)
				}
			}
		}
	}
}

func (s *Shell) galleryTile(c *ui.Context, asset GalleryAsset, w, imgH, totalH float32, thumbEdge int, opaque bool, tone ui.Color) {
	selected := s.gallery.selected[asset.ID]
	focused := s.Model.Gallery.FocusedID == asset.ID
	label := galleryAssetAccessibleLabel(asset)

	wrap := ui.Box(c.Key(fmt.Sprintf("gallery-tile-wrap-%d", asset.ID))).Size(w, totalH).Radius(galleryTileRadius).Clip()
	wrap.Children(func() {
		tile := ui.ButtonBase(c.Key(fmt.Sprintf("gallery-tile-%d", asset.ID))).
			Label(label).Tooltip(label).Fill().Radius(galleryTileRadius).Clip().
			TextColor(paper).Border(1, ui.RGBA(255, 255, 255, .09))

		cardBg := ui.RGBA(20, 22, 29, .86)
		if opaque {
			cardBg = s.theme.Surface
		}
		tile.Background(cardBg)
		if selected {
			tile.Border(2, tone)
		} else if tile.Hovered() || tile.FocusVisible() || focused {
			tile.Border(1.5, tone.Alpha(.78))
		}

		tile.Children(func() {
			ui.Column(c).Fill().Children(func() {
				stageBg := ui.RGBA(10, 11, 15, .95)
				ui.Box(c).FillWidth().Height(imgH).Background(stageBg).Clip().Children(func() {
					bmp := s.galleryThumb(asset, thumbEdge)
					if bmp != nil {
						ui.Image(c.Key(fmt.Sprintf("gallery-img-%d", asset.ID)), bmp).
							Absolute().Left(0).Right(0).Top(0).Bottom(0).Fit(ui.Contain).Label(asset.FileName).PassThrough()
					} else if asset.IsVideo() && asset.Available && !asset.Invalid {
						ui.Column(c).Fill().Justify(ui.Center).AlignItems(ui.Center).Gap(6).PassThrough().Children(func() {
							ui.Box(c).TextColor(tone.Alpha(.85)).Children(func() { glyph(c, "\uE714", 24) })
							sub := "MP4 录像"
							if asset.VideoCodec != "" {
								sub = asset.VideoCodec + " 录像"
							}
							ui.Text(c, sub).FontSize(11).TextColor(muted).SingleLine()
						})
					} else {
						placeholder := "加载中…"
						if asset.Invalid {
							placeholder = "媒体已损坏"
						} else if !asset.Available {
							placeholder = "源文件不存在"
						}
						ui.Row(c).Fill().Justify(ui.Center).AlignItems(ui.Center).PassThrough().Children(func() {
							ui.Text(c, placeholder).FontSize(12).TextColor(muted).SingleLine()
						})
					}

					badgeText, badgeBg, badgeFg := galleryStatusBadge(asset, tone, opaque, s.wishWarningColor())
					if badgeText != "" {
						ui.Row(c).Absolute().Left(8).Top(8).Height(20).Padding(0, 7).Radius(6).
							AlignItems(ui.Center).Background(badgeBg).PassThrough().Children(func() {
							ui.Text(c, badgeText).FontSize(10).FontWeight(600).TextColor(badgeFg).SingleLine()
						})
					}

					if len(asset.Tags) > 0 {
						tagTxt := "#" + truncateRunes(asset.Tags[0], 6)
						if len(asset.Tags) > 1 {
							tagTxt += fmt.Sprintf(" +%d", len(asset.Tags)-1)
						}
						ui.Row(c).Absolute().Left(8).Bottom(8).Height(20).Padding(0, 7).Radius(6).
							AlignItems(ui.Center).Background(ui.RGBA(12, 14, 20, .82)).PassThrough().Children(func() {
							ui.Text(c, tagTxt).FontSize(10).FontWeight(600).TextColor(paper).SingleLine()
						})
					}

					if asset.IsVideo() {
						durText := "视频"
						if asset.DurationMS > 0 {
							durText = formatGalleryDuration(asset.DurationMS)
						}
						ui.Row(c).Absolute().Right(8).Bottom(8).Height(20).Padding(0, 7).Radius(6).Gap(4).
							AlignItems(ui.Center).Background(ui.RGBA(10, 11, 15, .86)).PassThrough().Children(func() {
							ui.Box(c).TextColor(tone).Children(func() { glyph(c, "\uE768", 9) })
							mono(c, durText, 10).TextColor(paper)
						})
					}

					if s.Model.Gallery.SelectMode {
						checkBg := ui.RGBA(12, 13, 16, .72)
						checkBorder := ui.RGBA(255, 255, 255, .45)
						if selected {
							checkBg = tone
							checkBorder = tone
						}
						ui.Row(c).Absolute().Right(8).Top(8).Size(22, 22).Radius(11).
							Justify(ui.Center).AlignItems(ui.Center).Background(checkBg).Border(1.5, checkBorder).PassThrough().Children(func() {
							if selected {
								ui.Box(c).TextColor(ink).Children(func() { glyph(c, "\uE73E", 12) })
							}
						})
					}
				})

				ui.Box(c).FillWidth().Height(1).Background(ui.RGBA(255, 255, 255, .07)).PassThrough()

				ui.Row(c).FillWidth().Height(galleryTileFooterHeight-1).Padding(0, 10).AlignItems(ui.Center).Gap(6).PassThrough().Children(func() {
					name := asset.FileName
					if name == "" {
						name = "未命名作品"
					}
					ui.Text(c, name).FontSize(12).TextColor(paper).SingleLine().Grow(1).Shrink(1)
					if asset.Width > 0 && asset.Height > 0 {
						mono(c, fmt.Sprintf("%dx%d", asset.Width, asset.Height), 10).TextColor(muted)
					}
				})
			})
		}).OnClick(func() {
			if s.Model.Gallery.SelectMode {
				s.toggleGallerySelect(asset.ID)
				return
			}
			s.gallery.viewer.reset(asset.ID)
			s.Dispatch(state.Event{Kind: state.GalleryOpenViewer, Gallery: state.GalleryPayload{AssetID: asset.ID}})
		}).ContextMenu(func(m *ui.Menu) {
			s.galleryAssetContextMenu(m, asset)
		})

		if !s.Model.Gallery.SelectMode && !asset.Invalid {
			favLabel := "收藏 " + asset.FileName
			iconKey := "\uEB51"
			iconColor := paper.Alpha(.80)
			btnBg := ui.RGBA(12, 14, 20, .62)
			if asset.Favorited {
				favLabel = "取消收藏 " + asset.FileName
				iconKey = "\uEB52"
				iconColor = tone
				btnBg = ui.RGBA(12, 14, 20, .82)
			}
			favBtn := ui.ButtonBase(c.Key(fmt.Sprintf("gallery-fav-%d", asset.ID))).
				Label(favLabel).Tooltip(favLabel).
				Absolute().Right(8).Top(8).Size(26, 26).Radius(13).
				Justify(ui.Center).Background(btnBg).TextColor(iconColor)
			if favBtn.Hovered() || favBtn.FocusVisible() {
				favBtn.Border(1, tone).Background(ui.RGBA(12, 14, 20, .90))
			}
			favBtn.Children(func() { glyph(c, iconKey, 13) }).OnClick(func() {
				s.Dispatch(state.Event{Kind: state.GalleryToggleFavorite, Enabled: !asset.Favorited, Gallery: state.GalleryPayload{AssetID: asset.ID}})
			})
		}
	})
}

func (s *Shell) galleryStatusBar(c *ui.Context, g state.GalleryState, tone ui.Color, surfaceW, surfaceH float32, showCanvasControls bool) {
	statusText := "空闲"
	dot := muted
	switch {
	case g.Phase == state.GalleryScanning:
		statusText = "正在扫描…"
		dot = tone
	case g.Watching:
		statusText = "实时监听中"
		dot = wishPityGreen
	}
	summary := fmt.Sprintf("共 %d 项（%d 照片 · %d 视频）· 已归档 %d 项 (%s)",
		g.TotalCount, g.PhotoCount, g.VideoCount, g.ArchivedCount, formatGalleryBytes(g.ArchiveBytes))
	if g.ArchiveFreeBytes > 0 {
		summary += fmt.Sprintf(" · 剩余 %s", formatGalleryBytes(g.ArchiveFreeBytes))
	}
	if g.MissingCount > 0 {
		summary += fmt.Sprintf(" · 缺失 %d", g.MissingCount)
	}
	if g.InvalidCount > 0 {
		summary += fmt.Sprintf(" · 损坏 %d", g.InvalidCount)
	}

	pillBg := ui.RGBA(12, 14, 18, .72)
	ui.Row(c.Key("gallery-status-bar")).Absolute().Left(galleryInset).Right(galleryInset).Bottom(galleryBottomInset).
		Height(galleryStatusBarHeight).AlignItems(ui.Center).Gap(10).Children(func() {
		// Left translucent status capsule.
		ui.Row(c).Height(galleryStatusBarHeight).Padding(0, 12).Radius(16).AlignItems(ui.Center).Gap(8).
			Background(pillBg).Border(1, ui.RGBA(255, 255, 255, .08)).Children(func() {
			ui.Box(c).Size(6, 6).Radius(3).Background(dot)
			ui.Text(c, statusText).FontSize(12).TextColor(paper).SingleLine()
			ui.Text(c, "·").FontSize(12).TextColor(muted)
			ui.Text(c, summary).FontSize(12).TextColor(muted).SingleLine().Tooltip(summary)
			if s.OpenFolder != nil && (g.EffectiveArchive != "" || len(s.gallery.sources) > 0) {
				folderBtn := ui.ButtonBase(c.Key("gallery-open-folder")).Label("打开文件夹").Tooltip("打开归档目录").
					Height(22).Padding(0, 6).Radius(6).Gap(4).Justify(ui.Center).TextColor(muted)
				if folderBtn.Hovered() || folderBtn.FocusVisible() {
					folderBtn.Background(ui.RGBA(255, 255, 255, .08)).TextColor(paper)
				}
				folderBtn.Children(func() {
					glyph(c, "\uE8B7", 12)
				}).OnClick(func() {
					target := g.EffectiveArchive
					if target == "" && len(s.gallery.sources) > 0 {
						target = s.gallery.sources[0].RootPath
					}
					s.Dispatch(state.Event{Kind: state.GalleryOpenFolder, Path: target})
				})
			}
		})

		ui.Box(c).Grow(1)

		// Right floating canvas controls (zoom & recenter) or timeline shortcut hint.
		if showCanvasControls {
			quilt := s.gallery.canvas.ensureQuilt(s.gallery.assets, s.gallery.rowsRev)
			zoomPct := int(math.Round(float64(max(s.gallery.canvas.zoom, 0.1) * 100)))
			ui.Row(c.Key("gallery-canvas-hud")).Height(galleryStatusBarHeight).Padding(0, 6).Radius(16).AlignItems(ui.Center).Gap(2).
				Background(pillBg).Border(1, ui.RGBA(255, 255, 255, .08)).Children(func() {
				ui.Text(c, "左键拖拽 · Ctrl+滚轮缩放").FontSize(11).TextColor(muted).Padding(0, 8, 0, 6).SingleLine()
				zoomBtn := func(key, label, text string, onClick func()) {
					b := ui.ButtonBase(c.Key(key)).Label(label).Tooltip(label).
						Height(24).Padding(0, 7).Radius(12).Justify(ui.Center).TextColor(paper)
					if b.Hovered() || b.FocusVisible() {
						b.Background(ui.RGBA(255, 255, 255, .10))
					}
					b.Children(func() { mono(c, text, 11) }).OnClick(onClick)
				}
				zoomBtn("gallery-canvas-zoom-out", "缩小画廊", "−", func() {
					s.adjustGalleryCanvasZoom(quilt, surfaceW, surfaceH, 1/1.15, surfaceW/2, surfaceH/2)
				})
				resetBtn := ui.ButtonBase(c.Key("gallery-canvas-reset")).Label("重置画廊视角").Tooltip("重置视角与缩放 (0)").
					Height(24).Padding(0, 8).Radius(12).Gap(5).Justify(ui.Center).TextColor(paper)
				if resetBtn.Hovered() || resetBtn.FocusVisible() {
					resetBtn.Background(ui.RGBA(255, 255, 255, .10))
				}
				resetBtn.Children(func() {
					ui.Box(c).TextColor(muted).Children(func() { glyph(c, "\uE72A", 11) })
					mono(c, fmt.Sprintf("%d%%", zoomPct), 11)
				}).OnClick(func() {
					s.gallery.canvas.recenter(quilt, surfaceW, surfaceH)
				})
				zoomBtn("gallery-canvas-zoom-in", "放大画廊", "+", func() {
					s.adjustGalleryCanvasZoom(quilt, surfaceW, surfaceH, 1.15, surfaceW/2, surfaceH/2)
				})
			})
		} else {
			ui.Row(c).Height(galleryStatusBarHeight).Padding(0, 12).Radius(16).AlignItems(ui.Center).
				Background(pillBg).Border(1, ui.RGBA(255, 255, 255, .08)).Children(func() {
				ui.Text(c, "Space 预览 · Ctrl+A 全选 · Ctrl+S 导出").FontSize(11).TextColor(muted).SingleLine()
			})
		}
	})
}

func galleryStatusBadge(a GalleryAsset, tone ui.Color, opaque bool, warn ui.Color) (text string, bg, fg ui.Color) {
	if a.Invalid {
		return "已损坏", ui.RGBA(40, 18, 22, .90), warn
	}
	if !a.Available {
		return "源文件不存在", ui.RGBA(40, 26, 16, .90), warn
	}
	if a.HasArchived {
		if opaque {
			return "已归档", tone, ink
		}
		return "已归档", tone.Alpha(.88), ink
	}
	if a.HasExported || a.HasCustom {
		return "已保存", ui.RGBA(14, 16, 22, .84), paper
	}
	if a.HasCached {
		return "游戏缓存", ui.RGBA(14, 16, 22, .84), muted
	}
	return "", ui.Transparent, paper
}

func galleryAssetAccessibleLabel(a GalleryAsset) string {
	parts := []string{a.FileName}
	if a.IsVideo() {
		if a.DurationMS > 0 {
			parts = append(parts, "视频 "+formatGalleryDuration(a.DurationMS))
		} else {
			parts = append(parts, "视频")
		}
	}
	if a.Width > 0 && a.Height > 0 {
		parts = append(parts, fmt.Sprintf("%d×%d", a.Width, a.Height))
	}
	if a.Favorited {
		parts = append(parts, "已收藏")
	}
	var tags []string
	if a.Invalid {
		tags = append(tags, "已损坏")
	} else if !a.Available {
		tags = append(tags, "源文件不存在")
	} else {
		if a.HasArchived {
			tags = append(tags, "已归档")
		}
		if a.HasCached {
			tags = append(tags, "游戏缓存")
		}
		if a.HasExported || a.HasCustom {
			tags = append(tags, "已保存")
		}
	}
	if len(tags) > 0 {
		parts = append(parts, strings.Join(tags, "、"))
	}
	return strings.Join(parts, "，")
}

func shortUID(uid string) string {
	if len(uid) <= 8 {
		return uid
	}
	return "…" + uid[len(uid)-6:]
}

func truncateRunes(s string, maxRunes int) string {
	r := []rune(s)
	if len(r) <= maxRunes {
		return s
	}
	return string(r[:maxRunes]) + "…"
}

func formatGalleryDuration(ms int64) string {
	if ms <= 0 {
		return "00:00"
	}
	totalSec := (ms + 500) / 1000
	h := totalSec / 3600
	m := (totalSec % 3600) / 60
	sec := totalSec % 60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, sec)
	}
	return fmt.Sprintf("%02d:%02d", m, sec)
}

func formatGalleryBytes(n int64) string {
	if n <= 0 {
		return "0 B"
	}
	const (
		kib = 1024
		mib = 1024 * kib
		gib = 1024 * mib
	)
	switch {
	case n >= gib:
		return fmt.Sprintf("%.2f GB", float64(n)/float64(gib))
	case n >= mib:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(mib))
	case n >= kib:
		return fmt.Sprintf("%.1f KB", float64(n)/float64(kib))
	default:
		return fmt.Sprintf("%d B", n)
	}
}
