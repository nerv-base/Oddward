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

func (s *Shell) gallerySettingsOverlay(c *ui.Context) {
	g := s.Model.Gallery
	if !g.SettingsOpen {
		s.gallery.settingsTab = gallerySettingsTabArchive
		return
	}
	prefs := c.Preferences()
	opaque := prefs.HighContrast || prefs.ReduceTransparency
	shade := ui.RGBA(6, 7, 10, .62)
	tint := ui.RGBA(16, 18, 24, .90)
	if opaque {
		shade = ink
		tint = s.theme.Surface
	}
	tone := s.tone()
	w, h := c.Size()
	dialogW := min(gallerySettingsDialogWidth, max(w-64, 560))
	dialogH := min(gallerySettingsDialogHeight, max(h-64, 420))

	ui.Overlay(c, func() {
		back := ui.Row(c.Key("gallery-settings-backdrop")).Absolute().Left(0).Top(0).Right(0).Bottom(0).
			Justify(ui.Center).AlignItems(ui.Center).Padding(24).Background(shade).Modal()
		back.Children(func() {
			panel := ui.Column(c.Key("gallery-settings-panel")).Size(dialogW, dialogH).
				Radius(sheetRadius).Clip().
				Border(1, ui.RGBA(255, 255, 255, .14)).
				Shadow(0, 24, 64, 0, ui.RGBA(0, 0, 0, .58)).
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
					// Subtle artwork-derived corner ambient glow + top specular highlight.
					ui.Box(c).Absolute().Left(-120).Top(-140).Size(420, 320).PassThrough().RadialGradient(ui.RadialGradient{
						Radius: 210,
						Stops:  []ui.GradientStop{{Color: tone.Alpha(.13), At: 0}, {Color: ui.Transparent, At: 1}},
					})
					ui.Box(c).Absolute().Left(24).Right(24).Top(0).Height(1).Background(ui.RGBA(255, 255, 255, .18)).PassThrough()
				}

				// Refined dialog header.
				ui.Row(c).FillWidth().Padding(20, 24, 16, 24).AlignItems(ui.Center).Gap(12).Children(func() {
					ui.Column(c).Grow(1).Gap(3).Children(func() {
						ui.Text(c, "相册设置").FontSize(20).FontWeight(700).TextColor(paper).SingleLine()
						ui.Text(c, "管理本地照片归档策略、媒体目录与缩略图缓存").FontSize(12).TextColor(muted).SingleLine()
					})
					iconButton(c, "gallery-settings-close", "关闭相册设置", "\uE8BB", 32, func() {
						s.Dispatch(state.Event{Kind: state.GalleryCloseSettings})
					})
				})
				ui.Box(c).FillWidth().Height(1).Background(ui.RGBA(255, 255, 255, .08))

				// 2-pane centered settings body: left category rail + right tactile cards.
				ui.Row(c).FillWidth().Grow(1).MinHeight(0).Children(func() {
					// Left rail.
					railBg := ui.RGBA(10, 12, 16, .34)
					if opaque {
						railBg = s.theme.Surface
					}
					ui.Column(c.Key("gallery-settings-nav")).Width(gallerySettingsNavWidth).FillHeight().Shrink(0).
						Padding(16, 12, 16, 14).Gap(6).Background(railBg).Children(func() {
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
									FillWidth().Height(38).Padding(0, 12).Gap(10).Radius(10).Justify(ui.Start).TextColor(muted)
								if selected {
									if opaque {
										b.Background(tone.Alpha(.24)).Border(1, tone).TextColor(paper)
									} else {
										b.Background(tone.Alpha(.14)).Border(1, tone.Alpha(.34)).TextColor(paper)
									}
								} else if b.Hovered() || b.FocusVisible() {
									b.Background(ui.RGBA(255, 255, 255, .06)).TextColor(paper)
								}
								b.Children(func() {
									if selected {
										ui.Box(c).Absolute().Left(3).Top(9).Bottom(9).Width(3).Radius(1.5).Background(tone)
									}
									iconColor := muted
									if selected {
										iconColor = tone
									}
									ui.Box(c).TextColor(iconColor).Children(func() { glyph(c, tab.icon, 15) })
									ui.Text(c, tab.label).FontSize(13).FontWeight(600).SingleLine()
								}).OnClick(func() {
									s.gallery.settingsTab = tab.id
								})
							}
						})

						ui.Box(c).Grow(1)

						// Compact storage overview instrument card at rail bottom.
						ui.Column(c).FillWidth().Padding(11, 12).Gap(8).Radius(12).
							Background(ui.RGBA(255, 255, 255, .03)).Border(1, ui.RGBA(255, 255, 255, .07)).Children(func() {
							ui.Text(c, "存储概览").FontSize(11).FontWeight(600).TextColor(muted).SingleLine()
							ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
								ui.Text(c, "已归档").FontSize(11).TextColor(muted).Grow(1)
								mono(c, formatGalleryBytes(g.ArchiveBytes), 11).TextColor(paper)
							})
							ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
								ui.Text(c, "缩略图").FontSize(11).TextColor(muted).Grow(1)
								mono(c, formatGalleryBytes(g.ThumbCacheBytes), 11).TextColor(paper)
							})
						})
					})

					ui.Box(c).Width(1).FillHeight().Background(ui.RGBA(255, 255, 255, .08))

					// Right content pane.
					ui.Scroll(c.Key("gallery-settings-scroll")).Grow(1).MinWidth(0).FillHeight().Scrollbars(true).Children(func() {
						ui.Column(c).FillWidth().Padding(18, 22, 18, 22).Gap(14).Children(func() {
							switch s.gallery.settingsTab {
							case gallerySettingsTabSources:
								s.gallerySettingsSourcesTab(c, g, tone, opaque)
							case gallerySettingsTabCache:
								s.gallerySettingsCacheTab(c, g, tone, opaque)
							default:
								s.gallerySettingsArchiveTab(c, g, tone, opaque)
							}
						})
					})
				})

				ui.Box(c).FillWidth().Height(1).Background(ui.RGBA(255, 255, 255, .08))

				// Dialog footer strip.
				ui.Row(c).FillWidth().Padding(12, 22).AlignItems(ui.Center).Children(func() {
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

func (s *Shell) gallerySettingsCard(c *ui.Context, key string, opaque bool, body func()) {
	bg := ui.RGBA(255, 255, 255, .035)
	border := ui.RGBA(255, 255, 255, .08)
	if opaque {
		bg = s.theme.Surface
		border = hairline
	}
	ui.Column(c.Key(key)).FillWidth().Padding(16, 18).Gap(12).Radius(gallerySettingsCardRadius).
		Background(bg).Border(1, border).Children(body)
}

func (s *Shell) gallerySettingsArchiveTab(c *ui.Context, g state.GalleryState, tone ui.Color, opaque bool) {
	// Card 1: Auto-archive policy & honest disclosure.
	s.gallerySettingsCard(c, "gallery-card-auto-archive", opaque, func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(10).Children(func() {
			caption := ui.Text(c, "自动归档游戏内拍照").FontSize(15).FontWeight(600).TextColor(paper).SingleLine()
			badgeText, badgeBg, badgeFg := "默认关闭", ui.RGBA(255, 255, 255, .07), muted
			if g.AutoArchive {
				badgeText, badgeBg, badgeFg = "已开启", tone.Alpha(.18), tone
			}
			ui.Row(c).Height(22).Padding(0, 8).Radius(6).AlignItems(ui.Center).Background(badgeBg).Children(func() {
				ui.Text(c, badgeText).FontSize(11).FontWeight(600).TextColor(badgeFg).SingleLine()
			})
			ui.Box(c).Grow(1)
			s.preferenceSwitchFor(c, "gallery-auto-archive", caption, g.AutoArchive, g.Busy, func(on bool) state.Event {
				return state.Event{Kind: state.GalleryToggleAutoArchive, Enabled: on}
			})
		})
		wellBg := ui.RGBA(0, 0, 0, .24)
		if opaque {
			wellBg = ink
		}
		ui.Row(c).FillWidth().Padding(11, 13).Gap(10).Radius(10).Background(wellBg).Border(1, ui.RGBA(255, 255, 255, .06)).Children(func() {
			ui.Box(c).Width(3).FillHeight().Radius(1.5).Background(tone.Alpha(.65))
			ui.Text(c, "默认关闭。开启后会将游戏拍照缓存的原始字节独立复制到归档目录，防止游戏清理缓存导致作品丢失。归档副本会占用相应磁盘空间，不提升源图画质也不删除源文件；Oddward 关闭期间无法监听新拍摄的照片。").
				FontSize(12).TextColor(muted).LineHeight(1.65).Grow(1).Shrink(1)
		})
	})

	// Card 2: Archive location & manual batch archive.
	s.gallerySettingsCard(c, "gallery-card-archive-dir", opaque, func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
			ui.Text(c, "归档目录").FontSize(15).FontWeight(600).TextColor(paper).Grow(1)
			mono(c, fmt.Sprintf("%d 张 · %s", g.ArchivedCount, formatGalleryBytes(g.ArchiveBytes)), 12).TextColor(muted)
		})
		archivePath := g.EffectiveArchive
		if archivePath == "" {
			archivePath = "默认（系统图片目录\\Oddward\\Gallery）"
		}
		wellBg := ui.RGBA(0, 0, 0, .28)
		if opaque {
			wellBg = ink
		}
		ui.Row(c).FillWidth().Padding(9, 12).Radius(9).AlignItems(ui.Center).Gap(8).
			Background(wellBg).Border(1, ui.RGBA(255, 255, 255, .07)).Children(func() {
			ui.Box(c).TextColor(muted).Children(func() { glyph(c, "\uE8B7", 14) })
			ui.Text(c, archivePath).Font(monoFont).FontSize(12).TextColor(paper).SingleLine().Grow(1).Shrink(1).Tooltip(archivePath)
		})
		ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
			s.gallerySmallButton(c, "gallery-pick-archive-dir", "更改目录", g.Busy || s.PickGalleryDirectory == nil).OnClick(func() {
				s.Dispatch(state.Event{Kind: state.GalleryPickArchiveDir})
			})
			s.gallerySmallButton(c, "gallery-open-archive-dir", "打开目录", s.OpenFolder == nil || g.EffectiveArchive == "").OnClick(func() {
				s.Dispatch(state.Event{Kind: state.GalleryOpenFolder, Path: g.EffectiveArchive})
			})
			ui.Box(c).Grow(1)
			s.gallerySmallButton(c, "gallery-archive-existing", "归档已有照片", g.Busy || g.CachedCount == 0).OnClick(func() {
				s.Dispatch(state.Event{Kind: state.GalleryArchiveExisting})
			})
		})
	})
}

