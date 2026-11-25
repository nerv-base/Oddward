// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"fmt"
	"math"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// galleryCanvasState owns the 2D seamless infinite gallery wall camera, inertia
// physics and pointer drag gesture state strictly on the UI thread.
type galleryCanvasState struct {
	camX, camY float32 // world-space center currently at viewport center
	zoom       float32 // 1.0 = default exhibition scale
	velX, velY float32 // world-space velocity in px/sec for damped inertia
	lastTick   time.Time

	dragging     bool
	dragMoved    bool
	dragOnHeart  bool
	dragAssetID  int64
	dragStartX   float32
	dragStartY   float32
	dragLastX    float32
	dragLastY    float32
	dragLastTime time.Time
	hoveredID    int64
	layoutRev    uint64
}

func (st *galleryCanvasState) ensureInit() {
	if st.zoom <= 0 {
		st.zoom = 1
	}
}

func (st *galleryCanvasState) reset() {
	st.camX, st.camY = 0, 0
	st.zoom = 1
	st.velX, st.velY = 0, 0
	st.dragging = false
	st.dragMoved = false
	st.dragOnHeart = false
	st.dragAssetID = 0
}

func (st *galleryCanvasState) recenter(layout galleryLatticeLayout, viewW, viewH float32) {
	st.reset()
	minX, maxX, minY, _ := layout.cameraBounds(viewW, viewH, st.zoom)
	st.camX = clampFloat32(0, minX, maxX)
	st.camY = minY
}

type galleryLatticeLayout struct {
	count   int
	cols    int
	rows    int
	pitchX  float32
	pitchY  float32
	originX float32 // world X of cell (0,0) top-left so wall centers at (0,0)
	originY float32 // world Y of cell (0,0) top-left so wall centers at (0,0)
	worldW  float32
	worldH  float32
}

func computeGalleryLattice(n int) galleryLatticeLayout {
	if n <= 0 {
		return galleryLatticeLayout{}
	}
	var cols int
	switch {
	case n <= 2:
		cols = n
	case n <= 4:
		cols = 2
	case n <= 6:
		cols = 3
	default:
		cols = int(math.Round(math.Sqrt(float64(n) * 1.25)))
		if cols < 4 {
			cols = 4
		}
	}
	rows := (n + cols - 1) / cols
	pitchX := galleryCanvasTileW
	pitchY := galleryCanvasTileH
	worldW := float32(cols) * pitchX
	worldH := float32(rows) * pitchY
	return galleryLatticeLayout{
		count:   n,
		cols:    cols,
		rows:    rows,
		pitchX:  pitchX,
		pitchY:  pitchY,
		originX: -worldW / 2,
		originY: -worldH / 2,
		worldW:  worldW,
		worldH:  worldH,
	}
}

// cellWorldRect returns the flush world-space rectangle for cell (row, col).
func (l galleryLatticeLayout) cellWorldRect(row, col int) ui.Rect {
	return ui.Rect{
		X: l.originX + float32(col)*l.pitchX,
		Y: l.originY + float32(row)*l.pitchY,
		W: galleryCanvasTileW,
		H: galleryCanvasTileH,
	}
}

// cameraBounds returns the soft finite camera limits so few photos stay
// balanced in frame without duplication, while large libraries roam smoothly
// across all tiles with breathing margins for the floating HUD.
func (l galleryLatticeLayout) cameraBounds(viewW, viewH, zoom float32) (minX, maxX, minY, maxY float32) {
	if zoom <= 0 {
		zoom = 1
	}
	// Reserve top headroom for the floating header HUD; side edges run flush edge-to-edge.
	hudTop := float32(148)
	hudBottom := float32(64)

	visW := max(viewW, 160) / zoom
	visH := max(viewH-hudTop-hudBottom, 160) / zoom
	// Visual center offset caused by asymmetric top/bottom HUD clearance.
	hudCenterOffsetY := (hudTop - hudBottom) / (2 * zoom)

	if l.worldW <= visW {
		minX, maxX = 0, 0
	} else {
		halfSpanX := (l.worldW - visW) / 2
		minX, maxX = -halfSpanX, halfSpanX
	}
	if l.worldH <= visH {
		minY, maxY = -hudCenterOffsetY, -hudCenterOffsetY
	} else {
		halfSpanY := (l.worldH - visH) / 2
		minY = -halfSpanY - hudCenterOffsetY
		maxY = halfSpanY - hudCenterOffsetY
	}
	return minX, maxX, minY, maxY
}

