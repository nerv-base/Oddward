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

	prefs := c.Preferences()
	opaque := prefs.HighContrast || prefs.ReduceTransparency
	backdrop := ui.RGBA(6, 7, 10, .96)
	barBg := ui.RGBA(14, 16, 22, .86)
	if opaque {
		backdrop = ink
		barBg = s.theme.Surface
	}
	tone := s.tone()

	bmp, isFull, loading, loadErr := s.galleryViewerBitmap(asset.ID)

	ui.Overlay(c, func() {
		back := ui.Box(c.Key("gallery-viewer-backdrop")).Fill().Background(backdrop).Modal().
			Role(ui.RoleDialog).Label("图片查看器：" + asset.FileName)

		// Modal shortcuts per PRD §6.3. Evaluate every OverlayShortcut
		// unconditionally so MyGo keeps all registrations active across frames.
		esc := back.OverlayShortcut(0, ui.KeyEscape)
		prev := back.OverlayShortcut(0, ui.KeyLeft)
		next := back.OverlayShortcut(0, ui.KeyRight)
		copyImg := back.OverlayShortcut(ui.Ctrl, ui.KeyC)
		exportImg := back.OverlayShortcut(ui.Ctrl, ui.KeyS)
		fitKey := back.OverlayShortcut(0, ui.Key0)
		actualKey := back.OverlayShortcut(0, ui.Key1)
		zoomInKey := back.OverlayShortcut(0, ui.KeyEqual)
		zoomOutKey := back.OverlayShortcut(0, ui.KeyMinus)
		switch {
		case esc:
			s.Dispatch(state.Event{Kind: state.GalleryCloseViewer})
			return
		case prev:
			s.Dispatch(state.Event{Kind: state.GalleryViewerStep, Gallery: state.GalleryPayload{Step: -1}})
		case next:
			s.Dispatch(state.Event{Kind: state.GalleryViewerStep, Gallery: state.GalleryPayload{Step: 1}})
		case copyImg:
			s.Dispatch(state.Event{Kind: state.GalleryCopyImage, Gallery: state.GalleryPayload{AssetID: asset.ID}})
		case exportImg:
			s.Dispatch(state.Event{Kind: state.GalleryExportSingle, Gallery: state.GalleryPayload{AssetID: asset.ID}})
		case fitKey:
			v.fit, v.panX, v.panY = true, 0, 0
		case actualKey:
			v.fit, v.zoom, v.panX, v.panY = false, 1, 0, 0
		case zoomInKey:
			galleryAdjustZoom(v, galleryZoomStep)
		case zoomOutKey:
			galleryAdjustZoom(v, 1/galleryZoomStep)
		}

		back.Children(func() {
			// Interactive zoom/pan image canvas.
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
				// Use true asset dimensions when displaying an L1/L2 preview so
				// zoom 100% matches actual resolution consistently.
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
				// Clamp pan so image remains reachable on screen.
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

			// Floating Left / Right navigation buttons.
			if idx > 0 {
				ui.Box(c).Absolute().Left(16).TopPercent(50).Translate(0, -22).Children(func() {
					b := ui.ButtonBase(c.Key("gallery-viewer-prev")).Label("上一张").Tooltip("上一张 (Left)").
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
					b := ui.ButtonBase(c.Key("gallery-viewer-next")).Label("下一张").Tooltip("下一张 (Right)").
						Size(44, 44).Radius(22).Justify(ui.Center).Background(barBg).Border(1, ui.RGBA(255, 255, 255, .12)).TextColor(paper)
					if b.Hovered() || b.FocusVisible() {
						b.Border(1, tone)
					}
					b.Children(func() { glyph(c, "\uE76C", 18) }).OnClick(func() {
						s.Dispatch(state.Event{Kind: state.GalleryViewerStep, Gallery: state.GalleryPayload{Step: 1}})
					})
				})
			}

			// Top bar: title, position, zoom controls, close.
			ui.Row(c.Key("gallery-viewer-topbar")).Absolute().Left(0).Right(0).Top(0).Height(galleryViewerBarHeight).
				Padding(0, 18).AlignItems(ui.Center).Gap(10).Background(barBg).Children(func() {
				ui.Box(c).Absolute().Left(0).Right(0).Bottom(0).Height(1).Background(ui.RGBA(255, 255, 255, .08)).PassThrough()
				ui.Text(c, asset.FileName).FontSize(14).FontWeight(600).TextColor(paper).SingleLine().Grow(1).Shrink(1).Tooltip(asset.FileName)
				if idx >= 0 && total > 0 {
					mono(c, fmt.Sprintf("%d / %d", idx+1, total), 12).TextColor(muted)
				}
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
				iconButton(c, "gallery-viewer-close", "关闭查看器", "\uE8BB", 32, func() {
					s.Dispatch(state.Event{Kind: state.GalleryCloseViewer})
				})
			})

			// Bottom metadata + actions bar.
			ui.Row(c.Key("gallery-viewer-footer")).Absolute().Left(0).Right(0).Bottom(0).Height(galleryViewerFootHeight).
				Padding(0, 18).AlignItems(ui.Center).Gap(12).Background(barBg).Children(func() {
				ui.Box(c).Absolute().Left(0).Right(0).Top(0).Height(1).Background(ui.RGBA(255, 255, 255, .08)).PassThrough()
				ui.Column(c).Grow(1).MinWidth(0).Gap(3).Children(func() {
					metaLine := galleryViewerMetaLine(asset, s.WishLocation)
					ui.Text(c, metaLine).FontSize(12).TextColor(paper).SingleLine().Tooltip(metaLine)
					srcLine := galleryViewerSourceLine(asset)
					ui.Text(c, srcLine).FontSize(11).TextColor(muted).SingleLine().Tooltip(srcLine)
				})
				s.gallerySmallButton(c, "gallery-viewer-copy", "复制图片", !asset.Available || asset.Invalid).OnClick(func() {
					s.Dispatch(state.Event{Kind: state.GalleryCopyImage, Gallery: state.GalleryPayload{AssetID: asset.ID}})
				})
				s.gallerySmallButton(c, "gallery-viewer-export", "导出", g.Busy || !asset.Available || asset.Invalid).OnClick(func() {
					s.Dispatch(state.Event{Kind: state.GalleryExportSingle, Gallery: state.GalleryPayload{AssetID: asset.ID}})
				})
				s.gallerySmallButton(c, "gallery-viewer-reveal", "打开位置", !asset.Available).OnClick(func() {
					s.Dispatch(state.Event{Kind: state.GalleryRevealAsset, Gallery: state.GalleryPayload{AssetID: asset.ID}})
				})
				if !asset.HasArchived && asset.Available && !asset.Invalid {
					s.accentButton(c, "gallery-viewer-archive", "归档副本").Height(galleryControlHeight).Radius(10).Padding(0, 14).Disabled(g.Busy).OnClick(func() {
						s.Dispatch(state.Event{Kind: state.GalleryArchiveAsset, Gallery: state.GalleryPayload{AssetID: asset.ID}})
					})
				}
			})
		})
	})
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
	if a.ByteSize > 0 {
		parts = append(parts, formatGalleryBytes(a.ByteSize))
	}
	if a.MIMEType != "" {
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
	return fmt.Sprintf("来源：%s · %s", strings.Join(tags, " + "), uidText)
}
