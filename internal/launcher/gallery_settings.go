// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"fmt"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

const (
	gallerySettingsTabArchive = 0
	gallerySettingsTabSources = 1
	gallerySettingsTabCache   = 2
)

// gallerySettingsOverlay renders the centered frosted-glass settings modal
// aligned directly with Oddward's native launcherSettingsPage hierarchy:
// open two-column typography separated by simple 1px hairlines, without nested
// card borders, vertical callout bars or decorative badge clutter.
func (s *Shell) gallerySettingsOverlay(c *ui.Context) {
	g := s.Model.Gallery
	if !g.SettingsOpen {
		s.gallery.settingsTab = gallerySettingsTabArchive
		return
	}
	prefs := c.Preferences()
	opaque := prefs.HighContrast || prefs.ReduceTransparency
	shade := ui.RGBA(6, 7, 10, .48)
	tint := ui.RGBA(18, 19, 24, .84)
	if opaque {
		shade = ink
		tint = s.theme.Surface
	}
	tone := s.tone()
	w, h := c.Size()
	dialogW := min(gallerySettingsDialogWidth, max(w-64, 540))
	dialogH := min(gallerySettingsDialogHeight, max(h-64, 400))

	ui.Overlay(c, func() {
		back := ui.Row(c.Key("gallery-settings-backdrop")).Absolute().Left(0).Top(0).Right(0).Bottom(0).
			Justify(ui.Center).AlignItems(ui.Center).Padding(24).Background(shade).Modal()
		back.Children(func() {
			panel := ui.Column(c.Key("gallery-settings-panel")).Size(dialogW, dialogH).
				Padding(24, 28, 20, 28).Gap(18).
				Radius(sheetRadius).Clip().
				Border(1, ui.RGBA(255, 255, 255, .10)).
				Shadow(0, 20, 56, 0, ui.RGBA(0, 0, 0, .50)).
				Role(ui.RoleDialog).Label("相册设置")
			if opaque {
				panel.Background(tint)
			} else {
				panel.Material(frostedSurface{Tint: tint})
			}
			if s.motion() {
				entered := ui.Local(panel, "gallery-settings-entered", func() bool { return false })
				pose := ui.Pose{}
				if !*entered {
					pose, *entered = ui.Pose{Y: sheetEnterDistance}, true
					c.Invalidate()
				}
				panel.AnimatePose(pose, ui.Animation{Duration: 200 * time.Millisecond})
			}

			panel.Children(func() {
				if !opaque {
					ui.Box(c).Absolute().Left(-140).Top(-160).Size(360, 320).PassThrough().RadialGradient(ui.RadialGradient{
						Radius: 180,
						Stops:  []ui.GradientStop{{Color: tone.Alpha(.12), At: 0}, {Color: ui.Transparent, At: 1}},
					})
				}

				// Open two-column body matching launcherSettingsSections (no inner boxes, no vertical divider).
				ui.Row(c).FillWidth().Grow(1).MinHeight(0).Gap(28).AlignItems(ui.Start).Children(func() {
					// Left navigation column.
					ui.Column(c.Key("gallery-settings-nav")).Width(gallerySettingsNavWidth).FillHeight().Shrink(0).Gap(16).Children(func() {
						ui.Text(c, "相册设置").FontSize(22).FontWeight(700).TextColor(paper).SingleLine()

						ui.Column(c.Key("gallery-settings-tabs")).FillWidth().Gap(4).
							Role(ui.RoleTabList).Label("相册设置分类").Orientation(ui.Vertical).FocusGroup(ui.Vertical).Children(func() {
							tabs := []struct {
								id         int
								key, label string
								icon       string
							}{
								{gallerySettingsTabArchive, "gallery-set-tab-archive", "归档与备份", "\uE8F1"},
								{gallerySettingsTabSources, "gallery-set-tab-sources", "媒体来源", "\uE8B7"},
								{gallerySettingsTabCache, "gallery-set-tab-cache", "缓存与索引", "\uEDA2"},
							}
							for _, tab := range tabs {
								tab := tab
								selected := s.gallery.settingsTab == tab.id
								b := ui.ButtonBase(c.Key(tab.key)).Label(tab.label).Role(ui.RoleTab).Selected(selected).
									FillWidth().Height(38).Padding(0, 12).Gap(10).Radius(8).Justify(ui.Start).TextColor(muted)
								if selected {
									b.Background(tone.Alpha(.18)).TextColor(paper)
								} else if b.Hovered() || b.FocusVisible() {
									s.controlFill(b, ui.RGBA(255, 255, 255, .07))
									b.TextColor(paper)
								}
								b.Children(func() {
									glyph(c, tab.icon, 15)
									ui.Text(c, tab.label).FontSize(14).SingleLine()
								}).OnClick(func() {
									s.gallery.settingsTab = tab.id
								})
							}
						})

						ui.Box(c).Grow(1)

						// Plain typographic storage summary — no nested card border.
						ui.Column(c).FillWidth().Padding(0, 6).Gap(6).Children(func() {
							ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
								ui.Text(c, "已归档").FontSize(12).TextColor(muted).Grow(1)
								mono(c, formatGalleryBytes(g.ArchiveBytes), 12).TextColor(paper)
							})
							ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
								ui.Text(c, "缩略图").FontSize(12).TextColor(muted).Grow(1)
								mono(c, formatGalleryBytes(g.ThumbCacheBytes), 12).TextColor(paper)
							})
							if g.ArchiveFreeBytes > 0 {
								ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
									ui.Text(c, "磁盘剩余").FontSize(12).TextColor(muted).Grow(1)
									mono(c, formatGalleryBytes(g.ArchiveFreeBytes), 12).TextColor(muted)
								})
							}
						})
					})

					// Right flat scrollable content pane.
					ui.Column(c).Grow(1).MinWidth(0).FillHeight().Gap(12).Children(func() {
						// Quiet close button aligned to top-right.
						ui.Row(c).FillWidth().Height(28).AlignItems(ui.Center).Children(func() {
							ui.Box(c).Grow(1)
							iconButton(c, "gallery-settings-close", "关闭相册设置", "\uE8BB", 30, func() {
								s.Dispatch(state.Event{Kind: state.GalleryCloseSettings})
							})
						})

						ui.Scroll(c.Key("gallery-settings-scroll")).FillWidth().Grow(1).MinHeight(0).Scrollbars(true).Children(func() {
							ui.Column(c).FillWidth().Padding(0, 4, 8, 0).Gap(18).Children(func() {
								switch s.gallery.settingsTab {
								case gallerySettingsTabSources:
									s.gallerySettingsSourcesTab(c, g)
								case gallerySettingsTabCache:
									s.gallerySettingsCacheTab(c, g, tone)
								default:
									s.gallerySettingsArchiveTab(c, g, tone)
								}
							})
						})
					})
				})

				// Minimal footer strip.
				ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
					ui.Row(c).Gap(8).AlignItems(ui.Center).Grow(1).Children(func() {
						mono(c, "ESC", 10).Padding(2, 7).Radius(5).Background(ui.RGBA(255, 255, 255, .06)).Border(1, hairline).TextColor(muted)
						ui.Text(c, "关闭相册设置").FontSize(12).TextColor(muted).SingleLine()
					})
					s.gallerySmallButton(c, "gallery-settings-done", "完成", false).Padding(0, 18).OnClick(func() {
						s.Dispatch(state.Event{Kind: state.GalleryCloseSettings})
					})
				})
			})
			if back.OverlayShortcut(0, ui.KeyEscape) || panel.PressedOutside() {
				s.Dispatch(state.Event{Kind: state.GalleryCloseSettings})
			}
		})
	})
}