func (s *Shell) stepGalleryCanvasPhysics(c *ui.Context, layout galleryLatticeLayout, viewW, viewH float32) {
	st := &s.gallery.canvas
	st.ensureInit()
	now := c.Now()
	dt := float32(0.016)
	if !st.lastTick.IsZero() {
		raw := float32(now.Sub(st.lastTick).Seconds())
		if raw > 0 && raw < 0.1 {
			dt = raw
		}
	}
	st.lastTick = now

	minX, maxX, minY, maxY := layout.cameraBounds(viewW, viewH, st.zoom)

	// Under reduced motion, clamp immediately without inertia or spring-back.
	if !s.motion() {
		st.velX, st.velY = 0, 0
		if !st.dragging {
			st.camX = clampFloat32(st.camX, minX, maxX)
			st.camY = clampFloat32(st.camY, minY, maxY)
		}
		return
	}

	if st.dragging {
		return
	}

	// Integrate damped inertia velocity.
	moving := false
	if math.Abs(float64(st.velX)) > 6 || math.Abs(float64(st.velY)) > 6 {
		st.camX += st.velX * dt
		st.camY += st.velY * dt
		decay := float32(math.Exp(float64(-dt * 7.2)))
		st.velX *= decay
		st.velY *= decay
		moving = true
	} else {
		st.velX, st.velY = 0, 0
	}

	// Soft elastic spring-back when past finite gallery bounds.
	targetX := clampFloat32(st.camX, minX, maxX)
	targetY := clampFloat32(st.camY, minY, maxY)
	dx := targetX - st.camX
	dy := targetY - st.camY
	if math.Abs(float64(dx)) > 0.6 || math.Abs(float64(dy)) > 0.6 {
		pull := 1 - float32(math.Exp(float64(-dt*12.0)))
		st.camX += dx * pull
		st.camY += dy * pull
		// Damp outward momentum quickly when hitting boundary.
		st.velX *= 0.72
		st.velY *= 0.72
		moving = true
	} else {
		st.camX, st.camY = targetX, targetY
	}

	if moving {
		c.AnimationFrame()
	}
}

func (s *Shell) adjustGalleryCanvasZoom(layout galleryLatticeLayout, viewW, viewH, factor, anchorX, anchorY float32) {
	st := &s.gallery.canvas
	st.ensureInit()
	oldZoom := st.zoom
	nextZoom := clampFloat32(oldZoom*factor, galleryCanvasZoomMin, galleryCanvasZoomMax)
	if nextZoom == oldZoom {
		return
	}
	// Keep the world point under (anchorX, anchorY) fixed on screen while zooming.
	worldX := st.camX + (anchorX-viewW/2)/oldZoom
	worldY := st.camY + (anchorY-viewH/2)/oldZoom
	st.zoom = nextZoom
	st.camX = worldX - (anchorX-viewW/2)/nextZoom
	st.camY = worldY - (anchorY-viewH/2)/nextZoom
	st.velX, st.velY = 0, 0
	minX, maxX, minY, maxY := layout.cameraBounds(viewW, viewH, st.zoom)
	st.camX = clampFloat32(st.camX, minX, maxX)
	st.camY = clampFloat32(st.camY, minY, maxY)
}

