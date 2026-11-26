// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"fmt"
	"math"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// galleryCanvasState owns the 2D seamless editorial-quilt infinite gallery wall
// camera, cached multi-scale layout, inertia physics and pointer drag gesture
// state strictly on the UI thread.
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

	quilt      galleryQuilt
	quiltRev   uint64
	quiltCount int
	cameraRev  uint64
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

func (st *galleryCanvasState) ensureQuilt(assets []GalleryAsset, rev uint64) galleryQuilt {
	if st.quiltRev != rev || st.quiltCount != len(assets) {
		st.quilt = computeGalleryQuilt(assets)
		st.quiltRev = rev
		st.quiltCount = len(assets)
	}
	return st.quilt
}

func (st *galleryCanvasState) recenter(q galleryQuilt, viewW, viewH float32) {
	st.reset()
	minX, maxX, minY, _ := q.cameraBounds(viewW, viewH, st.zoom)
	st.camX = clampFloat32(0, minX, maxX)
	st.camY = minY
}

// galleryQuiltTile places one asset onto an integer span of the atomic 16:9
// grid so uniform 16:9 game captures form a multi-scale editorial mosaic
// (3x3 hero, 2x2 feature, 1x2 native portrait, 1x1 compact) with zero gaps,
// zero rounded corners and zero interior holes.
type galleryQuiltTile struct {
	assetIdx int
	col, row int
	spanW    int
	spanH    int
}

type galleryQuilt struct {
	cols       int
	rows       int
	originX    float32 // world X of cell (0,0) top-left so wall centers at (0,0)
	originY    float32 // world Y of cell (0,0) top-left so wall centers at (0,0)
	worldW     float32
	worldH     float32
	tiles      []galleryQuiltTile
	rowBuckets [][]int // starting row -> indices into tiles for O(visible) rendering
}

func (q galleryQuilt) tileWorldRect(t galleryQuiltTile) ui.Rect {
	return ui.Rect{
		X: q.originX + float32(t.col)*galleryCanvasUnitW,
		Y: q.originY + float32(t.row)*galleryCanvasUnitH,
		W: float32(t.spanW) * galleryCanvasUnitW,
		H: float32(t.spanH) * galleryCanvasUnitH,
	}
}

func galleryAssetIsPortrait(a GalleryAsset) bool {
	return a.Width > 0 && a.Height > 0 && a.Height*10 > a.Width*11
}

// galleryAssetHash mixes SHA256 or ID into a deterministic unsigned value so
// editorial rhythm stays stable across pans and reloads without randomness.
func galleryAssetHash(a GalleryAsset, idx int) uint32 {
	var h uint32 = 2166136261
	if a.SHA256 != "" {
		limit := min(len(a.SHA256), 16)
		for i := 0; i < limit; i++ {
			h ^= uint32(a.SHA256[i])
			h *= 16777619
		}
		return h
	}
	v := uint64(a.ID)*2654435761 ^ uint64(idx+1)*40503
	return uint32(v ^ (v >> 32))
}

// galleryAssetPriority scores real user/media curation signals so favorites,
// videos, full-res exports, tagged/albumed and archived works naturally rise
// to hero/feature scale while uncurated libraries still receive editorial rhythm.
func galleryAssetPriority(a GalleryAsset) int {
	score := 0
	if a.Favorited {
		score += 4
	}
	if a.IsVideo() {
		score += 3
	}
	if a.HasExported {
		score += 2
	}
	if len(a.Tags) > 0 || len(a.AlbumIDs) > 0 {
		score += 2
	}
	if a.HasArchived {
		score++
	}
	if a.Width >= 2560 {
		score++
	}
	return score
}

