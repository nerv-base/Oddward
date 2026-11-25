// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

const (
	galleryZoomMin  float32 = 0.1
	galleryZoomMax  float32 = 8.0
	galleryZoomStep float32 = 1.2
)

func (s *Shell) galleryViewerOverlay(c *ui.Context) {
	g := s.Model.Gallery
	if !g.ViewerOpen || g.ViewerID <= 0 {
		return
	}
	asset, ok := s.galleryAssetByID(g.ViewerID)
	if !ok {
		return
	}
	idx := s.galleryAssetIndex(asset.ID)
	total := len(s.gallery.assets)
	v := &s.gallery.viewer
	if v.assetID != asset.ID {
		v.reset(asset.ID)
	}
	if asset.IsVideo() {
		s.ensureGalleryVideo(asset)
	} else if v.videoPlayer != nil || v.videoAssetID != 0 {
		s.closeGalleryVideo()
	}

	prefs := c.Preferences()
	opaque := prefs.HighContrast || prefs.ReduceTransparency
	backdrop := ui.RGBA(6, 7, 10, .96)
	barBg := ui.RGBA(14, 16, 22, .86)
	if opaque {
		backdrop = ink
		barBg = s.theme.Surface
	}
	tone := s.tone()

	var bmp *ui.Bitmap
	var isFull, loading bool
	var loadErr string
	if !asset.IsVideo() {
		bmp, isFull, loading, loadErr = s.galleryViewerBitmap(asset.ID)
	} else {
		bmp = s.galleryThumb(asset, galleryThumbEdge(s.gallery.dpiScale))
	}

	// While an organization modal sits on top of the viewer, pause viewer
	// shortcuts so Enter/Space/Escape go to the foreground dialog.
	subModalOpen := g.AlbumModalOpen || g.TagModalOpen || g.ConfirmDeleteID != 0

	dialogLabel := "图片查看器：" + asset.FileName
	if asset.IsVideo() {
		dialogLabel = "视频播放器：" + asset.FileName
	}

	ui.Overlay(c, func() {
		back := ui.Box(c.Key("gallery-viewer-backdrop")).Fill().Background(backdrop).Modal().
			Role(ui.RoleDialog).Label(dialogLabel)

		// Evaluate every OverlayShortcut unconditionally so MyGo keeps all
		// registrations active across frames.
		esc := back.OverlayShortcut(0, ui.KeyEscape)
		prev := back.OverlayShortcut(0, ui.KeyLeft)
		next := back.OverlayShortcut(0, ui.KeyRight)
		copyImg := back.OverlayShortcut(ui.Ctrl, ui.KeyC)
		exportImg := back.OverlayShortcut(ui.Ctrl, ui.KeyS)
		fitKey := back.OverlayShortcut(0, ui.Key0)
		actualKey := back.OverlayShortcut(0, ui.Key1)
		zoomInKey := back.OverlayShortcut(0, ui.KeyEqual)
		zoomOutKey := back.OverlayShortcut(0, ui.KeyMinus)
		spaceKey := back.OverlayShortcut(0, ui.KeySpace)
		muteKey := back.OverlayShortcut(0, ui.KeyM)

		if !subModalOpen {
			switch {
			case esc:
				s.closeGalleryVideo()
				s.Dispatch(state.Event{Kind: state.GalleryCloseViewer})
				return
			case prev:
				s.Dispatch(state.Event{Kind: state.GalleryViewerStep, Gallery: state.GalleryPayload{Step: -1}})
			case next:
				s.Dispatch(state.Event{Kind: state.GalleryViewerStep, Gallery: state.GalleryPayload{Step: 1}})
			case copyImg:
				if !asset.IsVideo() {
					s.Dispatch(state.Event{Kind: state.GalleryCopyImage, Gallery: state.GalleryPayload{AssetID: asset.ID}})
				}
			case exportImg:
				s.Dispatch(state.Event{Kind: state.GalleryExportSingle, Gallery: state.GalleryPayload{AssetID: asset.ID}})
			case spaceKey && asset.IsVideo():
				if p := v.videoPlayer; p != nil {
					if p.Playing() {
						p.Pause()
					} else {
						if p.Ended() {
							p.Seek(0)
						}
						p.Play()
					}
				}
			case muteKey && asset.IsVideo():
				v.videoMuted = !v.videoMuted
				if p := v.videoPlayer; p != nil {
					p.SetMuted(v.videoMuted)
				}
			case fitKey && !asset.IsVideo():
				v.fit, v.panX, v.panY = true, 0, 0
			case actualKey && !asset.IsVideo():
				v.fit, v.zoom, v.panX, v.panY = false, 1, 0, 0
			case zoomInKey && !asset.IsVideo():
				galleryAdjustZoom(v, galleryZoomStep)
			case zoomOutKey && !asset.IsVideo():
				galleryAdjustZoom(v, 1/galleryZoomStep)
			}
		}

		back.Children(func() {
			if asset.IsVideo() {
				s.galleryVideoStage(c, asset, bmp, barBg, tone, opaque)
			} else {
				s.galleryImageStage(c, asset, bmp, loadErr)
			}

			// Floating Left / Right navigation buttons.
			if idx > 0 {
				ui.Box(c).Absolute().Left(16).TopPercent(50).Translate(0, -22).Children(func() {
					b := ui.ButtonBase(c.Key("gallery-viewer-prev")).Label("上一项").Tooltip("上一项 (Left)").
						Size(44, 44).Radius(22).Justify(ui.Center).Background(barBg).Border(1, ui.RGBA(255, 255, 255, .12)).TextColor(paper)
					if b.Hovered() || b.FocusVisible() {
						b.Border(1, tone)
					}
					b.Children(func() { glyph(c, "\uE72B", 18) }).OnClick(func() {
						s.Dispatch(state.Event{Kind: state.GalleryViewerStep, Gallery: state.GalleryPayload{Step: -1}})
					})
				})
			}
			if idx >= 0 && idx+1 < total {
				ui.Box(c).Absolute().Right(16).TopPercent(50).Translate(0, -22).Children(func() {
					b := ui.ButtonBase(c.Key("gallery-viewer-next")).Label("下一项").Tooltip("下一项 (Right)").
						Size(44, 44).Radius(22).Justify(ui.Center).Background(barBg).Border(1, ui.RGBA(255, 255, 255, .12)).TextColor(paper)
					if b.Hovered() || b.FocusVisible() {
						b.Border(1, tone)
					}
					b.Children(func() { glyph(c, "\uE76C", 18) }).OnClick(func() {
						s.Dispatch(state.Event{Kind: state.GalleryViewerStep, Gallery: state.GalleryPayload{Step: 1}})
					})
				})
			}

			// Top bar: title, position, zoom controls (photos) or codec pill (videos), close.
			ui.Row(c.Key("gallery-viewer-topbar")).Absolute().Left(0).Right(0).Top(0).Height(galleryViewerBarHeight).
				Padding(0, 18).AlignItems(ui.Center).Gap(10).Background(barBg).Children(func() {
				ui.Box(c).Absolute().Left(0).Right(0).Bottom(0).Height(1).Background(ui.RGBA(255, 255, 255, .08)).PassThrough()
				if asset.IsVideo() {
					ui.Box(c).TextColor(tone).Children(func() { glyph(c, "\uE714", 16) })
				}
				ui.Text(c, asset.FileName).FontSize(14).FontWeight(600).TextColor(paper).SingleLine().Grow(1).Shrink(1).Tooltip(asset.FileName)
				if idx >= 0 && total > 0 {
					mono(c, fmt.Sprintf("%d / %d", idx+1, total), 12).TextColor(muted)
				}
				if asset.IsVideo() {
					pill := galleryVideoCodecPill(asset)
					ui.Row(c).Height(26).Padding(0, 10).Radius(8).AlignItems(ui.Center).
						Background(ui.RGBA(255, 255, 255, .06)).Border(1, hairline).Children(func() {
						mono(c, pill, 11).TextColor(paper)
					})
				} else {
					if loading && !isFull {
						ui.Text(c, "原图加载中…").FontSize(11).TextColor(muted).SingleLine()
					}
					zoomPct := int(math.Round(float64(v.zoom * 100)))
					if zoomPct <= 0 {
						zoomPct = 100
					}
					s.gallerySmallButton(c, "gallery-zoom-out", "−", false).Label("缩小").Width(32).Padding(0).OnClick(func() {
						galleryAdjustZoom(v, 1/galleryZoomStep)
					})
					mono(c, fmt.Sprintf("%d%%", zoomPct), 12).TextColor(paper).Width(48).TextAlign(ui.Center)
					s.gallerySmallButton(c, "gallery-zoom-in", "+", false).Label("放大").Width(32).Padding(0).OnClick(func() {
						galleryAdjustZoom(v, galleryZoomStep)
					})
					s.gallerySmallButton(c, "gallery-zoom-fit", "适应窗口", false).OnClick(func() {
						v.fit, v.panX, v.panY = true, 0, 0
					})
					s.gallerySmallButton(c, "gallery-zoom-actual", "实际尺寸", false).OnClick(func() {
						v.fit, v.zoom, v.panX, v.panY = false, 1, 0, 0
					})
				}
				iconButton(c, "gallery-viewer-close", "关闭查看器", "\uE8BB", 32, func() {
					s.closeGalleryVideo()
					s.Dispatch(state.Event{Kind: state.GalleryCloseViewer})
				})
			})

			// Bottom metadata + organization + actions bar.
			ui.Row(c.Key("gallery-viewer-footer")).Absolute().Left(0).Right(0).Bottom(0).Height(galleryViewerFootHeight).
				Padding(0, 18).AlignItems(ui.Center).Gap(8).Background(barBg).Children(func() {
				ui.Box(c).Absolute().Left(0).Right(0).Top(0).Height(1).Background(ui.RGBA(255, 255, 255, .08)).PassThrough()
				ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
					metaLine := galleryViewerMetaLine(asset, s.WishLocation)
					ui.Text(c, metaLine).FontSize(12).TextColor(paper).SingleLine().Tooltip(metaLine)
					srcLine := galleryViewerSourceLine(asset)
					ui.Text(c, srcLine).FontSize(11).TextColor(muted).SingleLine().Tooltip(srcLine)
				})

				// Favorite toggle.
				favLabel := "收藏"
				favIcon := "\uEB51"
				if asset.Favorited {
					favLabel = "取消收藏"
					favIcon = "\uEB52"
				}
				favBtn := ui.ButtonBase(c.Key("gallery-viewer-fav")).Label(favLabel).Tooltip(favLabel).
					Size(galleryControlHeight, galleryControlHeight).Radius(10).Justify(ui.Center).
					Background(ui.RGBA(255, 255, 255, .06)).Border(1, hairline).TextColor(paper).Disabled(g.Busy)
				if asset.Favorited {
					favBtn.TextColor(tone).Border(1, tone.Alpha(.55))
				}
				favBtn.Children(func() { glyph(c, favIcon, 15) }).OnClick(func() {
					s.Dispatch(state.Event{Kind: state.GalleryToggleFavorite, Enabled: !asset.Favorited, Gallery: state.GalleryPayload{AssetID: asset.ID}})
				})

				s.gallerySmallButton(c, "gallery-viewer-album", "相册", g.Busy).OnClick(func() {
					s.Dispatch(state.Event{Kind: state.GalleryOpenAlbumModal, Gallery: state.GalleryPayload{AssetID: asset.ID}})
				})
				s.gallerySmallButton(c, "gallery-viewer-tag", "标签", g.Busy).OnClick(func() {
					s.Dispatch(state.Event{Kind: state.GalleryOpenTagModal, Gallery: state.GalleryPayload{AssetID: asset.ID}})
				})

				if !asset.IsVideo() {
					s.gallerySmallButton(c, "gallery-viewer-set-bg", "设为背景", g.Busy || !asset.Available || asset.Invalid).OnClick(func() {
						s.Dispatch(state.Event{Kind: state.GallerySetAsBackground, Gallery: state.GalleryPayload{AssetID: asset.ID}})
					})
					s.gallerySmallButton(c, "gallery-viewer-copy", "复制", !asset.Available || asset.Invalid).Label("复制图片").OnClick(func() {
						s.Dispatch(state.Event{Kind: state.GalleryCopyImage, Gallery: state.GalleryPayload{AssetID: asset.ID}})
					})
				}

				s.gallerySmallButton(c, "gallery-viewer-export", "导出", g.Busy || !asset.Available || asset.Invalid).OnClick(func() {
					s.Dispatch(state.Event{Kind: state.GalleryExportSingle, Gallery: state.GalleryPayload{AssetID: asset.ID}})
				})
				s.gallerySmallButton(c, "gallery-viewer-reveal", "位置", !asset.Available).Label("打开文件位置").OnClick(func() {
					s.Dispatch(state.Event{Kind: state.GalleryRevealAsset, Gallery: state.GalleryPayload{AssetID: asset.ID}})
				})
				if !asset.HasArchived && asset.Available && !asset.Invalid {
					s.accentButton(c, "gallery-viewer-archive", "归档").Label("归档副本").Height(galleryControlHeight).Radius(10).Padding(0, 12).Disabled(g.Busy).OnClick(func() {
						s.Dispatch(state.Event{Kind: state.GalleryArchiveAsset, Gallery: state.GalleryPayload{AssetID: asset.ID}})
					})
				}
			})
		})
	})
}

