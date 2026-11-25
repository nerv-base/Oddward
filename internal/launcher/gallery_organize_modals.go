// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// galleryAlbumModalOverlay renders the centered flat frosted-glass dialog for
// creating, renaming, deleting and assigning custom albums (zero nested boxes).
func (s *Shell) galleryAlbumModalOverlay(c *ui.Context) {
	g := s.Model.Gallery
	if !g.AlbumModalOpen {
		s.gallery.renamingAlbumID = 0
		return
	}
	prefs := c.Preferences()
	opaque := prefs.HighContrast || prefs.ReduceTransparency
	shade := ui.RGBA(6, 7, 10, .48)
	tint := ui.RGBA(18, 19, 24, .86)
	if opaque {
		shade = ink
		tint = s.theme.Surface
	}
	tone := s.tone()
	w, h := c.Size()
	dialogW := min(galleryOrganizeDialogWidth, max(w-64, 400))
	dialogH := min(galleryOrganizeDialogHeight, max(h-64, 340))

	targetAsset, hasTarget := GalleryAsset{}, false
	if g.AlbumModalAssetID > 0 {
		targetAsset, hasTarget = s.galleryAssetByID(g.AlbumModalAssetID)
	}
	batchMode := !hasTarget && g.SelectMode && g.SelectedCnt > 0

	title := "自定义相册"
	subtitle := "删除相册仅移除分组，不会删除本地文件。"
	if hasTarget {
		title = "添加到相册"
		subtitle = targetAsset.FileName
	} else if batchMode {
		title = fmt.Sprintf("将已选 %d 项加入相册", g.SelectedCnt)
	}

	submitNew := func() {
		name := strings.TrimSpace(s.gallery.albumNameInput)
		if name == "" || g.Busy {
			return
		}
		s.Dispatch(state.Event{
			Kind:    state.GalleryCreateAlbum,
			Message: name,
			Gallery: state.GalleryPayload{AssetID: g.AlbumModalAssetID},
		})
	}

	ui.Overlay(c, func() {
		back := ui.Row(c.Key("gallery-album-backdrop")).Absolute().Left(0).Top(0).Right(0).Bottom(0).
			Justify(ui.Center).AlignItems(ui.Center).Padding(24).Background(shade).Modal()
		back.Children(func() {
			panel := ui.Column(c.Key("gallery-album-panel")).Size(dialogW, dialogH).
				Padding(22, 24, 18, 24).Gap(14).
				Radius(sheetRadius).Clip().
				Border(1, ui.RGBA(255, 255, 255, .10)).
				Shadow(0, 20, 56, 0, ui.RGBA(0, 0, 0, .50)).
				Role(ui.RoleDialog).Label("管理自定义相册")
			if opaque {
				panel.Background(tint)
			} else {
				panel.Material(frostedSurface{Tint: tint})
			}
			if s.motion() {
				entered := ui.Local(panel, "gallery-album-entered", func() bool { return false })
				pose := ui.Pose{}
				if !*entered {
					pose, *entered = ui.Pose{Y: sheetEnterDistance}, true
					c.Invalidate()
				}
				panel.AnimatePose(pose, ui.Animation{Duration: 180 * time.Millisecond})
			}

			panel.Children(func() {
				ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(12).Children(func() {
					ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
						ui.Text(c, title).FontSize(20).FontWeight(700).TextColor(paper).SingleLine()
						ui.Text(c, subtitle).FontSize(12).TextColor(muted).SingleLine().Tooltip(subtitle)
					})
					iconButton(c, "gallery-album-close", "关闭相册管理", "\uE8BB", 30, func() {
						s.Dispatch(state.Event{Kind: state.GalleryCloseAlbumModal})
					})
				})

				ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
					ui.TextInput(c.Key("gallery-album-new-input"), &s.gallery.albumNameInput).
						Label("新相册名称").Placeholder("输入新相册名称…").
						Grow(1).Height(36).Padding(6, 10).Radius(8).FontSize(13).
						Background(ui.RGBA(255, 255, 255, .05)).Border(1, hairline).TextColor(paper).
						Disabled(g.Busy).OnSubmit(submitNew)
					s.accentButton(c, "gallery-album-create-btn", "新建相册").
						Height(36).Radius(8).Padding(0, 14).
						Disabled(g.Busy || strings.TrimSpace(s.gallery.albumNameInput) == "").
						OnClick(submitNew)
				})

				ui.Box(c).FillWidth().Height(1).Background(hairline)

				// Flat hairline-separated list — zero card-in-card borders.
				ui.Scroll(c.Key("gallery-album-list")).FillWidth().Grow(1).MinHeight(0).Scrollbars(true).Children(func() {
					ui.Column(c).FillWidth().Children(func() {
						if len(s.gallery.albums) == 0 {
							ui.Text(c, "还没有自定义相册，可在上方直接新建。").FontSize(13).TextColor(muted).Padding(16, 0)
							return
						}
						for i, al := range s.gallery.albums {
							al := al
							if i > 0 {
								ui.Box(c).FillWidth().Height(1).Background(ui.RGBA(255, 255, 255, .06))
							}
							inAlbum := hasTarget && slices.Contains(targetAsset.AlbumIDs, al.ID)
							ui.Column(c.Key(fmt.Sprintf("gallery-album-row-%d", al.ID))).FillWidth().Padding(10, 2).Children(func() {
								if s.gallery.renamingAlbumID == al.ID {
									saveRename := func() {
										next := strings.TrimSpace(s.gallery.renameAlbumInput)
										if next == "" || g.Busy {
											return
										}
										s.Dispatch(state.Event{
											Kind:    state.GalleryRenameAlbum,
											Message: next,
											Gallery: state.GalleryPayload{AlbumID: al.ID},
										})
									}
									ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
										ui.TextInput(c.Key(fmt.Sprintf("gallery-album-rename-input-%d", al.ID)), &s.gallery.renameAlbumInput).
											Label("重命名相册").Grow(1).Height(32).Padding(4, 10).Radius(8).FontSize(13).
											Background(ui.RGBA(255, 255, 255, .06)).Border(1, tone.Alpha(.6)).TextColor(paper).
											OnSubmit(saveRename)
										s.gallerySmallButton(c, fmt.Sprintf("gallery-album-rename-save-%d", al.ID), "保存", g.Busy || strings.TrimSpace(s.gallery.renameAlbumInput) == "").Height(30).OnClick(saveRename)
										s.gallerySmallButton(c, fmt.Sprintf("gallery-album-rename-cancel-%d", al.ID), "取消", false).Height(30).OnClick(func() {
											s.gallery.renamingAlbumID = 0
											s.gallery.renameAlbumInput = ""
										})
									})
									return
								}
								ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(8).Children(func() {
									ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
										nameColor := paper
										if inAlbum {
											nameColor = tone
										}
										ui.Text(c, al.Name).FontSize(14).FontWeight(500).TextColor(nameColor).SingleLine().Tooltip(al.Name)
										mono(c, fmt.Sprintf("%d 项", al.Count), 11).TextColor(muted)
									})
									if hasTarget {
										if inAlbum {
											s.gallerySmallButton(c, fmt.Sprintf("gallery-album-rm-item-%d", al.ID), "移出", g.Busy).Height(30).OnClick(func() {
												s.Dispatch(state.Event{Kind: state.GalleryRemoveFromAlbum, Gallery: state.GalleryPayload{AlbumID: al.ID, AssetID: targetAsset.ID}})
											})
										} else {
											s.gallerySmallButton(c, fmt.Sprintf("gallery-album-add-item-%d", al.ID), "加入", g.Busy).Height(30).OnClick(func() {
												s.Dispatch(state.Event{Kind: state.GalleryAddToAlbum, Gallery: state.GalleryPayload{AlbumID: al.ID, AssetID: targetAsset.ID}})
											})
										}
									} else if batchMode {
										s.gallerySmallButton(c, fmt.Sprintf("gallery-album-add-batch-%d", al.ID), "加入所选", g.Busy).Height(30).OnClick(func() {
											s.Dispatch(state.Event{Kind: state.GalleryAddToAlbum, Gallery: state.GalleryPayload{AlbumID: al.ID}})
										})
									} else {
										s.gallerySmallButton(c, fmt.Sprintf("gallery-album-filter-%d", al.ID), "查看", false).Height(30).OnClick(func() {
											s.Dispatch(state.Event{Kind: state.GallerySetAlbum, Gallery: state.GalleryPayload{AlbumID: al.ID}})
											s.Dispatch(state.Event{Kind: state.GalleryCloseAlbumModal})
										})
									}
									iconButton(c, fmt.Sprintf("gallery-album-rename-%d", al.ID), "重命名相册", "\uE70F", 28, func() {
										s.gallery.renamingAlbumID = al.ID
										s.gallery.renameAlbumInput = al.Name
									})
									iconButton(c, fmt.Sprintf("gallery-album-del-%d", al.ID), "删除相册", "\uE74D", 28, func() {
										s.Dispatch(state.Event{Kind: state.GalleryDeleteAlbum, Gallery: state.GalleryPayload{AlbumID: al.ID}})
									})
								})
							})
						}
					})
				})

				ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Grow(1).Children(func() {
						mono(c, "ESC", 10).Padding(2, 7).Radius(5).Background(ui.RGBA(255, 255, 255, .06)).Border(1, hairline).TextColor(muted)
						ui.Text(c, "关闭").FontSize(12).TextColor(muted).SingleLine()
					})
					s.gallerySmallButton(c, "gallery-album-done", "完成", false).Padding(0, 18).OnClick(func() {
						s.Dispatch(state.Event{Kind: state.GalleryCloseAlbumModal})
					})
				})
			})
			if back.OverlayShortcut(0, ui.KeyEscape) || panel.PressedOutside() {
				s.Dispatch(state.Event{Kind: state.GalleryCloseAlbumModal})
			}
		})
	})
}