// computeGalleryQuilt packs assets into a zero-hole flush multi-scale quilt.
// All landscape tiers (3x3, 2x2, 1x1) share exact 16:9 geometry so game photos
// keep their full frame uncropped; true portrait captures claim 1x2 slots.
func computeGalleryQuilt(assets []GalleryAsset) galleryQuilt {
	n := len(assets)
	if n == 0 {
		return galleryQuilt{}
	}

	// Curated small-library compositions (1..6 works): exact zero-hole blocks
	// with natural scale hierarchy even before a library grows large.
	if n <= 6 {
		return buildSmallGalleryQuilt(assets)
	}

	// Assign target spans with rolling cadence so scale contrast holds even
	// inside uniform tabs (e.g. all Favorites or all Videos or all raw 16:9).
	type pendingItem struct {
		idx   int
		wantW int
		wantH int
		prio  int
	}
	allowHero := n >= 9
	queue := make([]pendingItem, n)
	totalCells := 0
	sinceHero := 99
	consecFeature := 0
	for i, a := range assets {
		prio := galleryAssetPriority(a)
		h := galleryAssetHash(a, i)
		var w, ht int
		switch {
		case galleryAssetIsPortrait(a):
			w, ht = 1, 2
			consecFeature = 0
		case i == 0 && allowHero:
			// Lead latest work anchors the top-left of the wall as a 3x3 showcase hero.
			w, ht = 3, 3
			sinceHero = 0
			consecFeature = 0
		case allowHero && sinceHero >= 8 && (prio >= 4 || (prio >= 2 && h%3 == 0) || sinceHero >= 11):
			w, ht = 3, 3
			sinceHero = 0
			consecFeature = 0
		case consecFeature < 2 && (prio >= 3 || (prio >= 1 && h%3 != 0) || h%4 == 0):
			w, ht = 2, 2
			sinceHero++
			consecFeature++
		default:
			w, ht = 1, 1
			sinceHero++
			consecFeature = 0
		}
		queue[i] = pendingItem{idx: i, wantW: w, wantH: ht, prio: prio}
		totalCells += w * ht
	}

	// Each atomic cell is already 16:9 (192x108), so cols ≈ sqrt(totalCells)
	// naturally yields a balanced ~16:9 physical 2D wall at every library size.
	gridCols := int(math.Round(math.Sqrt(float64(totalCells))))
	if allowHero && gridCols < 7 {
		gridCols = 7
	} else if gridCols < 5 {
		gridCols = 5
	}

	// Zero-hole skyline packer: always fills the lexicographically earliest
	// skyline trough (minRow, leftmost col), using a short lookahead window
	// (<= 6 items) to plug narrow troughs with 1x1/1x2 works rather than
	// shrinking upcoming heroes unnecessarily.
	colHeight := make([]int, gridCols)
	tiles := make([]galleryQuiltTile, 0, n)
	maxRow := 0

	const lookahead = 6
	for len(queue) > 0 {
		minR := colHeight[0]
		startC := 0
		for c := 1; c < gridCols; c++ {
			if colHeight[c] < minR {
				minR = colHeight[c]
				startC = c
			}
		}
		runW := 1
		for startC+runW < gridCols && colHeight[startC+runW] == minR {
			runW++
		}

		// Pick the first item within the local window whose desired width fits runW.
		pick := -1
		win := min(len(queue), lookahead)
		for k := 0; k < win; k++ {
			if queue[k].wantW <= runW {
				pick = k
				break
			}
		}
		if pick < 0 {
			pick = 0
		}
		item := queue[pick]
		queue = append(queue[:pick], queue[pick+1:]...)

		w, h := item.wantW, item.wantH
		// Gracefully shrink if no local lookahead item fit a narrow trough.
		if w > runW {
			if runW >= 2 {
				w, h = 2, 2
			} else if galleryAssetIsPortrait(assets[item.idx]) {
				w, h = 1, 2
			} else {
				w, h = 1, 1
			}
		}

		// Level off the final band so the bottom silhouette stays composed.
		if len(queue) == 0 && w == 1 && runW >= 2 {
			skyMax := minR
			for _, ch := range colHeight {
				if ch > skyMax {
					skyMax = ch
				}
			}
			if skyMax-minR >= 2 && !galleryAssetIsPortrait(assets[item.idx]) {
				w, h = 2, 2
			}
		} else if len(queue) < gridCols/2 && w == 3 && h == 3 {
			w, h = 2, 2
		}

		tiles = append(tiles, galleryQuiltTile{
			assetIdx: item.idx,
			col:      startC,
			row:      minR,
			spanW:    w,
			spanH:    h,
		})
		for k := 0; k < w; k++ {
			colHeight[startC+k] = minR + h
		}
		if minR+h > maxRow {
			maxRow = minR + h
		}
	}

	return finalizeGalleryQuilt(gridCols, maxRow, tiles)
}