func (s *Shell) gallerySettingsSourcesTab(c *ui.Context, g state.GalleryState, tone ui.Color, opaque bool) {
	// Card 1: Built-in read-only game cache & Pictures discovery.
	s.gallerySettingsCard(c, "gallery-card-auto-discover", opaque, func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Gap(10).Children(func() {
			caption := ui.Text(c, "自动发现游戏缓存").FontSize(15).FontWeight(600).TextColor(paper).SingleLine()
			ui.Row(c).Height(22).Padding(0, 8).Radius(6).AlignItems(ui.Center).Background(tone.Alpha(.14)).Children(func() {
				ui.Text(c, "只读探测").FontSize(11).FontWeight(600).TextColor(tone).SingleLine()
			})
			ui.Box(c).Grow(1)
			s.preferenceSwitchFor(c, "gallery-auto-discover", caption, g.AutoDiscoverGame, g.Busy, func(on bool) state.Event {
				return state.Event{Kind: state.GalleryToggleAutoDiscover, Enabled: on}
			})
		})
		ui.Text(c, "从已定位的《异环》安装目录只读探测 Selfie 拍照目录，绝不修改或删除游戏文件。系统图片目录下的 NevernessToEverness 导出目录也会自动识别。").
			FontSize(12).TextColor(muted).LineHeight(1.6)
	})

	// Card 2: Custom folders.
	s.gallerySettingsCard(c, "gallery-card-custom-sources", opaque, func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
			ui.Text(c, "自定义媒体来源").FontSize(15).FontWeight(600).TextColor(paper).Grow(1)
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
			ui.Row(c.Key(fmt.Sprintf("gallery-custom-src-%d", src.ID))).FillWidth().AlignItems(ui.Center).Gap(10).
				Padding(8, 12).Radius(9).Background(ui.RGBA(0, 0, 0, .24)).Border(1, ui.RGBA(255, 255, 255, .07)).Children(func() {
				ui.Box(c).TextColor(muted).Children(func() { glyph(c, "\uE8B7", 14) })
				ui.Text(c, src.RootPath).Font(monoFont).FontSize(12).TextColor(paper).SingleLine().Grow(1).Shrink(1).Tooltip(src.RootPath)
				s.gallerySmallButton(c, fmt.Sprintf("gallery-remove-src-%d", src.ID), "移除", g.Busy).Height(28).OnClick(func() {
					s.Dispatch(state.Event{Kind: state.GalleryRemoveCustomSource, Gallery: state.GalleryPayload{SourceID: src.ID}})
				})
			})
		}
		if customCount == 0 {
			ui.Text(c, "尚未添加自定义文件夹。移除自定义目录仅取消相册索引，不会删除你的本地照片。").
				FontSize(12).TextColor(muted).LineHeight(1.6)
		}
	})
}

