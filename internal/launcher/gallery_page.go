// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func (s *Shell) galleryPage(c *ui.Context) {
	g := s.Model.Gallery
	tone := s.tone()
	prefs := c.Preferences()
	opaque := prefs.HighContrast || prefs.ReduceTransparency
	width, _ := c.Size()
	available := max(width-railWidth-bezelWidth-2*galleryInset, 420)

	// Handle page-level keyboard shortcuts when no modal is open.
	// Evaluate every Shortcut call unconditionally so registrations stay active.
	if !g.ModalOpen() && s.Model.Overlay == state.NoOverlay {
		selectAll := c.Shortcut(ui.Ctrl, ui.KeyA) && !c.ShortcutRepeated()
		copyImg := c.Shortcut(ui.Ctrl, ui.KeyC) && !c.ShortcutRepeated()
		exportKey := c.Shortcut(ui.Ctrl, ui.KeyS) && !c.ShortcutRepeated()
		previewKey := c.Shortcut(0, ui.KeySpace) && !c.ShortcutRepeated()
		escSelect := c.Shortcut(0, ui.KeyEscape) && !c.ShortcutRepeated() && g.SelectMode
		switch {
		case selectAll:
			s.Dispatch(state.Event{Kind: state.GallerySelectAll})
		case copyImg:
			if id := s.galleryShortcutTargetID(); id > 0 {
				s.Dispatch(state.Event{Kind: state.GalleryCopyImage, Gallery: state.GalleryPayload{AssetID: id}})
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
		surface.Material(frostedSurface{Tint: ui.RGBA(10, 11, 14, .58)})
	}
	surface.Children(func() {
		if !opaque {
			ui.Box(c).Absolute().Right(wishBloomRight).Bottom(wishBloomBottom).Size(wishBloomSize, wishBloomSize).PassThrough().RadialGradient(ui.RadialGradient{
				Radius: wishBloomSize / 2,
				Stops:  []ui.GradientStop{{Color: tone.Alpha(wishHighlightOpacity), At: 0}, {Color: ui.Transparent, At: 1}},
			})
		}

		// Top header + toolbar + scope tabs.
		ui.Column(c.Key("gallery-header")).Absolute().Left(galleryInset).Right(galleryInset).Top(galleryHeaderTop).Gap(10).Children(func() {
			s.galleryToolbar(c, g, tone)
			s.galleryScopeBar(c, g, tone)
			if g.Notice != "" {
				color := muted
				if g.NoticeWarn {
					color = s.wishWarningColor()
				}
				ui.Text(c, g.Notice).FontSize(12).TextColor(color).SingleLine().Tooltip(g.Notice)
			}
		})

		gridTop := galleryGridTop
		if g.Notice != "" {
			gridTop = galleryGridTopNotice
		}
		gridBottom := galleryBottomInset + galleryStatusBarHeight + 8

		// Main body: loading/error/empty or virtualized date-grouped photo grid.
		switch {
		case g.Phase == state.GalleryFailed && len(s.gallery.assets) == 0:
			s.galleryEmptyCard(c, gridTop, "无法读取相册索引", g.Error, true)
		case len(s.gallery.assets) == 0:
			title, body := "还没有发现摄影作品", "在《异环》中拍照或保存到系统图片目录后，海特洛相册会自动发现并整理你的作品。也可以手动添加自定义文件夹。"
			if g.Phase == state.GalleryScanning {
				title, body = "正在扫描媒体目录", "正在读取游戏拍照缓存与已保存图片目录的元数据…"
			} else if g.TotalCount > 0 {
				title, body = "当前筛选条件下没有照片", "切换来源分类或 UID 筛选器可查看全部已索引作品。"
			}
			s.galleryEmptyCard(c, gridTop, title, body, false)
		default:
			s.galleryVirtualGrid(c, available, gridTop, gridBottom, opaque, tone)
		}

		// Bottom real-time status bar.
		s.galleryStatusBar(c, g, tone)
	})

	if s.galleryImagesEnabled() && !s.gallery.images.active && len(s.gallery.images.wanted) > 0 {
		c.Invalidate()
	}

	// Gallery modals (viewer, settings drawer, delete archive confirmation).
	s.galleryViewerOverlay(c)
	s.gallerySettingsOverlay(c)
	s.galleryDeleteArchiveConfirm(c)
}

func (s *Shell) galleryShortcutTargetID() int64 {
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
	b := s.quietButton(c, key, label, disabled).Height(galleryControlHeight).Radius(10).Padding(0, 12)
	if prefs := c.Preferences(); prefs.HighContrast || prefs.ReduceTransparency {
		b.Background(s.theme.Surface).Border(1, muted)
	}
	return b
}

func (s *Shell) galleryToolbar(c *ui.Context, g state.GalleryState, tone ui.Color) {
	ui.Row(c.Key("gallery-toolbar")).FillWidth().Height(galleryToolbarHeight).AlignItems(ui.Center).Gap(8).Children(func() {
		ui.Row(c).AlignItems(ui.Center).Gap(10).Grow(1).Children(func() {
			ui.Text(c, "海特洛相册").FontSize(26).FontWeight(700).SingleLine()
			countText := fmt.Sprintf("%d 张", g.TotalCount)
			if g.FilteredCount != g.TotalCount {
				countText = fmt.Sprintf("%d / %d 张", g.FilteredCount, g.TotalCount)
			}
			ui.Row(c).Height(24).Padding(0, 10).Radius(12).AlignItems(ui.Center).
				Background(ui.RGBA(255, 255, 255, .06)).Border(1, hairline).Children(func() {
				mono(c, countText, 12).TextColor(muted)
			})
		})

		if g.SelectMode {
			ui.Text(c, fmt.Sprintf("已选 %d 张", g.SelectedCnt)).FontSize(13).TextColor(tone).SingleLine()
			s.gallerySmallButton(c, "gallery-select-all", "全选", g.FilteredCount == 0).OnClick(func() {
				s.Dispatch(state.Event{Kind: state.GallerySelectAll})
			})
			s.accentButton(c, "gallery-batch-export", fmt.Sprintf("导出 (%d)", g.SelectedCnt)).
				Height(galleryControlHeight).Radius(10).Padding(0, 14).Disabled(g.Busy || g.SelectedCnt == 0).
				OnClick(func() { s.Dispatch(state.Event{Kind: state.GalleryExportSelected}) })
			s.gallerySmallButton(c, "gallery-exit-select", "取消多选", false).OnClick(func() {
				s.Dispatch(state.Event{Kind: state.GalleryToggleSelectMode})
			})
			return
		}

		// UID filter menu.
		uidLabel := "全部 UID ▾"
		switch g.UID {
		case "":
		case state.GalleryUIDUnknown:
			uidLabel = "未知 UID ▾"
		default:
			uidLabel = "UID " + shortUID(g.UID) + " ▾"
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

		refreshLabel := "刷新"
		if g.Phase == state.GalleryScanning {
			refreshLabel = "扫描中…"
		}
		s.gallerySmallButton(c, "gallery-refresh", refreshLabel, g.Phase == state.GalleryScanning || g.Busy).OnClick(func() {
			s.Dispatch(state.Event{Kind: state.GalleryRefresh})
		})

		s.gallerySmallButton(c, "gallery-open-folder", "打开文件夹", s.OpenFolder == nil && g.EffectiveArchive == "").OnClick(func() {
			target := g.EffectiveArchive
			if target == "" && len(s.gallery.sources) > 0 {
				target = s.gallery.sources[0].RootPath
			}
			s.Dispatch(state.Event{Kind: state.GalleryOpenFolder, Path: target})
		})

		s.gallerySmallButton(c, "gallery-multi-select", "多选", len(s.gallery.assets) == 0).OnClick(func() {
			s.Dispatch(state.Event{Kind: state.GalleryToggleSelectMode})
		})

		s.gallerySmallButton(c, "gallery-settings-btn", "相册设置", false).OnClick(func() {
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
		{"gallery-tab-cached", "游戏缓存", g.CachedCount, state.GalleryScopeCached},
		{"gallery-tab-exported", "已保存", g.ExportedCount, state.GalleryScopeExported},
		{"gallery-tab-archived", "已归档", g.ArchivedCount, state.GalleryScopeArchived},
	}
	ui.Row(c.Key("gallery-scope-tabs")).FillWidth().Height(galleryTabsHeight).AlignItems(ui.Center).Gap(6).
		Role(ui.RoleTabList).Label("相册来源筛选").Children(func() {
		for _, tab := range tabs {
			tab := tab
			selected := g.Scope == tab.scope
			title := fmt.Sprintf("%s %d", tab.label, tab.count)
			b := ui.ButtonBase(c.Key(tab.key)).Label(tab.label).Role(ui.RoleTab).Selected(selected).
				Height(galleryTabsHeight).Padding(0, 12).Radius(8).Justify(ui.Center).TextColor(muted)
			if selected {
				b.TextColor(paper).Background(ui.RGBA(255, 255, 255, .07))
			} else if b.Hovered() || b.FocusVisible() {
				b.TextColor(paper).Background(ui.RGBA(255, 255, 255, .04))
			}
			b.Children(func() {
				ui.Text(c, title).FontSize(13).FontWeight(600).SingleLine()
				if selected {
					ui.Box(c).Absolute().Left(12).Right(12).Bottom(2).Height(2.5).Radius(1.25).Background(tone)
				}
			}).OnClick(func() {
				s.Dispatch(state.Event{Kind: state.GallerySetScope, Gallery: state.GalleryPayload{Scope: tab.scope}})
			})
		}
		ui.Box(c).Grow(1)
		if g.AutoArchive {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(6, 6).Radius(3).Background(tone)
				ui.Text(c, "自动归档已开启").FontSize(12).TextColor(muted).SingleLine()
			})
		} else {
			ui.Text(c, "自动归档未开启").FontSize(12).TextColor(muted).SingleLine()
		}
	})
}

func (s *Shell) galleryEmptyCard(c *ui.Context, top float32, title, body string, failed bool) {
	if failed && body == "" {
		body = "读取相册数据库或目录时发生错误，请检查目录权限后重试。"
	}
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
			} else if s.Model.Gallery.Phase != state.GalleryScanning {
				s.accentButton(c, "gallery-empty-scan", "立即扫描").OnClick(func() {
					s.Dispatch(state.Event{Kind: state.GalleryRefresh})
				})
				if s.PickGalleryDirectory != nil {
					s.quietButton(c, "gallery-empty-add", "添加自定义文件夹", s.Model.Gallery.Busy).OnClick(func() {
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
	imgH := tileW * 9 / 16 // 16:9 frame matching desktop widescreen screenshots; images render with Contain!
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
			return "date:" + r.date
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
			hdr := ui.Row(c.Key("gallery-date-"+row.date)).FillWidth().Height(galleryDateHeaderHeight).
				Padding(0, 4).AlignItems(ui.Center).Gap(8)
			if opaque {
				hdr.Background(s.theme.Surface)
			} else {
				hdr.Background(ui.RGBA(14, 15, 20, .86))
			}
			hdr.Children(func() {
				ui.Text(c, row.date).FontSize(14).FontWeight(600).TextColor(paper).SingleLine()
				mono(c, fmt.Sprintf("%d 张", row.count), 12).TextColor(muted)
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

	// Prefetch thumbnails for rows immediately above and below the visible window.
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

	tile := ui.ButtonBase(c.Key(fmt.Sprintf("gallery-tile-%d", asset.ID))).
		Label(label).Tooltip(label).Size(w, totalH).Radius(galleryTileRadius).Clip().
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
			// Thumbnail stage: artwork always placed with ui.Contain per AGENTS.md.
			stageBg := ui.RGBA(10, 11, 15, .95)
			ui.Box(c).FillWidth().Height(imgH).Background(stageBg).Clip().Children(func() {
				bmp := s.galleryThumb(asset, thumbEdge)
				if bmp != nil {
					ui.Image(c.Key(fmt.Sprintf("gallery-img-%d", asset.ID)), bmp).
						Absolute().Left(0).Right(0).Top(0).Bottom(0).Fit(ui.Contain).Label(asset.FileName).PassThrough()
				} else {
					placeholder := "加载中…"
					if asset.Invalid {
						placeholder = "图片已损坏"
					} else if !asset.Available {
						placeholder = "源文件不存在"
					}
					ui.Row(c).Fill().Justify(ui.Center).AlignItems(ui.Center).PassThrough().Children(func() {
						ui.Text(c, placeholder).FontSize(12).TextColor(muted).SingleLine()
					})
				}

				// Top-left provenance/status badge.
				badgeText, badgeBg, badgeFg := galleryStatusBadge(asset, tone, opaque, s.wishWarningColor())
				if badgeText != "" {
					ui.Row(c).Absolute().Left(8).Top(8).Height(20).Padding(0, 8).Radius(6).
						AlignItems(ui.Center).Background(badgeBg).Border(1, ui.RGBA(255, 255, 255, .10)).PassThrough().Children(func() {
						ui.Text(c, badgeText).FontSize(10).FontWeight(600).TextColor(badgeFg).SingleLine()
					})
				}

				// Top-right selection indicator in multi-select mode.
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

			// Compact metadata footer.
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
		if m.Item("查看大图").Chosen() {
			s.gallery.viewer.reset(asset.ID)
			s.Dispatch(state.Event{Kind: state.GalleryOpenViewer, Gallery: state.GalleryPayload{AssetID: asset.ID}})
		}
		if m.Item("复制图片").Disabled(!asset.Available || asset.Invalid).Chosen() {
			s.Dispatch(state.Event{Kind: state.GalleryCopyImage, Gallery: state.GalleryPayload{AssetID: asset.ID}})
		}
		if m.Item("导出图片…").Disabled(!asset.Available || asset.Invalid).Chosen() {
			s.Dispatch(state.Event{Kind: state.GalleryExportSingle, Gallery: state.GalleryPayload{AssetID: asset.ID}})
		}
		if m.Item("打开文件位置").Disabled(!asset.Available).Chosen() {
			s.Dispatch(state.Event{Kind: state.GalleryRevealAsset, Gallery: state.GalleryPayload{AssetID: asset.ID}})
		}
		m.Separator()
		if !asset.HasArchived && asset.Available && !asset.Invalid {
			if m.Item("归档副本").Chosen() {
				s.Dispatch(state.Event{Kind: state.GalleryArchiveAsset, Gallery: state.GalleryPayload{AssetID: asset.ID}})
			}
		}
		if asset.HasArchived {
			if m.Item("删除 Oddward 归档副本…").Chosen() {
				s.Dispatch(state.Event{Kind: state.GalleryRequestDeleteArchive, Gallery: state.GalleryPayload{AssetID: asset.ID}})
			}
		}
	})
}

func (s *Shell) galleryStatusBar(c *ui.Context, g state.GalleryState, tone ui.Color) {
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
	summary := fmt.Sprintf("共 %d 项 · 已归档 %d 项 (%s)", g.TotalCount, g.ArchivedCount, formatGalleryBytes(g.ArchiveBytes))
	if g.MissingCount > 0 {
		summary += fmt.Sprintf(" · 缺失 %d", g.MissingCount)
	}
	if g.InvalidCount > 0 {
		summary += fmt.Sprintf(" · 损坏 %d", g.InvalidCount)
	}

	ui.Row(c.Key("gallery-status-bar")).Absolute().Left(galleryInset).Right(galleryInset).Bottom(galleryBottomInset).
		Height(galleryStatusBarHeight).Padding(0, 12).Radius(8).AlignItems(ui.Center).Gap(10).
		Background(ui.RGBA(12, 14, 18, .55)).Border(1, hairline).Children(func() {
		ui.Box(c).Size(6, 6).Radius(3).Background(dot)
		ui.Text(c, statusText).FontSize(12).TextColor(paper).SingleLine()
		ui.Box(c).Size(1, 12).Background(hairline)
		ui.Text(c, summary).FontSize(12).TextColor(muted).SingleLine().Grow(1).Shrink(1).Tooltip(summary)
		ui.Text(c, "Space 预览 · Ctrl+A 全选 · Ctrl+C 复制 · Ctrl+S 导出").FontSize(11).TextColor(muted).SingleLine()
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
	if a.Width > 0 && a.Height > 0 {
		parts = append(parts, fmt.Sprintf("%d×%d", a.Width, a.Height))
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