// galleryTagModalOverlay renders the centered flat frosted-glass dialog for
// attaching and removing tags without nested box borders.
func (s *Shell) galleryTagModalOverlay(c *ui.Context) {
	g := s.Model.Gallery
	if !g.TagModalOpen {
		return
	}
	prefs := c.Preferences()
	opaque := prefs.HighContrast || prefs.ReduceTransparency
	shade := ui.RGBA(6, 7, 10, .48)
	tint := ui.RGBA(18, 19, 24, .86)
	if opaque {
		shade = ink
		tint = s.theme.Surface
	}
	tone := s.tone()
	w, h := c.Size()
	dialogW := min(float32(460), max(w-64, 380))
	dialogH := min(float32(390), max(h-64, 300))

	targetAsset, hasTarget := GalleryAsset{}, false
	if g.TagModalAssetID > 0 {
		targetAsset, hasTarget = s.galleryAssetByID(g.TagModalAssetID)
	}
	title := "管理标签"
	subtitle := "为所选作品添加标签，方便在搜索栏或标签菜单中筛选。"
	if hasTarget {
		title = "作品标签"
		subtitle = targetAsset.FileName
	} else if g.SelectedCnt > 0 {
		title = fmt.Sprintf("为已选 %d 项添加标签", g.SelectedCnt)
	}

	submitTag := func(raw string) {
		name := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(raw), "#"))
		if name == "" || g.Busy {
			return
		}
		s.Dispatch(state.Event{
			Kind:    state.GalleryAddTag,
			Message: name,
			Gallery: state.GalleryPayload{AssetID: g.TagModalAssetID},
		})
	}

	ui.Overlay(c, func() {
		back := ui.Row(c.Key("gallery-tag-backdrop")).Absolute().Left(0).Top(0).Right(0).Bottom(0).
			Justify(ui.Center).AlignItems(ui.Center).Padding(24).Background(shade).Modal()
		back.Children(func() {
			panel := ui.Column(c.Key("gallery-tag-panel")).Size(dialogW, dialogH).
				Padding(22, 24, 18, 24).Gap(14).
				Radius(sheetRadius).Clip().
				Border(1, ui.RGBA(255, 255, 255, .10)).
				Shadow(0, 20, 56, 0, ui.RGBA(0, 0, 0, .50)).
				Role(ui.RoleDialog).Label("管理标签")
			if opaque {
				panel.Background(tint)
			} else {
				panel.Material(frostedSurface{Tint: tint})
			}

			panel.Children(func() {
				ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(12).Children(func() {
					ui.Column(c).Grow(1).MinWidth(0).Gap(2).Children(func() {
						ui.Text(c, title).FontSize(20).FontWeight(700).TextColor(paper).SingleLine()
						ui.Text(c, subtitle).FontSize(12).TextColor(muted).SingleLine().Tooltip(subtitle)
					})
					iconButton(c, "gallery-tag-close", "关闭标签管理", "\uE8BB", 30, func() {
						s.Dispatch(state.Event{Kind: state.GalleryCloseTagModal})
					})
				})

				ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
					ui.TextInput(c.Key("gallery-tag-new-input"), &s.gallery.tagNameInput).
						Label("标签名称").Placeholder("输入标签，例如 夜景、街拍…").
						Grow(1).Height(36).Padding(6, 10).Radius(8).FontSize(13).
						Background(ui.RGBA(255, 255, 255, .05)).Border(1, hairline).TextColor(paper).
						Disabled(g.Busy).OnSubmit(func() { submitTag(s.gallery.tagNameInput) })
					s.accentButton(c, "gallery-tag-add-btn", "添加标签").
						Height(36).Radius(8).Padding(0, 14).
						Disabled(g.Busy || strings.TrimSpace(s.gallery.tagNameInput) == "").
						OnClick(func() { submitTag(s.gallery.tagNameInput) })
				})

				ui.Box(c).FillWidth().Height(1).Background(hairline)

				ui.Scroll(c.Key("gallery-tag-scroll")).FillWidth().Grow(1).MinHeight(0).Scrollbars(true).Children(func() {
					ui.Column(c).FillWidth().Gap(14).Children(func() {
						if hasTarget {
							ui.Column(c).FillWidth().Gap(8).Children(func() {
								ui.Text(c, "当前标签（点击移除）").FontSize(12).TextColor(muted)
								if len(targetAsset.Tags) == 0 {
									ui.Text(c, "暂未添加标签").FontSize(12).TextColor(muted)
								} else {
									ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
										for _, tag := range targetAsset.Tags {
											tag := tag
											b := ui.ButtonBase(c.Key("gallery-rm-tag-"+tag)).Label("移除标签 "+tag).
												Height(28).Padding(0, 10).Radius(14).Gap(6).Justify(ui.Center).
												Background(tone.Alpha(.18)).TextColor(paper).Disabled(g.Busy)
											b.Children(func() {
												ui.Text(c, "#"+tag).FontSize(12).FontWeight(500).SingleLine()
												ui.Text(c, "×").FontSize(13).TextColor(tone)
											}).OnClick(func() {
												s.Dispatch(state.Event{Kind: state.GalleryRemoveTag, Message: tag, Gallery: state.GalleryPayload{AssetID: targetAsset.ID}})
											})
										}
									})
								}
							})
						}

						ui.Column(c).FillWidth().Gap(8).Children(func() {
							ui.Text(c, "已有标签（点击添加）").FontSize(12).TextColor(muted)
							if len(s.gallery.tags) == 0 {
								ui.Text(c, "尚未创建过任何标签。").FontSize(12).TextColor(muted)
							} else {
								ui.Row(c).FillWidth().Wrap().Gap(8).Children(func() {
									for _, t := range s.gallery.tags {
										t := t
										attached := hasTarget && slices.Contains(targetAsset.Tags, t.Name)
										b := ui.ButtonBase(c.Key("gallery-quick-tag-"+t.Name)).Label("添加标签 "+t.Name).
											Height(28).Padding(0, 10).Radius(14).Gap(5).Justify(ui.Center).
											Background(ui.RGBA(255, 255, 255, .06)).TextColor(paper).Disabled(g.Busy || attached)
										if attached {
											b.Background(ui.RGBA(255, 255, 255, .03)).TextColor(muted)
										} else if b.Hovered() || b.FocusVisible() {
											b.Background(ui.RGBA(255, 255, 255, .12))
										}
										b.Children(func() {
											ui.Text(c, fmt.Sprintf("#%s (%d)", t.Name, t.Count)).FontSize(12).SingleLine()
										}).OnClick(func() { submitTag(t.Name) })
									}
								})
							}
						})
					})
				})

				ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Grow(1).Children(func() {
						mono(c, "ESC", 10).Padding(2, 7).Radius(5).Background(ui.RGBA(255, 255, 255, .06)).Border(1, hairline).TextColor(muted)
						ui.Text(c, "关闭").FontSize(12).TextColor(muted).SingleLine()
					})
					s.gallerySmallButton(c, "gallery-tag-done", "完成", false).Padding(0, 18).OnClick(func() {
						s.Dispatch(state.Event{Kind: state.GalleryCloseTagModal})
					})
				})
			})
			if back.OverlayShortcut(0, ui.KeyEscape) || panel.PressedOutside() {
				s.Dispatch(state.Event{Kind: state.GalleryCloseTagModal})
			}
		})
	})
}