// galleryInfiniteCanvas renders the folia-major-inspired Bravais staggered
// infinite gallery wall with viewport culling, rubber-band finite edges,
// left-drag inertia panning, wheel scroll & Ctrl+wheel cursor-anchored zoom.
func (s *Shell) galleryInfiniteCanvas(c *ui.Context, viewW, viewH float32, opaque bool, tone ui.Color) {
	st := &s.gallery.canvas
	st.ensureInit()
	assets := s.gallery.assets
	if len(assets) == 0 {
		return
	}
	layout := computeGalleryLattice(len(assets))
	if st.layoutRev != s.gallery.rowsRev {
		st.recenter(layout, viewW, viewH)
		st.layoutRev = s.gallery.rowsRev
	}
	s.stepGalleryCanvasPhysics(c, layout, viewW, viewH)
	st.hoveredID = 0

	// Shared canvas pointer/scroll handler. Child tiles pass their top-left
	// screen offset so coordinates convert cleanly into canvas space.
	handleCanvasInput := func(ev ui.InputEvent, offsetX, offsetY float32, tileAsset *GalleryAsset, heartRect ui.Rect) bool {
		cx := ev.X + offsetX
		cy := ev.Y + offsetY
		switch ev.Kind {
		case ui.InputZoom:
			if ev.Scale > 0 && ev.Scale != 1 {
				s.adjustGalleryCanvasZoom(layout, viewW, viewH, ev.Scale, cx, cy)
				return true
			}
			return false
		case ui.InputScroll:
			if ev.Mods&ui.Ctrl != 0 {
				if ev.DY == 0 {
					return false
				}
				factor := float32(1.12)
				if ev.DY > 0 {
					factor = 1 / factor
				}
				s.adjustGalleryCanvasZoom(layout, viewW, viewH, factor, cx, cy)
				return true
			}
			dx, dy := ev.DX, ev.DY
			if ev.Mods&ui.Shift != 0 && dx == 0 {
				dx, dy = dy, 0
			}
			if dx == 0 && dy == 0 {
				return false
			}
			st.velX, st.velY = 0, 0
			st.camX += dx / st.zoom
			st.camY += dy / st.zoom
			minX, maxX, minY, maxY := layout.cameraBounds(viewW, viewH, st.zoom)
			// Soft overscroll allowance when wheel-scrolling, springing smoothly back.
			pad := float32(48) / st.zoom
			if !s.motion() {
				pad = 0
			}
			st.camX = clampFloat32(st.camX, minX-pad, maxX+pad)
			st.camY = clampFloat32(st.camY, minY-pad, maxY+pad)
			if s.motion() {
				c.AnimationFrame()
			}
			return true
		case ui.InputPointerDown:
			if ev.Button != 0 {
				return false
			}
			st.dragging = true
			st.dragMoved = false
			st.dragOnHeart = tileAsset != nil && !s.Model.Gallery.SelectMode && !tileAsset.Invalid &&
				heartRect.W > 0 && ev.X >= heartRect.X && ev.X <= heartRect.X+heartRect.W &&
				ev.Y >= heartRect.Y && ev.Y <= heartRect.Y+heartRect.H
			st.velX, st.velY = 0, 0
			st.dragStartX, st.dragStartY = cx, cy
			st.dragLastX, st.dragLastY = cx, cy
			st.dragLastTime = ev.Time
			if tileAsset != nil {
				st.dragAssetID = tileAsset.ID
			} else {
				st.dragAssetID = 0
			}
			return true
		case ui.InputPointerMove:
			if !st.dragging {
				return false
			}
			dx := cx - st.dragLastX
			dy := cy - st.dragLastY
			if !st.dragMoved {
				if math.Hypot(float64(cx-st.dragStartX), float64(cy-st.dragStartY)) >= 6 {
					st.dragMoved = true
				}
			}
			if st.dragMoved {
				minX, maxX, minY, maxY := layout.cameraBounds(viewW, viewH, st.zoom)
				wx := -dx / st.zoom
				wy := -dy / st.zoom
				// Rubber-band resistance when pulling past finite gallery bounds.
				if (st.camX < minX && wx < 0) || (st.camX > maxX && wx > 0) {
					wx *= 0.28
				}
				if (st.camY < minY && wy < 0) || (st.camY > maxY && wy > 0) {
					wy *= 0.28
				}
				st.camX += wx
				st.camY += wy
				if !st.dragLastTime.IsZero() && ev.Time.After(st.dragLastTime) {
					dt := float32(ev.Time.Sub(st.dragLastTime).Seconds())
					if dt > 0.002 && dt < 0.2 {
						st.velX = st.velX*0.35 + (wx/dt)*0.65
						st.velY = st.velY*0.35 + (wy/dt)*0.65
					}
				}
			}
			st.dragLastX, st.dragLastY = cx, cy
			st.dragLastTime = ev.Time
			return true
		case ui.InputPointerUp:
			if !st.dragging {
				return false
			}
			wasMoved := st.dragMoved
			wasOnHeart := st.dragOnHeart
			targetID := st.dragAssetID
			st.dragging = false
			st.dragMoved = false
			st.dragOnHeart = false
			st.dragAssetID = 0
			if ev.Canceled {
				st.velX, st.velY = 0, 0
				return true
			}
			if !wasMoved && targetID > 0 {
				st.velX, st.velY = 0, 0
				if wasOnHeart {
					if a, ok := s.galleryAssetByID(targetID); ok && !a.Invalid {
						s.Dispatch(state.Event{
							Kind:    state.GalleryToggleFavorite,
							Enabled: !a.Favorited,
							Gallery: state.GalleryPayload{AssetID: targetID},
						})
					}
				} else if s.Model.Gallery.SelectMode {
					s.toggleGallerySelect(targetID)
				} else {
					s.gallery.viewer.reset(targetID)
					s.Dispatch(state.Event{Kind: state.GalleryOpenViewer, Gallery: state.GalleryPayload{AssetID: targetID}})
				}
				return true
			}
			if s.motion() {
				c.AnimationFrame()
			}
			return true
		}
		return false
	}

	// Compute visible lattice index window (O(visible), scales smoothly to 10,000+ items).
	visMinX := st.camX - viewW/(2*st.zoom)
	visMaxX := st.camX + viewW/(2*st.zoom)
	visMinY := st.camY - viewH/(2*st.zoom)
	visMaxY := st.camY + viewH/(2*st.zoom)

	rowStart := max(0, int(math.Floor(float64((visMinY-layout.originY)/layout.pitchY)))-1)
	rowEnd := min(layout.rows-1, int(math.Ceil(float64((visMaxY-layout.originY)/layout.pitchY)))+1)
	colStart := max(0, int(math.Floor(float64((visMinX-layout.originX)/layout.pitchX)))-1)
	colEnd := min(layout.cols-1, int(math.Ceil(float64((visMaxX-layout.originX)/layout.pitchX)))+1)

	thumbEdge := galleryThumbEdge(s.gallery.dpiScale * max(st.zoom, 1))

	canvasEl := ui.Box(c.Key("gallery-infinite-canvas")).Absolute().Left(0).Right(0).Top(0).Bottom(0).Clip().
		Label("无限画廊画布").AutoFocus()
	if st.dragging {
		canvasEl.Cursor(ui.CursorGrabbing)
	} else {
		canvasEl.Cursor(ui.CursorGrab)
	}
	canvasEl.HandleInput(func(ev ui.InputEvent) bool {
		return handleCanvasInput(ev, 0, 0, nil, ui.Rect{})
	}).Draw(func(p *ui.Painter, r ui.Rect) {
		if opaque {
			return
		}
		// Ultra-subtle architectural dot matrix anchored to world space so panning feels tangible.
		dotStep := 48 * st.zoom
		if dotStep >= 22 {
			offX := float32(math.Mod(float64(viewW/2-st.camX*st.zoom), float64(dotStep)))
			offY := float32(math.Mod(float64(viewH/2-st.camY*st.zoom), float64(dotStep)))
			if offX < 0 {
				offX += dotStep
			}
			if offY < 0 {
				offY += dotStep
			}
			dotColor := ui.RGBA(255, 255, 255, .045)
			for y := r.Y + offY; y < r.Y+r.H; y += dotStep {
				for x := r.X + offX; x < r.X+r.W; x += dotStep {
					p.Fill(ui.Rect{X: x, Y: y, W: 1.5, H: 1.5}, dotColor, 0.75)
				}
			}
		}
	}).Children(func() {
		// Render ONLY the visible tiles (typically 12–30) with full accessibility,
		// hover states, context menus and GPU contain framing.
		for row := rowStart; row <= rowEnd; row++ {
			for col := colStart; col <= colEnd; col++ {
				idx := row*layout.cols + col
				if idx < 0 || idx >= len(assets) {
					continue
				}
				wr := layout.cellWorldRect(row, col)
				// Snap both edges to integer DIPs so flush adjacent tiles never show subpixel hair cracks.
				sx := float32(math.Round(float64(viewW/2 + (wr.X-st.camX)*st.zoom)))
				sy := float32(math.Round(float64(viewH/2 + (wr.Y-st.camY)*st.zoom)))
				sx1 := float32(math.Round(float64(viewW/2 + (wr.X+wr.W-st.camX)*st.zoom)))
				sy1 := float32(math.Round(float64(viewH/2 + (wr.Y+wr.H-st.camY)*st.zoom)))
				sw := max(sx1-sx, 1)
				sh := max(sy1-sy, 1)
				if sx+sw < -40 || sx > viewW+40 || sy+sh < -40 || sy > viewH+40 {
					// Prefetch just outside viewport for silky panning.
					s.prefetchGalleryThumb(assets[idx], thumbEdge)
					continue
				}
				s.galleryCanvasTile(c, assets[idx], sx, sy, sw, sh, thumbEdge, opaque, tone, handleCanvasInput)
			}
		}
	})
}