func (s *Shell) gallerySettingsArchiveTab(c *ui.Context, g state.GalleryState, tone ui.Color) {
	// Section 1: Auto-archive toggle — flat typography matching launcherSettingsPage.
	ui.Column(c.Key("gallery-sec-auto-archive")).FillWidth().Gap(8).Children(func() {
		ui.Row(c).FillWidth().Height(36).AlignItems(ui.Center).Children(func() {
			caption := ui.Text(c, "自动归档游戏内拍照").FontSize(15).FontWeight(500).TextColor(paper).Grow(1)
			s.preferenceSwitchFor(c, "gallery-auto-archive", caption, g.AutoArchive, g.Busy, func(on bool) state.Event {
				return state.Event{Kind: state.GalleryToggleAutoArchive, Enabled: on}
			})
		})
		ui.Text(c, "默认关闭。开启后会将游戏拍照和录像缓存独立复制到归档目录，防止游戏清理缓存导致作品丢失；不提升画质，也不删除源文件。Oddward 关闭期间无法监听新拍摄的媒体。").
			FontSize(13).TextColor(muted).LineHeight(1.6)
	})

	ui.Box(c).FillWidth().Height(1).Background(hairline)

	// Section 2: Archive directory & photo/video storage breakdown.
	ui.Column(c.Key("gallery-sec-archive-dir")).FillWidth().Gap(10).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
			ui.Text(c, "归档目录").FontSize(15).FontWeight(500).TextColor(paper).Grow(1)
			mono(c, fmt.Sprintf("%d 项 · %s", g.ArchivedCount, formatGalleryBytes(g.ArchiveBytes)), 12).TextColor(muted)
		})
		archivePath := g.EffectiveArchive
		if archivePath == "" {
			archivePath = "默认（系统图片目录\\Oddward\\Gallery）"
		}
		ui.Text(c, archivePath).Font(monoFont).FontSize(12).TextColor(muted).SingleLine().Tooltip(archivePath)

		// Slim 4px proportional breakdown bar without extra container frames.
		total := g.ArchivePhotoBytes + g.ArchiveVideoBytes
		photoPct, videoPct := float32(0), float32(0)
		if total > 0 {
			photoPct = float32(g.ArchivePhotoBytes) / float32(total) * 100
			videoPct = float32(g.ArchiveVideoBytes) / float32(total) * 100
		}
		ui.Row(c).FillWidth().Height(4).Radius(2).Background(ui.RGBA(255, 255, 255, .08)).Clip().Gap(1).Children(func() {
			if photoPct > 0 {
				ui.Box(c).WidthPercent(max(photoPct, 2)).FillHeight().Background(tone)
			}
			if videoPct > 0 {
				ui.Box(c).WidthPercent(max(videoPct, 2)).FillHeight().Background(wishWarningCool)
			}
		})
		ui.Row(c).FillWidth().Gap(14).AlignItems(ui.Center).Children(func() {
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(6, 6).Radius(3).Background(tone)
				ui.Text(c, fmt.Sprintf("照片 %d 张 (%s)", g.ArchivePhotoCount, formatGalleryBytes(g.ArchivePhotoBytes))).FontSize(12).TextColor(muted).SingleLine()
			})
			ui.Row(c).Gap(6).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Size(6, 6).Radius(3).Background(wishWarningCool)
				ui.Text(c, fmt.Sprintf("视频 %d 段 (%s)", g.ArchiveVideoCount, formatGalleryBytes(g.ArchiveVideoBytes))).FontSize(12).TextColor(muted).SingleLine()
			})
		})

		ui.Row(c).FillWidth().Gap(8).Margin(4, 0, 0, 0).AlignItems(ui.Center).Children(func() {
			s.gallerySmallButton(c, "gallery-pick-archive-dir", "更改目录", g.Busy || s.PickGalleryDirectory == nil).OnClick(func() {
				s.Dispatch(state.Event{Kind: state.GalleryPickArchiveDir})
			})
			s.gallerySmallButton(c, "gallery-open-archive-dir", "打开目录", s.OpenFolder == nil || g.EffectiveArchive == "").OnClick(func() {
				s.Dispatch(state.Event{Kind: state.GalleryOpenFolder, Path: g.EffectiveArchive})
			})
			ui.Box(c).Grow(1)
			s.gallerySmallButton(c, "gallery-archive-existing", "归档已有媒体", g.Busy || g.CachedCount == 0).OnClick(func() {
				s.Dispatch(state.Event{Kind: state.GalleryArchiveExisting})
			})
		})
	})
}