// galleryBatchDeleteArchiveConfirm renders the centered confirmation dialog
// before deleting Oddward-managed archive copies in batch.
func (s *Shell) galleryBatchDeleteArchiveConfirm(c *ui.Context) {
	if !s.Model.Gallery.ConfirmBatchDelete {
		return
	}
	archCount, reclaimBytes, _ := s.gallerySelectedArchivedStats()
	prefs := c.Preferences()
	opaque := prefs.HighContrast || prefs.ReduceTransparency
	backdrop := ink.Alpha(wishBackdropOpacity)
	tint := ui.RGBA(18, 19, 24, .92)
	if opaque {
		backdrop = ink
		tint = s.theme.Surface
	}

	ui.Overlay(c, func() {
		back := ui.Row(c.Key("gallery-batch-delete-backdrop")).Absolute().Left(0).Top(0).Right(0).Bottom(0).
			Justify(ui.Center).AlignItems(ui.Center).Padding(24).Background(backdrop).Modal()
		back.Children(func() {
			dlg := ui.Column(c.Key("gallery-batch-delete-confirm")).Width(wishConfirmWidth).
				Padding(wishConfirmPadding).Gap(14).Radius(sheetRadius).
				Border(1, ui.RGBA(255, 255, 255, .10)).
				Shadow(0, 20, 48, 0, ui.RGBA(0, 0, 0, .52)).
				Role(ui.RoleDialog).Label("批量删除归档副本")
			if opaque {
				dlg.Background(tint)
			} else {
				dlg.Material(frostedSurface{Tint: tint})
			}
			dlg.Children(func() {
				ui.Text(c, "批量删除 Oddward 归档副本").FontSize(wishTextDialog).FontWeight(700).TextColor(paper)
				summary := fmt.Sprintf("将删除已选中的 %d 项归档副本，预计释放 %s 空间。", archCount, formatGalleryBytes(reclaimBytes))
				if archCount == 0 {
					summary = "当前选中项里没有 Oddward 归档副本。"
				}
				ui.Text(c, summary).FontSize(14).FontWeight(500).TextColor(paper).LineHeight(1.5)
				ui.Text(c, "仅删除 Oddward 归档目录中保存的独立副本，绝不会修改或删除游戏目录、系统图片/视频目录或自定义文件夹中的原始文件。").
					FontSize(13).TextColor(muted).LineHeight(1.6)
				ui.Row(c).FillWidth().Gap(wishSpaceXL).Margin(4, 0, 0, 0).Children(func() {
					s.wishQuietButton(c, "gallery-batch-delete-cancel", "取消", false).Grow(1).AutoFocus().OnClick(func() {
						s.Dispatch(state.Event{Kind: state.GalleryCancelBatchDeleteArchive})
					})
					s.wishQuietButton(c, "gallery-batch-delete-accept", "删除归档副本", archCount == 0).Grow(1).OnClick(func() {
						s.Dispatch(state.Event{Kind: state.GalleryConfirmBatchDeleteArchive})
					})
				})
			})
			if back.OverlayShortcut(0, ui.KeyEscape) || dlg.PressedOutside() {
				s.Dispatch(state.Event{Kind: state.GalleryCancelBatchDeleteArchive})
			}
		})
	})
}