func (s *Shell) galleryImageStage(c *ui.Context, asset GalleryAsset, bmp *ui.Bitmap, loadErr string) {
	v := &s.gallery.viewer
	canvasBox := ui.Box(c.Key("gallery-viewer-canvas")).Absolute().
		Left(0).Right(0).Top(galleryViewerBarHeight).Bottom(galleryViewerFootHeight).Clip()
	if v.dragging {
		canvasBox.Cursor(ui.CursorGrabbing)
	} else if !v.fit && v.zoom > 1 {
		canvasBox.Cursor(ui.CursorGrab)
	}
	canvasBox.HandleInput(func(ev ui.InputEvent) bool {
		switch ev.Kind {
		case ui.InputScroll:
			if ev.DY == 0 {
				return false
			}
			if ev.DY < 0 {
				galleryAdjustZoom(v, galleryZoomStep)
			} else {
				galleryAdjustZoom(v, 1/galleryZoomStep)
			}
			return true
		case ui.InputPointerDown:
			if ev.Button != 0 {
				return false
			}
			if ev.Clicks >= 2 {
				if v.fit {
					v.fit, v.zoom, v.panX, v.panY = false, 1, 0, 0
				} else {
					v.fit, v.panX, v.panY = true, 0, 0
				}
				v.dragging = false
				return true
			}
			v.dragging = true
			v.dragLastX, v.dragLastY = ev.X, ev.Y
			return true
		case ui.InputPointerMove:
			if !v.dragging {
				return false
			}
			v.panX += ev.X - v.dragLastX
			v.panY += ev.Y - v.dragLastY
			v.dragLastX, v.dragLastY = ev.X, ev.Y
			return true
		case ui.InputPointerUp:
			if v.dragging {
				v.dragging = false
				return true
			}
		}
		return false
	}).Draw(func(p *ui.Painter, r ui.Rect) {
		if bmp == nil {
			msg := "正在加载图片…"
			if loadErr != "" {
				msg = "无法加载图片：" + loadErr
			} else if asset.Invalid {
				msg = "图片已损坏或格式不受支持"
			} else if !asset.Available {
				msg = "源文件不存在且没有可用归档副本"
			}
			w, h := p.MeasureText(r.W-48, ui.Span{Text: msg, Size: 14})
			p.Text(r.X+(r.W-w)/2, r.Y+(r.H-h)/2, msg, 14, muted)
			return
		}
		bw, bh := bmp.Size()
		if bw <= 0 || bh <= 0 || r.W <= 0 || r.H <= 0 {
			return
		}
		natW, natH := float32(bw), float32(bh)
		if asset.Width > 0 && asset.Height > 0 {
			natW, natH = float32(asset.Width), float32(asset.Height)
		}
		pad := float32(28)
		availW := max(r.W-2*pad, 32)
		availH := max(r.H-2*pad, 32)
		fitScale := min(availW/natW, availH/natH)
		if fitScale <= 0 {
			fitScale = 1
		}
		scale := v.zoom
		if v.fit {
			scale = fitScale
			v.zoom = fitScale
		}
		dstW := natW * scale
		dstH := natH * scale
		maxPanX := max((dstW-r.W)/2+48, 0)
		maxPanY := max((dstH-r.H)/2+48, 0)
		v.panX = clampFloat32(v.panX, -maxPanX, maxPanX)
		v.panY = clampFloat32(v.panY, -maxPanY, maxPanY)

		dst := ui.Rect{
			X: r.X + (r.W-dstW)/2 + v.panX,
			Y: r.Y + (r.H-dstH)/2 + v.panY,
			W: dstW,
			H: dstH,
		}
		p.Clip(r, 0, func() {
			p.Shadow(dst, 2, 0, 12, 36, 0, ui.RGBA(0, 0, 0, .55))
			p.ImageRegion(bmp, ui.Rect{}, dst, ui.ImageOptions{})
			p.Stroke(dst, ui.RGBA(255, 255, 255, .10), 0, 1)
		})
	})
}