func (s *Shell) galleryCanvasTile(
	c *ui.Context,
	asset GalleryAsset,
	sx, sy, sw, sh float32,
	thumbEdge int,
	opaque bool,
	tone ui.Color,
	handleInput func(ev ui.InputEvent, offsetX, offsetY float32, tileAsset *GalleryAsset, heartRect ui.Rect) bool,
) {
	selected := s.gallery.selected[asset.ID]
	focused := s.Model.Gallery.FocusedID == asset.ID
	label := galleryAssetAccessibleLabel(asset)
	heartSize := float32(28)
	heartRect := ui.Rect{X: sw - heartSize - 8, Y: 8, W: heartSize, H: heartSize}

	// Flush seamless contact-sheet tile: sharp 90° corners, zero gap, zero shadow.
	tile := ui.Box(c.Key(fmt.Sprintf("gallery-canvas-tile-%d", asset.ID))).
		Absolute().Left(sx).Top(sy).Size(sw, sh).
		Clip().
		Role(ui.RoleButton).Label(label).Tooltip(label).
		Background(ui.RGBA(12, 13, 18, 1))
	if opaque {
		tile.Background(s.theme.Surface)
	}

	hovered := tile.Hovered() && !s.gallery.canvas.dragging
	if hovered {
		s.gallery.canvas.hoveredID = asset.ID
	}
	if s.gallery.canvas.dragging {
		tile.Cursor(ui.CursorGrabbing)
	} else {
		tile.Cursor(ui.CursorPointer)
	}

	bmp := s.galleryThumb(asset, thumbEdge)
	switch {
	case selected:
		tile.Border(2, tone)
	case hovered || focused:
		tile.Border(2, tone.Alpha(.88))
	case bmp == nil:
		// Keep placeholder tiles subtly legible against adjacent frames.
		tile.Border(1, ui.RGBA(255, 255, 255, .06))
	}

	assetCopy := asset
	tile.HandleInput(func(ev ui.InputEvent) bool {
		return handleInput(ev, sx, sy, &assetCopy, heartRect)
	}).ContextMenu(func(m *ui.Menu) {
		s.galleryAssetContextMenu(m, assetCopy)
	}).Children(func() {
		if bmp != nil {
			// Full-bleed edge-to-edge cover on the seamless wall; clicking opens the uncropped viewer.
			ui.Image(c.Key(fmt.Sprintf("gallery-cimg-%d", asset.ID)), bmp).
				Absolute().Left(0).Right(0).Top(0).Bottom(0).Fit(ui.Cover).Label(asset.FileName).PassThrough()
		} else if asset.IsVideo() && asset.Available && !asset.Invalid {
			ui.Column(c).Fill().Justify(ui.Center).AlignItems(ui.Center).Gap(6).PassThrough().Children(func() {
				ui.Box(c).TextColor(tone.Alpha(.85)).Children(func() { glyph(c, "\uE714", 26) })
				sub := "MP4 录像"
				if asset.VideoCodec != "" {
					sub = asset.VideoCodec + " 录像"
				}
				ui.Text(c, sub).FontSize(12).TextColor(muted).SingleLine()
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

		// Calm hover scrim with filename & resolution — absent at rest so the art breathes.
		if hovered && sw >= 150 {
			ui.Box(c).Absolute().Left(0).Right(0).Bottom(0).Height(52).PassThrough().LinearGradient(ui.LinearGradient{
				Angle: 180,
				Stops: []ui.GradientStop{
					{Color: ui.Transparent, At: 0},
					{Color: ui.RGBA(8, 9, 13, .82), At: 1},
				},
			})
			ui.Row(c).Absolute().Left(10).Right(10).Bottom(8).AlignItems(ui.Center).Gap(6).PassThrough().Children(func() {
				name := asset.FileName
				if name == "" {
					name = "未命名作品"
				}
				ui.Text(c, name).FontSize(11).FontWeight(600).TextColor(paper).SingleLine().Grow(1).Shrink(1)
				if !asset.IsVideo() && asset.Width > 0 && asset.Height > 0 {
					mono(c, fmt.Sprintf("%dx%d", asset.Width, asset.Height), 10).TextColor(muted)
				}
			})
		}

		// Minimal corner indicators: error/missing always visible; archive/tags appear subtly on hover.
		showStatus := asset.Invalid || !asset.Available || (hovered && sw >= 160)
		if showStatus {
			badgeText, badgeBg, badgeFg := galleryStatusBadge(asset, tone, opaque, s.wishWarningColor())
			if badgeText != "" {
				ui.Row(c).Absolute().Left(8).Top(8).Height(20).Padding(0, 7).Radius(6).
					AlignItems(ui.Center).Background(badgeBg).PassThrough().Children(func() {
					ui.Text(c, badgeText).FontSize(10).FontWeight(600).TextColor(badgeFg).SingleLine()
				})
			}
		} else if asset.HasArchived && sw >= 140 {
			// Quiet 6px accent archive dot at rest (zero visual noise).
			ui.Box(c).Absolute().Left(10).Top(10).Size(6, 6).Radius(3).Background(tone).PassThrough()
		}

		// Video duration badge stays legible at rest so videos stand out from stills.
		if asset.IsVideo() && sw >= 120 {
			durText := "视频"
			if asset.DurationMS > 0 {
				durText = formatGalleryDuration(asset.DurationMS)
			}
			bottomPos := float32(8)
			if hovered && sw >= 150 {
				bottomPos = 30
			}
			ui.Row(c).Absolute().Right(8).Bottom(bottomPos).Height(20).Padding(0, 7).Radius(6).Gap(4).
				AlignItems(ui.Center).Background(ui.RGBA(8, 9, 13, .78)).PassThrough().Children(func() {
				ui.Box(c).TextColor(tone).Children(func() { glyph(c, "\uE768", 9) })
				mono(c, durText, 10).TextColor(paper)
			})
		}

		// Top-right selection badge or favorite heart.
		if s.Model.Gallery.SelectMode {
			checkBg := ui.RGBA(12, 13, 16, .74)
			checkBorder := ui.RGBA(255, 255, 255, .48)
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
		} else if !asset.Invalid && (asset.Favorited || hovered) && sw >= 130 {
			iconKey := "\uEB51"
			iconColor := paper.Alpha(.85)
			btnBg := ui.RGBA(10, 12, 18, .66)
			if asset.Favorited {
				iconKey = "\uEB52"
				iconColor = tone
				btnBg = ui.RGBA(10, 12, 18, .78)
			}
			ui.Row(c).Absolute().Left(heartRect.X).Top(heartRect.Y).Size(heartRect.W, heartRect.H).Radius(heartRect.W / 2).
				Justify(ui.Center).AlignItems(ui.Center).Background(btnBg).TextColor(iconColor).PassThrough().Children(func() {
				glyph(c, iconKey, 13)
			})
		}
	})
}

func (s *Shell) galleryAssetContextMenu(m *ui.Menu, asset GalleryAsset) {
	viewLabel := "查看大图"
	if asset.IsVideo() {
		viewLabel = "播放视频"
	}
	if m.Item(viewLabel).Chosen() {
		s.gallery.viewer.reset(asset.ID)
		s.Dispatch(state.Event{Kind: state.GalleryOpenViewer, Gallery: state.GalleryPayload{AssetID: asset.ID}})
	}
	favItem := "加入收藏"
	if asset.Favorited {
		favItem = "取消收藏"
	}
	if m.Item(favItem).Chosen() {
		s.Dispatch(state.Event{Kind: state.GalleryToggleFavorite, Enabled: !asset.Favorited, Gallery: state.GalleryPayload{AssetID: asset.ID}})
	}
	if m.Item("添加到相册…").Chosen() {
		s.Dispatch(state.Event{Kind: state.GalleryOpenAlbumModal, Gallery: state.GalleryPayload{AssetID: asset.ID}})
	}
	if m.Item("管理标签…").Chosen() {
		s.Dispatch(state.Event{Kind: state.GalleryOpenTagModal, Gallery: state.GalleryPayload{AssetID: asset.ID}})
	}
	if !asset.IsVideo() {
		if m.Item("设为启动器背景").Disabled(!asset.Available || asset.Invalid).Chosen() {
			s.Dispatch(state.Event{Kind: state.GallerySetAsBackground, Gallery: state.GalleryPayload{AssetID: asset.ID}})
		}
	}
	m.Separator()
	if !asset.IsVideo() {
		if m.Item("复制图片").Disabled(!asset.Available || asset.Invalid).Chosen() {
			s.Dispatch(state.Event{Kind: state.GalleryCopyImage, Gallery: state.GalleryPayload{AssetID: asset.ID}})
		}
	}
	expLabel := "导出图片…"
	if asset.IsVideo() {
		expLabel = "导出视频…"
	}
	if m.Item(expLabel).Disabled(!asset.Available || asset.Invalid).Chosen() {
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
}
