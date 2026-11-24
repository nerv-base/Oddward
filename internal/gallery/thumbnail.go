// SPDX-License-Identifier: GPL-3.0-or-later

package gallery

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/draw"
)

// Tiered thumbnail edges per PRD §13.1.
const (
	ThumbL0 = 128  // fast placeholder / small tiles
	ThumbL1 = 384  // main gallery grid
	ThumbL2 = 1024 // quick preview / initial viewer frame

	thumbMagic          = "ODGT"
	thumbFormatVersion  = 1
	thumbDecoderVersion = 1
	thumbHeaderSize     = 20 // magic(4) + fmtVer(1) + decVer(1) + edge(2) + w(2) + h(2) + savedAt(8)
	thumbMaxEdge        = 1536
	thumbMaxFileBytes   = thumbHeaderSize + 4*thumbMaxEdge*thumbMaxEdge
	thumbPruneEvery     = 64
	thumbClockSkew      = 24 * time.Hour
)

// ThumbCache is a content-addressed, disk-budgeted decoded RGBA thumbnail store.
// Keys are (sha256, edge, decoderVersion). Clearing or pruning the cache never
// touches original or archived photos.
type ThumbCache struct {
	dir string

	mu          sync.Mutex
	budgetBytes int64
	saves       int
	now         func() time.Time
}

// NewThumbCache creates a disk thumbnail cache rooted at dir with budgetMB MiB.
// Returns nil when dir is empty.
func NewThumbCache(dir string, budgetMB int) *ThumbCache {
	if dir == "" {
		return nil
	}
	if budgetMB < MinThumbCacheMB {
		budgetMB = DefaultThumbCacheMB
	}
	return &ThumbCache{
		dir:         filepath.Clean(dir),
		budgetBytes: int64(budgetMB) << 20,
		now:         time.Now,
	}
}

// SetBudgetMB updates the disk cache eviction budget in MiB.
func (c *ThumbCache) SetBudgetMB(budgetMB int) {
	if c == nil {
		return
	}
	if budgetMB < MinThumbCacheMB {
		budgetMB = DefaultThumbCacheMB
	}
	if budgetMB > MaxThumbCacheMB {
		budgetMB = MaxThumbCacheMB
	}
	c.mu.Lock()
	c.budgetBytes = int64(budgetMB) << 20
	c.mu.Unlock()
}

func (c *ThumbCache) timestamp() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *ThumbCache) thumbPath(sha256Hex string, edge int) (string, error) {
	if !ValidSHA256Hex(sha256Hex) || edge <= 0 || edge > thumbMaxEdge {
		return "", ErrInvalidMedia
	}
	lower := strings.ToLower(sha256Hex)
	return filepath.Join(c.dir, lower[:2], fmt.Sprintf("%s-%d-v%d.odgt", lower, edge, thumbDecoderVersion)), nil
}

// Load reads a validated cached thumbnail for (sha256Hex, edge) or returns nil.
func (c *ThumbCache) Load(ctx context.Context, sha256Hex string, edge int) *image.RGBA {
	if c == nil || ctx.Err() != nil {
		return nil
	}
	path, err := c.thumbPath(sha256Hex, edge)
	if err != nil {
		return nil
	}
	if _, err := checkLocalPath(ctx, path, false, false); err != nil {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, thumbMaxFileBytes+1))
	if err != nil || len(data) > thumbMaxFileBytes {
		return nil
	}
	return parseGalleryThumb(data, edge, c.timestamp())
}