func (s *Shell) galleryVideoStage(c *ui.Context, asset GalleryAsset, poster *ui.Bitmap, barBg, tone ui.Color, opaque bool) {
	v := &s.gallery.viewer
	p := v.videoPlayer
	if p != nil && p.Playing() {
		c.After(100 * time.Millisecond)
	}

	stageBottom := galleryViewerFootHeight + galleryVideoBarHeight + 16
	ui.Box(c.Key("gallery-video-stage")).Absolute().
		Left(64).Right(64).Top(galleryViewerBarHeight+16).Bottom(stageBottom).
		Radius(12).Clip().Background(ink).Border(1, ui.RGBA(255, 255, 255, .08)).Children(func() {
		switch {
		case p != nil && p.Err() == nil:
			ui.Video(c.Key(fmt.Sprintf("gallery-video-view-%d", asset.ID)), p).Fill().Fit(ui.Contain)
		case poster != nil:
			ui.Image(c.Key(fmt.Sprintf("gallery-video-poster-%d", asset.ID)), poster).Fill().Fit(ui.Contain).Label(asset.FileName)
			ui.Row(c).Absolute().Left(0).Right(0).Bottom(18).Justify(ui.Center).PassThrough().Children(func() {
				msg := v.videoErr
				if msg == "" {
					msg = "视频准备中…"
				}
				ui.Row(c).Padding(6, 14).Radius(14).Background(ui.RGBA(10, 11, 15, .82)).Border(1, hairline).Children(func() {
					ui.Text(c, msg).FontSize(12).TextColor(paper).SingleLine()
				})
			})
		default:
			msg := v.videoErr
			if asset.Invalid {
				msg = "视频文件已损坏或格式不受支持"
			} else if !asset.Available {
				msg = "视频源文件不存在且没有可用归档副本"
			} else if p != nil && p.Err() != nil {
				msg = "无法播放视频：" + state.BriefError(p.Err())
			} else if msg == "" {
				msg = "正在打开视频…"
			}
			ui.Column(c).Fill().Justify(ui.Center).AlignItems(ui.Center).Gap(10).Children(func() {
				ui.Box(c).TextColor(tone).Children(func() { glyph(c, "\uE714", 34) })
				ui.Text(c, msg).FontSize(14).TextColor(muted).SingleLine()
			})
		}
	})

	// Floating glass video playback transport bar.
	var curDur, totalDur time.Duration
	playing := false
	if p != nil && p.Err() == nil {
		playing = p.Playing()
		curDur = p.Position()
		totalDur = p.Duration()
	}
	if totalDur <= 0 && asset.DurationMS > 0 {
		totalDur = time.Duration(asset.DurationMS) * time.Millisecond
	}
	progress := float32(0)
	if totalDur > 0 && curDur > 0 {
		progress = clampFloat32(float32(curDur)/float32(totalDur), 0, 1)
	}

	ctrlBg := barBg
	if !opaque {
		ctrlBg = ui.RGBA(16, 18, 24, .88)
	}
	ui.Row(c.Key("gallery-video-controls")).Absolute().
		Left(84).Right(84).Bottom(galleryViewerFootHeight+10).Height(galleryVideoBarHeight).
		Padding(0, 14).Gap(12).Radius(12).AlignItems(ui.Center).
		Background(ctrlBg).Border(1, ui.RGBA(255, 255, 255, .12)).Children(func() {
		playLabel := "播放视频"
		playGlyph := "\uE768"
		if playing {
			playLabel = "暂停视频"
			playGlyph = "\uE769"
		}
		playBtn := ui.ButtonBase(c.Key("gallery-video-play")).Label(playLabel).Tooltip(playLabel+" (Space)").
			Size(32, 32).Radius(16).Justify(ui.Center).Background(tone).TextColor(ink).Disabled(p == nil || p.Err() != nil)
		playBtn.Children(func() { glyph(c, playGlyph, 14) }).OnClick(func() {
			if p == nil {
				return
			}
			if p.Playing() {
				p.Pause()
			} else {
				if p.Ended() {
					p.Seek(0)
				}
				p.Play()
			}
		})

		mono(c, formatGalleryDuration(curDur.Milliseconds()), 12).TextColor(paper).Width(46)

		// Clickable seek scrubber.
		var trackRect ui.Rect
		scrubber := ui.Box(c.Key("gallery-video-scrubber")).Grow(1).Height(22).
			Cursor(ui.CursorPointer).Role(ui.RoleSlider).Label("播放进度").
			Draw(func(pt *ui.Painter, r ui.Rect) {
				trackRect = r
				midY := r.Y + r.H/2
				bar := ui.Rect{X: r.X, Y: midY - 3, W: r.W, H: 6}
				pt.Fill(bar, ui.RGBA(255, 255, 255, .14), 3)
				fillW := r.W * progress
				if fillW > 0 {
					pt.Fill(ui.Rect{X: r.X, Y: midY - 3, W: fillW, H: 6}, tone, 3)
				}
				knobX := r.X + fillW
				pt.Fill(ui.Rect{X: knobX - 6, Y: midY - 6, W: 12, H: 12}, paper, 6)
			}).
			HandleInput(func(ev ui.InputEvent) bool {
				if p == nil || p.Err() != nil || totalDur <= 0 || trackRect.W <= 0 {
					return false
				}
				if ev.Kind == ui.InputPointerDown && ev.Button == 0 {
					ratio := clampFloat32((ev.X-trackRect.X)/trackRect.W, 0, 1)
					p.Seek(time.Duration(float64(totalDur) * float64(ratio)))
					return true
				}
				return false
			})
		_ = scrubber

		mono(c, formatGalleryDuration(totalDur.Milliseconds()), 12).TextColor(muted).Width(46).TextAlign(ui.End)

		muteLabel := "静音"
		muteGlyph := "\uE767"
		if v.videoMuted {
			muteLabel = "取消静音"
			muteGlyph = "\uE74F"
		}
		muteBtn := ui.ButtonBase(c.Key("gallery-video-mute")).Label(muteLabel).Tooltip(muteLabel+" (M)").
			Size(30, 30).Radius(8).Justify(ui.Center).TextColor(paper).Disabled(p == nil || p.Err() != nil)
		if muteBtn.Hovered() || muteBtn.FocusVisible() {
			muteBtn.Background(ui.RGBA(255, 255, 255, .08))
		}
		muteBtn.Children(func() { glyph(c, muteGlyph, 15) }).OnClick(func() {
			v.videoMuted = !v.videoMuted
			if p != nil {
				p.SetMuted(v.videoMuted)
			}
		})
	})
}