func (s *Shell) gallerySettingsSourcesTab(c *ui.Context, g state.GalleryState) {
	ui.Column(c.Key("gallery-sec-auto-discover")).FillWidth().Gap(8).Children(func() {
		ui.Row(c).FillWidth().Height(36).AlignItems(ui.Center).Children(func() {
			caption := ui.Text(c, "自动发现游戏缓存").FontSize(15).FontWeight(500).TextColor(paper).Grow(1)
			s.preferenceSwitchFor(c, "gallery-auto-discover", caption, g.AutoDiscoverGame, g.Busy, func(on bool) state.Event {
				return state.Event{Kind: state.GalleryToggleAutoDiscover, Enabled: on}
			})
		})
		ui.Text(c, "从已定位的《异环》安装目录只读探测 Selfie 下的 ScreenShots（拍照）与 FFMpeg（录像）目录，绝不修改或删除游戏文件。系统图片与视频目录下的 NevernessToEverness 导出目录也会自动识别。").
			FontSize(13).TextColor(muted).LineHeight(1.6)
	})

	ui.Box(c).FillWidth().Height(1).Background(hairline)

	ui.Column(c.Key("gallery-sec-custom-sources")).FillWidth().Gap(10).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
			ui.Text(c, "自定义媒体来源").FontSize(15).FontWeight(500).TextColor(paper).Grow(1)
			s.gallerySmallButton(c, "gallery-add-source", "添加文件夹", g.Busy || s.PickGalleryDirectory == nil).OnClick(func() {
				s.Dispatch(state.Event{Kind: state.GalleryAddCustomSource})
			})
		})
		customCount := 0
		for _, src := range s.gallery.sources {
			if src.SourceType != GallerySourceCustom {
				continue
			}
			customCount++
			src := src
			if customCount > 1 {
				ui.Box(c).FillWidth().Height(1).Background(ui.RGBA(255, 255, 255, .06))
			}
			ui.Row(c.Key(fmt.Sprintf("gallery-custom-src-%d", src.ID))).FillWidth().Height(36).AlignItems(ui.Center).Gap(10).Children(func() {
				ui.Text(c, src.RootPath).Font(monoFont).FontSize(12).TextColor(paper).SingleLine().Grow(1).Shrink(1).Tooltip(src.RootPath)
				s.gallerySmallButton(c, fmt.Sprintf("gallery-remove-src-%d", src.ID), "移除", g.Busy).Height(28).OnClick(func() {
					s.Dispatch(state.Event{Kind: state.GalleryRemoveCustomSource, Gallery: state.GalleryPayload{SourceID: src.ID}})
				})
			})
		}
		if customCount == 0 {
			ui.Text(c, "尚未添加自定义文件夹。移除自定义目录仅取消相册索引，不会删除你的本地文件。").
				FontSize(13).TextColor(muted).LineHeight(1.6)
		}
	})
}