// Save atomically writes a decoded thumbnail to disk and periodically prunes
// oldest files when disk usage exceeds the configured budget.
func (c *ThumbCache) Save(ctx context.Context, sha256Hex string, edge int, pixels *image.RGBA) error {
	if c == nil || pixels == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	target, err := c.thumbPath(sha256Hex, edge)
	if err != nil {
		return err
	}
	b := pixels.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 || w > edge || h > edge || pixels.Rect != image.Rect(0, 0, w, h) || pixels.Stride != 4*w || len(pixels.Pix) < 4*w*h {
		return errors.New("gallery: invalid thumbnail RGBA bounds")
	}

	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if _, err := checkLocalPath(ctx, dir, true, false); err != nil {
		return err
	}

	data := make([]byte, thumbHeaderSize, thumbHeaderSize+4*w*h)
	copy(data[0:4], thumbMagic)
	data[4] = thumbFormatVersion
	data[5] = thumbDecoderVersion
	binary.LittleEndian.PutUint16(data[6:8], uint16(edge))
	binary.LittleEndian.PutUint16(data[8:10], uint16(w))
	binary.LittleEndian.PutUint16(data[10:12], uint16(h))
	binary.LittleEndian.PutUint64(data[12:20], uint64(c.timestamp().Unix()))
	data = append(data, pixels.Pix[:4*w*h]...)

	stage, err := os.CreateTemp(dir, ".thumb-*.tmp")
	if err != nil {
		return err
	}
	stagePath := stage.Name()
	committed := false
	defer func() {
		_ = stage.Close()
		if !committed {
			_ = os.Remove(stagePath)
		}
	}()
	if _, err := stage.Write(data); err != nil {
		return err
	}
	if err := stage.Close(); err != nil {
		return err
	}
	if err := replaceFile(stagePath, target); err != nil {
		return err
	}
	committed = true

	c.mu.Lock()
	c.saves++
	shouldPrune := c.saves%thumbPruneEvery == 0
	budget := c.budgetBytes
	c.mu.Unlock()
	if shouldPrune {
		_ = c.Prune(budget)
	}
	return nil
}

// LoadOrDecode returns a cached thumbnail when present, otherwise decodes and
// resizes sourcePath off the UI thread and stores the result in the disk cache.
func (c *ThumbCache) LoadOrDecode(ctx context.Context, sha256Hex, sourcePath string, edge int) (*image.RGBA, bool, error) {
	if pixels := c.Load(ctx, sha256Hex, edge); pixels != nil {
		return pixels, true, nil
	}
	pixels, err := DecodeScaled(ctx, sourcePath, edge)
	if err != nil {
		return nil, false, err
	}
	if c != nil && ValidSHA256Hex(sha256Hex) {
		_ = c.Save(ctx, sha256Hex, edge, pixels)
	}
	return pixels, false, nil
}

// UsageBytes returns the total bytes currently occupied by managed .odgt files.
func (c *ThumbCache) UsageBytes() int64 {
	if c == nil {
		return 0
	}
	var total int64
	_ = filepath.WalkDir(c.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".odgt") {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// Prune deletes oldest managed thumbnail files until total size <= targetBytes.
func (c *ThumbCache) Prune(targetBytes int64) error {
	if c == nil {
		return nil
	}
	type item struct {
		path string
		size int64
		mod  time.Time
	}
	var files []item
	var total int64
	_ = filepath.WalkDir(c.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".odgt") {
			return nil
		}
		if info, ierr := d.Info(); ierr == nil {
			files = append(files, item{path: path, size: info.Size(), mod: info.ModTime()})
			total += info.Size()
		}
		return nil
	})
	if total <= targetBytes {
		return nil
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for _, f := range files {
		if total <= targetBytes {
			break
		}
		if err := os.Remove(f.path); err == nil {
			total -= f.size
		}
	}
	return nil
}

// Clear removes all managed thumbnail files without touching anything outside
// the thumbnail cache directory.
func (c *ThumbCache) Clear() error {
	if c == nil {
		return nil
	}
	if _, err := os.Lstat(c.dir); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if _, err := checkLocalPath(context.Background(), c.dir, true, false); err != nil {
		return err
	}
	return filepath.WalkDir(c.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasSuffix(d.Name(), ".odgt") || (strings.HasPrefix(d.Name(), ".thumb-") && strings.HasSuffix(d.Name(), ".tmp")) {
			_ = os.Remove(path)
		}
		return nil
	})
}

func parseGalleryThumb(data []byte, edge int, now time.Time) *image.RGBA {
	if len(data) < thumbHeaderSize || string(data[0:4]) != thumbMagic || data[4] != thumbFormatVersion || data[5] != thumbDecoderVersion {
		return nil
	}
	storedEdge := int(binary.LittleEndian.Uint16(data[6:8]))
	w := int(binary.LittleEndian.Uint16(data[8:10]))
	h := int(binary.LittleEndian.Uint16(data[10:12]))
	savedUnix := int64(binary.LittleEndian.Uint64(data[12:20]))
	if storedEdge != edge || w <= 0 || h <= 0 || w > edge || h > edge {
		return nil
	}
	saved := time.Unix(savedUnix, 0)
	if saved.After(now.Add(thumbClockSkew)) {
		return nil
	}
	payload := data[thumbHeaderSize:]
	if len(payload) != 4*w*h {
		return nil
	}
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	copy(out.Pix, payload)
	return out
}