func buildSmallGalleryQuilt(assets []GalleryAsset) galleryQuilt {
	n := len(assets)
	var cols, rows int
	var tiles []galleryQuiltTile
	switch n {
	case 1:
		cols, rows = 3, 3
		tiles = []galleryQuiltTile{{assetIdx: 0, col: 0, row: 0, spanW: 3, spanH: 3}}
	case 2:
		cols, rows = 4, 2
		tiles = []galleryQuiltTile{
			{assetIdx: 0, col: 0, row: 0, spanW: 2, spanH: 2},
			{assetIdx: 1, col: 2, row: 0, spanW: 2, spanH: 2},
		}
	case 3:
		cols, rows = 3, 2
		tiles = []galleryQuiltTile{
			{assetIdx: 0, col: 0, row: 0, spanW: 2, spanH: 2},
			{assetIdx: 1, col: 2, row: 0, spanW: 1, spanH: 1},
			{assetIdx: 2, col: 2, row: 1, spanW: 1, spanH: 1},
		}
	case 4:
		cols, rows = 4, 2
		tiles = []galleryQuiltTile{
			{assetIdx: 0, col: 0, row: 0, spanW: 2, spanH: 2},
			{assetIdx: 1, col: 2, row: 0, spanW: 1, spanH: 1},
			{assetIdx: 2, col: 2, row: 1, spanW: 1, spanH: 1},
			{assetIdx: 3, col: 3, row: 0, spanW: 1, spanH: 2},
		}
	case 5:
		cols, rows = 4, 2
		tiles = []galleryQuiltTile{
			{assetIdx: 0, col: 0, row: 0, spanW: 2, spanH: 2},
			{assetIdx: 1, col: 2, row: 0, spanW: 1, spanH: 1},
			{assetIdx: 2, col: 3, row: 0, spanW: 1, spanH: 1},
			{assetIdx: 3, col: 2, row: 1, spanW: 1, spanH: 1},
			{assetIdx: 4, col: 3, row: 1, spanW: 1, spanH: 1},
		}
	case 6:
		cols, rows = 4, 3
		tiles = []galleryQuiltTile{
			{assetIdx: 0, col: 0, row: 0, spanW: 2, spanH: 2},
			{assetIdx: 1, col: 2, row: 0, spanW: 1, spanH: 1},
			{assetIdx: 2, col: 3, row: 0, spanW: 1, spanH: 1},
			{assetIdx: 3, col: 2, row: 1, spanW: 2, spanH: 2},
			{assetIdx: 4, col: 0, row: 2, spanW: 1, spanH: 1},
			{assetIdx: 5, col: 1, row: 2, spanW: 1, spanH: 1},
		}
	}
	return finalizeGalleryQuilt(cols, rows, tiles)
}

func finalizeGalleryQuilt(cols, rows int, tiles []galleryQuiltTile) galleryQuilt {
	worldW := float32(cols) * galleryCanvasUnitW
	worldH := float32(rows) * galleryCanvasUnitH
	buckets := make([][]int, rows)
	for i, t := range tiles {
		if t.row >= 0 && t.row < rows {
			buckets[t.row] = append(buckets[t.row], i)
		}
	}
	return galleryQuilt{
		cols:       cols,
		rows:       rows,
		originX:    -worldW / 2,
		originY:    -worldH / 2,
		worldW:     worldW,
		worldH:     worldH,
		tiles:      tiles,
		rowBuckets: buckets,
	}
}