func (s *Shell) gallerySettingsCacheTab(c *ui.Context, g state.GalleryState, tone ui.Color) {
	ui.Column(c.Key("gallery-sec-thumb-cache")).FillWidth().Gap(10).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
			ui.Text(c, "缩略图磁盘缓存").FontSize(15).FontWeight(500).TextColor(paper).Grow(1)
			mono(c, fmt.Sprintf("%s / %d MB", formatGalleryBytes(g.ThumbCacheBytes), g.ThumbCacheMB), 12).TextColor(muted)
		})
		budgetBytes := int64(max(g.ThumbCacheMB, 1)) << 20
		ratio := clampFloat32(float32(g.ThumbCacheBytes)/float32(budgetBytes), 0, 1)
		ui.Box(c).FillWidth().Height(4).Radius(2).Background(ui.RGBA(255, 255, 255, .08)).Clip().Children(func() {
			if ratio > 0 {
				ui.Box(c).WidthPercent(max(ratio*100, 2)).FillHeight().Radius(2).Background(tone)
			}
		})
		ui.Text(c, "缓存仅保存已解码缩略图以加快浏览加载，超额时自动清理最久未访问条目，绝不影响原始文件与归档副本。").
			FontSize(13).TextColor(muted).LineHeight(1.6)

		ui.Row(c).FillWidth().Gap(8).Children(func() {
			for _, mb := range []int{128, 256, 512, 1024} {
				mb := mb
				selected := g.ThumbCacheMB == mb
				b := ui.ButtonBase(c.Key(fmt.Sprintf("gallery-thumb-mb-%d", mb))).Label(fmt.Sprintf("%d MB", mb)).
					Grow(1).Height(32).Radius(8).Justify(ui.Center).Disabled(g.Busy).TextColor(muted)
				if selected {
					b.Background(tone.Alpha(.18)).TextColor(paper)
				} else if b.Hovered() || b.FocusVisible() {
					b.Background(ui.RGBA(255, 255, 255, .07)).TextColor(paper)
				} else {
					b.Background(ui.RGBA(255, 255, 255, .04))
				}
				b.Children(func() {
					mono(c, fmt.Sprintf("%d MB", mb), 12)
				}).OnClick(func() {
					s.Dispatch(state.Event{Kind: state.GallerySetThumbCacheMB, Gallery: state.GalleryPayload{ThumbCacheMB: mb}})
				})
			}
		})
	})

	ui.Box(c).FillWidth().Height(1).Background(hairline)

	ui.Column(c.Key("gallery-sec-maintenance")).FillWidth().Gap(10).Children(func() {
		ui.Text(c, "缓存与索引维护").FontSize(15).FontWeight(500).TextColor(paper)
		ui.Text(c, "重建索引会保留收藏、自定义相册、标签和归档副本；清理失效索引仅移除源文件已消失且未归档的数据库条目，绝不删除本地文件。").
			FontSize(13).TextColor(muted).LineHeight(1.6)
		ui.Row(c).FillWidth().Gap(8).Margin(2, 0, 0, 0).Children(func() {
			s.gallerySmallButton(c, "gallery-clear-thumbs", "清理缩略图缓存", g.Busy).OnClick(func() {
				s.Dispatch(state.Event{Kind: state.GalleryClearThumbCache})
			})
			s.gallerySmallButton(c, "gallery-prune-missing", fmt.Sprintf("清理失效索引 (%d)", g.MissingCount), g.Busy || g.MissingCount == 0).OnClick(func() {
				s.Dispatch(state.Event{Kind: state.GalleryPruneMissing})
			})
			s.gallerySmallButton(c, "gallery-rebuild-index", "重新建立索引", g.Busy).OnClick(func() {
				s.Dispatch(state.Event{Kind: state.GalleryRebuildIndex})
			})
		})
	})
}