func galleryVideoCodecPill(a GalleryAsset) string {
	var parts []string
	if strings.EqualFold(a.MIMEType, "video/quicktime") {
		parts = append(parts, "MOV")
	} else {
		parts = append(parts, "MP4")
	}
	switch {
	case a.VideoCodec != "" && a.AudioCodec != "":
		parts = append(parts, a.VideoCodec+"/"+a.AudioCodec)
	case a.VideoCodec != "":
		parts = append(parts, a.VideoCodec)
	}
	if a.DurationMS > 0 {
		parts = append(parts, formatGalleryDuration(a.DurationMS))
	}
	return strings.Join(parts, " · ")
}

func galleryAdjustZoom(v *galleryViewerState, factor float32) {
	if v.zoom <= 0 {
		v.zoom = 1
	}
	v.fit = false
	v.zoom = clampFloat32(v.zoom*factor, galleryZoomMin, galleryZoomMax)
}

func clampFloat32(x, lo, hi float32) float32 {
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

func galleryViewerMetaLine(a GalleryAsset, loc *time.Location) string {
	if loc == nil {
		loc = time.Local
	}
	var parts []string
	if a.Width > 0 && a.Height > 0 {
		parts = append(parts, fmt.Sprintf("%d × %d", a.Width, a.Height))
	}
	if a.IsVideo() && a.DurationMS > 0 {
		parts = append(parts, formatGalleryDuration(a.DurationMS))
	}
	if a.ByteSize > 0 {
		parts = append(parts, formatGalleryBytes(a.ByteSize))
	}
	if a.IsVideo() {
		container := "MP4"
		if strings.EqualFold(a.MIMEType, "video/quicktime") {
			container = "MOV"
		}
		switch {
		case a.VideoCodec != "" && a.AudioCodec != "":
			parts = append(parts, fmt.Sprintf("%s (%s / %s)", container, a.VideoCodec, a.AudioCodec))
		case a.VideoCodec != "":
			parts = append(parts, fmt.Sprintf("%s (%s)", container, a.VideoCodec))
		default:
			parts = append(parts, container)
		}
	} else if a.MIMEType != "" {
		parts = append(parts, strings.ToUpper(strings.TrimPrefix(a.MIMEType, "image/")))
	}
	if !a.CapturedAt.IsZero() {
		parts = append(parts, "拍摄时间 "+a.CapturedAt.In(loc).Format("2006-01-02 15:04:05"))
	} else if !a.ModifiedAt.IsZero() {
		parts = append(parts, "修改时间 "+a.ModifiedAt.In(loc).Format("2006-01-02 15:04:05"))
	}
	if len(parts) == 0 {
		return "无可用媒体属性"
	}
	return strings.Join(parts, " · ")
}

func galleryViewerSourceLine(a GalleryAsset) string {
	var tags []string
	if a.HasCached {
		tags = append(tags, "游戏缓存")
	}
	if a.HasExported {
		tags = append(tags, "已保存目录")
	}
	if a.HasCustom {
		tags = append(tags, "自定义目录")
	}
	if a.HasArchived {
		tags = append(tags, "已归档")
	}
	if !a.Available {
		tags = append(tags, "源文件不存在")
	}
	if a.Invalid {
		tags = append(tags, "文件损坏")
	}
	uidText := "UID 未知"
	if len(a.UIDs) > 0 {
		uidText = "UID " + strings.Join(a.UIDs, " / ")
	}
	line := fmt.Sprintf("来源：%s · %s", strings.Join(tags, " + "), uidText)
	if len(a.Tags) > 0 {
		line += " · #" + strings.Join(a.Tags, " #")
	}
	return line
}