func (s *Shell) gallerySettingsCacheTab(c *ui.Context, g state.GalleryState, tone ui.Color, opaque bool) {
	// Card 1: Thumbnail cache budget + usage meter.
	s.gallerySettingsCard(c, "gallery-card-thumb-cache", opaque, func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Children(func() {
			ui.Text(c, "缩略图磁盘缓存").FontSize(15).FontWeight(600).TextColor(paper).Grow(1)
			mono(c, fmt.Sprintf("%s / %d MB", formatGalleryBytes(g.ThumbCacheBytes), g.ThumbCacheMB), 12).TextColor(paper)
		})
		budgetBytes := int64(max(g.ThumbCacheMB, 1)) << 20
		ratio := float32(g.ThumbCacheBytes) / float32(budgetBytes)
		if ratio < 0 {
			ratio = 0
		}
		if ratio > 1 {
			ratio = 1
		}
		ui.Box(c).FillWidth().Height(6).Radius(3).Background(ui.RGBA(255, 255, 255, .08)).Clip().Children(func() {
			if ratio > 0 {
				ui.Box(c).WidthPercent(max(ratio*100, 2)).FillHeight().Radius(3).Background(tone)
			}
		})
		ui.Text(c, "缓存仅保存已解码缩略图以加快滚动加载，超额时自动清理最久未访问条目，绝不影响原始照片与归档副本。").
			FontSize(12).TextColor(muted).LineHeight(1.6)

		// Segmented budget selector inside recessed track.
		ui.Row(c).FillWidth().Padding(4).Gap(4).Radius(10).Background(ui.RGBA(0, 0, 0, .28)).Border(1, ui.RGBA(255, 255, 255, .06)).Children(func() {
			for _, mb := range []int{128, 256, 512, 1024} {
				mb := mb
				selected := g.ThumbCacheMB == mb
				b := ui.ButtonBase(c.Key(fmt.Sprintf("gallery-thumb-mb-%d", mb))).Label(fmt.Sprintf("%d MB", mb)).
					Grow(1).Height(30).Radius(7).Justify(ui.Center).Disabled(g.Busy).TextColor(muted)
				if selected {
					b.Background(tone.Alpha(.20)).Border(1, tone.Alpha(.65)).TextColor(paper)
				} else if b.Hovered() || b.FocusVisible() {
					b.Background(ui.RGBA(255, 255, 255, .06)).TextColor(paper)
				}
				b.Children(func() {
					mono(c, fmt.Sprintf("%d MB", mb), 12)
				}).OnClick(func() {
					s.Dispatch(state.Event{Kind: state.GallerySetThumbCacheMB, Gallery: state.GalleryPayload{ThumbCacheMB: mb}})
				})
			}
		})
	})

	// Card 2: Cache & index maintenance.
	s.gallerySettingsCard(c, "gallery-card-maintenance", opaque, func() {
		ui.Text(c, "缓存与索引维护").FontSize(15).FontWeight(600).TextColor(paper)
		ui.Text(c, "若缩略图异常或手动整理过外部目录，可随时清理缓存或重新建立本地索引。").
			FontSize(12).TextColor(muted).LineHeight(1.6)
		ui.Row(c).FillWidth().Gap(10).Children(func() {
			s.gallerySmallButton(c, "gallery-clear-thumbs", "清理缩略图缓存", g.Busy).OnClick(func() {
				s.Dispatch(state.Event{Kind: state.GalleryClearThumbCache})
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
				Background(s.theme.Surface).Border(1, ui.RGBA(255, 255, 255, .14)).
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