func (s *Shell) galleryDeleteArchiveConfirm(c *ui.Context) {
	id := s.Model.Gallery.ConfirmDeleteID
	if id <= 0 {
		return
	}
	prefs := c.Preferences()
	backdrop := ink.Alpha(wishBackdropOpacity)
	if prefs.HighContrast || prefs.ReduceTransparency {
		backdrop = ink
	}
	ui.Overlay(c, func() {
		back := ui.Box(c.Key("gallery-delete-backdrop")).Fill().Background(backdrop).Modal()
		back.Children(func() {
			ui.Column(c.Key("gallery-delete-confirm")).Absolute().LeftPercent(50).TopPercent(50).
				Translate(-wishConfirmWidth/2, -wishConfirmOffsetY).Width(wishConfirmWidth).
				Padding(wishConfirmPadding).Gap(wishSpaceHero).Radius(sheetRadius).
				Background(s.theme.Surface).Border(1, ui.RGBA(255, 255, 255, .10)).
				Shadow(0, 20, 48, 0, ui.RGBA(0, 0, 0, .52)).
				Role(ui.RoleDialog).Label("删除归档副本").Children(func() {
				ui.Text(c, "删除 Oddward 归档副本").FontSize(wishTextDialog).FontWeight(700)
				ui.Text(c, "仅删除 Oddward 归档目录中保存的独立副本，绝不会修改或删除游戏目录或系统图片目录中的原始文件。").
					FontSize(wishTextHelp).TextColor(muted).LineHeight(wishBodyLineHeight)
				ui.Row(c).FillWidth().Gap(wishSpaceXL).Children(func() {
					s.wishQuietButton(c, "gallery-delete-cancel", "取消", false).Grow(1).AutoFocus().OnClick(func() {
						s.Dispatch(state.Event{Kind: state.GalleryCancelDeleteArchive})
					})
					s.wishQuietButton(c, "gallery-delete-accept", "删除归档副本", false).Grow(1).OnClick(func() {
						s.Dispatch(state.Event{Kind: state.GalleryConfirmDeleteArchive})
					})
				})
			})
		})
		if back.OverlayShortcut(0, ui.KeyEscape) {
			s.Dispatch(state.Event{Kind: state.GalleryCancelDeleteArchive})
		}
	})
}