// DecodeScaled safely validates and decodes path into an upright RGBA image
// scaled to fit within maxEdge (0 preserves full original resolution).
func DecodeScaled(ctx context.Context, path string, maxEdge int) (*image.RGBA, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	clean, err := checkLocalPath(ctx, path, false, false)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(clean)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := statOpenFile(f)
	if err != nil {
		return nil, err
	}
	if st.Reparse || st.IsDir {
		return nil, ErrUnsafePath
	}
	if st.Size <= 0 {
		return nil, ErrInvalidMedia
	}
	if st.Size > MaxImageBytes {
		return nil, ErrTooLarge
	}

	raw, err := io.ReadAll(io.LimitReader(f, MaxImageBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > MaxImageBytes {
		return nil, ErrTooLarge
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	info, err := InspectReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, err
	}

	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidMedia, err)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	rgba := toRGBAWithOrientation(img, info.Orientation)
	if maxEdge <= 0 {
		return rgba, nil
	}
	b := rgba.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= maxEdge && h <= maxEdge {
		return rgba, nil
	}
	dstW, dstH := maxEdge, maxEdge
	if w >= h {
		dstH = max(1, h*maxEdge/w)
	} else {
		dstW = max(1, w*maxEdge/h)
	}
	scaled := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	// BiLinear offers crisp downsampling at ~4x the speed of CatmullRom for
	// 1080p/1440p gallery thumbnails.
	draw.BiLinear.Scale(scaled, scaled.Bounds(), rgba, rgba.Bounds(), draw.Src, nil)
	return scaled, nil
}

// ReadClipboardPNG returns verified PNG bytes suitable for Windows clipboard
// image writing. Already-valid upright PNGs are returned directly without
// re-encoding; other formats are decoded upright and encoded as PNG.
func ReadClipboardPNG(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	clean, err := checkLocalPath(ctx, path, false, false)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(clean)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := statOpenFile(f)
	if err != nil {
		return nil, err
	}
	if st.Reparse || st.IsDir {
		return nil, ErrUnsafePath
	}
	if st.Size <= 0 || st.Size > MaxImageBytes {
		return nil, ErrTooLarge
	}
	raw, err := io.ReadAll(io.LimitReader(f, MaxImageBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > MaxImageBytes {
		return nil, ErrTooLarge
	}
	info, err := InspectReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		return nil, err
	}
	if info.MIMEType == "image/png" {
		return raw, nil
	}
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidMedia, err)
	}
	rgba := toRGBAWithOrientation(img, info.Orientation)
	var out bytes.Buffer
	if err := png.Encode(&out, rgba); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

func toRGBAWithOrientation(src image.Image, orient int) *image.RGBA {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	var rgba *image.RGBA
	if direct, ok := src.(*image.RGBA); ok && direct.Rect.Min == (image.Point{}) && direct.Stride == 4*w {
		rgba = direct
	} else {
		rgba = image.NewRGBA(image.Rect(0, 0, w, h))
		draw.Copy(rgba, image.Point{}, src, b, draw.Src, nil)
	}
	if orient <= 1 || orient > 8 {
		return rgba
	}
	dstW, dstH := w, h
	if orient >= 5 && orient <= 8 {
		dstW, dstH = h, w
	}
	dst := image.NewRGBA(image.Rect(0, 0, dstW, dstH))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			var dx, dy int
			switch orient {
			case 2:
				dx, dy = w-1-x, y
			case 3:
				dx, dy = w-1-x, h-1-y
			case 4:
				dx, dy = x, h-1-y
			case 5:
				dx, dy = y, x
			case 6:
				dx, dy = h-1-y, x
			case 7:
				dx, dy = h-1-y, w-1-x
			case 8:
				dx, dy = y, w-1-x
			}
			si := y*rgba.Stride + x*4
			di := dy*dst.Stride + dx*4
			copy(dst.Pix[di:di+4], rgba.Pix[si:si+4])
		}
	}
	return dst
}