// cameraBounds returns the soft finite camera limits so few photos stay
// balanced in frame without duplication, while large libraries roam smoothly
// across all tiles edge-to-edge under the floating header HUD.
func (q galleryQuilt) cameraBounds(viewW, viewH, zoom float32) (minX, maxX, minY, maxY float32) {
	if zoom <= 0 {
		zoom = 1
	}
	hudTop := float32(148)
	hudBottom := float32(64)

	visW := max(viewW, 160) / zoom
	visH := max(viewH-hudTop-hudBottom, 160) / zoom
	hudCenterOffsetY := (hudTop - hudBottom) / (2 * zoom)

	if q.worldW <= visW {
		minX, maxX = 0, 0
	} else {
		halfSpanX := (q.worldW - visW) / 2
		minX, maxX = -halfSpanX, halfSpanX
	}
	if q.worldH <= visH {
		minY, maxY = -hudCenterOffsetY, -hudCenterOffsetY
	} else {
		halfSpanY := (q.worldH - visH) / 2
		minY = -halfSpanY - hudCenterOffsetY
		maxY = halfSpanY - hudCenterOffsetY
	}
	return minX, maxX, minY, maxY
}

func (s *Shell) stepGalleryCanvasPhysics(c *ui.Context, q galleryQuilt, viewW, viewH float32) {
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

	minX, maxX, minY, maxY := q.cameraBounds(viewW, viewH, st.zoom)

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

func (s *Shell) adjustGalleryCanvasZoom(q galleryQuilt, viewW, viewH, factor, anchorX, anchorY float32) {
	st := &s.gallery.canvas
	st.ensureInit()
	oldZoom := st.zoom
	nextZoom := clampFloat32(oldZoom*factor, galleryCanvasZoomMin, galleryCanvasZoomMax)
	if nextZoom == oldZoom {
		return
	}
	worldX := st.camX + (anchorX-viewW/2)/oldZoom
	worldY := st.camY + (anchorY-viewH/2)/oldZoom
	st.zoom = nextZoom
	st.camX = worldX - (anchorX-viewW/2)/nextZoom
	st.camY = worldY - (anchorY-viewH/2)/nextZoom
	st.velX, st.velY = 0, 0
	minX, maxX, minY, maxY := q.cameraBounds(viewW, viewH, st.zoom)
	st.camX = clampFloat32(st.camX, minX, maxX)
	st.camY = clampFloat32(st.camY, minY, maxY)
}

// galleryCanvasTileThumbEdge picks a quantized thumbnail tier proportional to
// the tile's rendered physical pixel size so 3x3 heroes decode crisply while
// 1x1 compact tiles stay lightweight.
func galleryCanvasTileThumbEdge(sw, sh, dpiScale float32) int {
	phys := max(sw, sh) * max(dpiScale, 1)
	switch {
	case phys > 560:
		return 768
	case phys > 360:
		return 512
	default:
		return galleryThumbEdgeL1
	}
}

// galleryInfiniteCanvas renders the seamless sharp-cornered multi-scale
// infinite gallery wall with O(visible) row-bucket culling, rubber-band finite
// edges, left-drag inertia panning, wheel scroll & Ctrl+wheel zoom.
func (s *Shell) galleryInfiniteCanvas(c *ui.Context, viewW, viewH float32, opaque bool, tone ui.Color) {
	st := &s.gallery.canvas
	st.ensureInit()
	assets := s.gallery.assets
	if len(assets) == 0 {
		return
	}
	quilt := st.ensureQuilt(assets, s.gallery.rowsRev)
	if st.cameraRev != s.gallery.rowsRev {
		st.recenter(quilt, viewW, viewH)
		st.cameraRev = s.gallery.rowsRev
	}
	s.stepGalleryCanvasPhysics(c, quilt, viewW, viewH)
	st.hoveredID = 0

	handleCanvasInput := func(ev ui.InputEvent, offsetX, offsetY float32, tileAsset *GalleryAsset, heartRect ui.Rect) bool {
		cx := ev.X + offsetX
		cy := ev.Y + offsetY
		switch ev.Kind {
		case ui.InputZoom:
			if ev.Scale > 0 && ev.Scale != 1 {
				s.adjustGalleryCanvasZoom(quilt, viewW, viewH, ev.Scale, cx, cy)
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
				s.adjustGalleryCanvasZoom(quilt, viewW, viewH, factor, cx, cy)
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
			minX, maxX, minY, maxY := quilt.cameraBounds(viewW, viewH, st.zoom)
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
				minX, maxX, minY, maxY := quilt.cameraBounds(viewW, viewH, st.zoom)
				wx := -dx / st.zoom
				wy := -dy / st.zoom
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

	// Visible row bucket window: tiles span at most 3 atomic rows, so scanning
	// [rowStart-3 .. rowEnd] is strictly O(visible) even for 10,000+ works.
	visMinY := st.camY - viewH/(2*st.zoom)
	visMaxY := st.camY + viewH/(2*st.zoom)
	rowStart := max(0, int(math.Floor(float64((visMinY-quilt.originY)/galleryCanvasUnitH)))-3)
	rowEnd := min(quilt.rows-1, int(math.Ceil(float64((visMaxY-quilt.originY)/galleryCanvasUnitH)))+1)

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
			dotColor := ui.RGBA(255, 255, 255, .04)
			for y := r.Y + offY; y < r.Y+r.H; y += dotStep {
				for x := r.X + offX; x < r.X+r.W; x += dotStep {
					p.Fill(ui.Rect{X: x, Y: y, W: 1.5, H: 1.5}, dotColor, 0.75)
				}
			}
		}
	}).Children(func() {
		for r := rowStart; r <= rowEnd; r++ {
			for _, tileIdx := range quilt.rowBuckets[r] {
				qt := quilt.tiles[tileIdx]
				if qt.assetIdx < 0 || qt.assetIdx >= len(assets) {
					continue
				}
				asset := assets[qt.assetIdx]
				wr := quilt.tileWorldRect(qt)
				// Snap both edges to integer DIPs so flush adjacent tiles never show subpixel cracks.
				sx := float32(math.Round(float64(viewW/2 + (wr.X-st.camX)*st.zoom)))
				sy := float32(math.Round(float64(viewH/2 + (wr.Y-st.camY)*st.zoom)))
				sx1 := float32(math.Round(float64(viewW/2 + (wr.X+wr.W-st.camX)*st.zoom)))
				sy1 := float32(math.Round(float64(viewH/2 + (wr.Y+wr.H-st.camY)*st.zoom)))
				sw := max(sx1-sx, 1)
				sh := max(sy1-sy, 1)
				thumbEdge := galleryCanvasTileThumbEdge(sw, sh, s.gallery.dpiScale)

				if sx+sw < -40 || sx > viewW+40 || sy+sh < -40 || sy > viewH+40 {
					if sx+sw >= -320 && sx <= viewW+320 && sy+sh >= -240 && sy <= viewH+240 {
						s.prefetchGalleryThumb(asset, thumbEdge)
					}
					continue
				}
				s.galleryCanvasTile(c, asset, sx, sy, sw, sh, thumbEdge, opaque, tone, handleCanvasInput)
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

	// Scale corner controls cleanly to match 1x1 compact vs 2x2/3x3 feature tiles.
	compact := sw < 240
	heartSize := float32(28)
	heartInset := float32(8)
	heartIconSize := float32(13)
	if compact {
		heartSize = 22
		heartInset = 6
		heartIconSize = 11
	}
	heartRect := ui.Rect{X: sw - heartSize - heartInset, Y: heartInset, W: heartSize, H: heartSize}

	// Flush seamless photographic tile: sharp 90° corners, zero gap, zero shadow.
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
			iconSz := float32(26)
			if compact {
				iconSz = 20
			}
			ui.Column(c).Fill().Justify(ui.Center).AlignItems(ui.Center).Gap(5).PassThrough().Children(func() {
				ui.Box(c).TextColor(tone.Alpha(.85)).Children(func() { glyph(c, "\uE714", iconSz) })
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
				ui.Text(c, placeholder).FontSize(11).TextColor(muted).SingleLine()
			})
		}

		// Calm hover scrim with filename & resolution — absent at rest so the wall breathes.
		if hovered && sw >= 140 {
			scrimH := float32(52)
			if compact {
				scrimH = 40
			}
			ui.Box(c).Absolute().Left(0).Right(0).Bottom(0).Height(scrimH).PassThrough().LinearGradient(ui.LinearGradient{
				Angle: 180,
				Stops: []ui.GradientStop{
					{Color: ui.Transparent, At: 0},
					{Color: ui.RGBA(8, 9, 13, .84), At: 1},
				},
			})
			ui.Row(c).Absolute().Left(8).Right(8).Bottom(7).AlignItems(ui.Center).Gap(6).PassThrough().Children(func() {
				name := asset.FileName
				if name == "" {
					name = "未命名作品"
				}
				ui.Text(c, name).FontSize(11).FontWeight(600).TextColor(paper).SingleLine().Grow(1).Shrink(1)
				if !compact && !asset.IsVideo() && asset.Width > 0 && asset.Height > 0 {
					mono(c, fmt.Sprintf("%dx%d", asset.Width, asset.Height), 10).TextColor(muted)
				}
			})
		}

		// Minimal corner indicators: error/missing always visible; archive/tags appear subtly on hover.
		showStatus := asset.Invalid || !asset.Available || (hovered && sw >= 170)
		if showStatus {
			badgeText, badgeBg, badgeFg := galleryStatusBadge(asset, tone, opaque, s.wishWarningColor())
			if badgeText != "" {
				ui.Row(c).Absolute().Left(8).Top(8).Height(20).Padding(0, 7).Radius(4).
					AlignItems(ui.Center).Background(badgeBg).PassThrough().Children(func() {
					ui.Text(c, badgeText).FontSize(10).FontWeight(600).TextColor(badgeFg).SingleLine()
				})
			}
		} else if asset.HasArchived && sw >= 120 {
			ui.Box(c).Absolute().Left(8).Top(8).Size(5, 5).Radius(2.5).Background(tone).PassThrough()
		}

		// Video duration badge stays legible at rest so videos stand out from stills.
		if asset.IsVideo() && sw >= 110 {
			durText := "视频"
			if asset.DurationMS > 0 {
				durText = formatGalleryDuration(asset.DurationMS)
			}
			bottomPos := float32(8)
			if hovered && sw >= 140 {
				bottomPos = 28
			}
			pillH := float32(20)
			if compact {
				pillH = 18
			}
			ui.Row(c).Absolute().Right(8).Bottom(bottomPos).Height(pillH).Padding(0, 6).Radius(4).Gap(4).
				AlignItems(ui.Center).Background(ui.RGBA(8, 9, 13, .80)).PassThrough().Children(func() {
				ui.Box(c).TextColor(tone).Children(func() { glyph(c, "\uE768", 8) })
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
			ui.Row(c).Absolute().Right(heartInset).Top(heartInset).Size(22, 22).Radius(11).
				Justify(ui.Center).AlignItems(ui.Center).Background(checkBg).Border(1.5, checkBorder).PassThrough().Children(func() {
				if selected {
					ui.Box(c).TextColor(ink).Children(func() { glyph(c, "\uE73E", 12) })
				}
			})
		} else if !asset.Invalid && (asset.Favorited || hovered) && sw >= 110 {
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
				glyph(c, iconKey, heartIconSize)
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
