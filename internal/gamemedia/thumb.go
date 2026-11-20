// SPDX-License-Identifier: GPL-3.0-or-later
package gamemedia

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// A thumbnail is a decoded icon already scaled to the edge the view draws, so a
// restart loads one small file instead of decoding the source image and
// resampling it again. Thumbnails have no index and no database row: the file
// name is the key (a hash of the item id, so an id can never escape the
// directory) and a fixed header carries the edge, the pixel size and the save
// time a reader has to validate before trusting the payload.
const (
	thumbMagic      = "ODWI"
	thumbVersion    = 1
	thumbHeaderSize = 19 // magic, version, edge, width, height, saved-at
	thumbMaxEdge    = 512
	// thumbFileMaxBytes is the largest thumbnail this cache writes. A bigger
	// file cannot be one of ours and is dropped without reading it whole.
	thumbFileMaxBytes = thumbHeaderSize + 4*thumbMaxEdge*thumbMaxEdge
	// thumbTargetBytes bounds the directory: every thumbPruneEvery saves the
	// cache drops its oldest files back to this size.
	thumbTargetBytes = 24 << 20
	thumbTTL         = 30 * 24 * time.Hour
	// thumbPruneEvery is the number of saves between directory scans.
	thumbPruneEvery = 128
	// thumbClockSkew tolerates a save time slightly ahead of the reader.
	thumbClockSkew = 24 * time.Hour
)

// ThumbCache is a bounded, best-effort file store of decoded thumbnails. Load
// returns nil for anything it cannot fully validate: a truncated or foreign
// file, a size that no longer matches, or an entry older than thumbTTL. Missing
// entries are simply decoded again, so the cache never has to be repaired.
type ThumbCache struct {
	dir string

	mu    sync.Mutex
	ready bool
	saves int
	// now is overridable in tests; production uses time.Now.
	now func() time.Time
}

// NewThumbCache returns a cache rooted at dir, or nil when dir is empty (thumb
// caching disabled).
func NewThumbCache(dir string) *ThumbCache {
	if dir == "" {
		return nil
	}
	return &ThumbCache{dir: dir}
}

func (c *ThumbCache) timestamp() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// Load returns the pixels stored for id at the requested edge, or nil.
func (c *ThumbCache) Load(id string, edge int) *image.RGBA {
	if c == nil || id == "" || edge <= 0 || edge > thumbMaxEdge {
		return nil
	}
	path := filepath.Join(c.dir, thumbName(id, edge))
	if err := checkLocalPath(context.Background(), path, false); err != nil {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, thumbFileMaxBytes+1))
	if err != nil || len(data) > thumbFileMaxBytes {
		return nil
	}
	return parseThumb(data, edge, c.timestamp())
}

// Save writes one thumbnail. The caller keeps ownership of pixels; the payload
// is copied into the file. Failures are reported but never fatal: the icon is
// already decoded and only the next restart loses the shortcut.
func (c *ThumbCache) Save(id string, edge int, pixels *image.RGBA) error {
	if c == nil {
		return errors.New("gamemedia: thumbnail cache disabled")
	}
	if id == "" || pixels == nil {
		return errors.New("gamemedia: invalid thumbnail")
	}
	bounds := pixels.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if edge <= 0 || edge > thumbMaxEdge || w <= 0 || h <= 0 || w > edge || h > edge ||
		pixels.Rect != image.Rect(0, 0, w, h) || pixels.Stride != 4*w || len(pixels.Pix) < 4*w*h {
		return errors.New("gamemedia: invalid thumbnail size")
	}
	data := make([]byte, thumbHeaderSize, thumbHeaderSize+4*w*h)
	copy(data[0:4], thumbMagic)
	data[4] = thumbVersion
	binary.LittleEndian.PutUint16(data[5:7], uint16(edge))
	binary.LittleEndian.PutUint16(data[7:9], uint16(w))
	binary.LittleEndian.PutUint16(data[9:11], uint16(h))
	binary.LittleEndian.PutUint64(data[11:19], uint64(c.timestamp().Unix()))
	data = append(data, pixels.Pix[:4*w*h]...)
	if err := c.write(thumbName(id, edge), data); err != nil {
		return err
	}
	c.mu.Lock()
	c.saves++
	prune := c.saves%thumbPruneEvery == 0
	c.mu.Unlock()
	if prune {
		return c.pruneTo(thumbTargetBytes)
	}
	return nil
}

// pruneTo deletes the oldest thumbnails until the directory holds at most
// target bytes. It only ever removes files this cache named, so a foreign file
// in the directory is ignored rather than counted or deleted.
func (c *ThumbCache) pruneTo(target int64) error {
	if c == nil {
		return nil
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	type entry struct {
		path string
		size int64
		mod  time.Time
	}
	files := make([]entry, 0, len(entries))
	var total int64
	for _, item := range entries {
		if item.IsDir() || !validThumbName(item.Name()) {
			continue
		}
		info, err := item.Info()
		if err != nil {
			continue
		}
		files = append(files, entry{filepath.Join(c.dir, item.Name()), info.Size(), info.ModTime()})
		total += info.Size()
	}
	if total <= target {
		return nil
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for _, file := range files {
		if total <= target {
			break
		}
		if err := os.Remove(file.path); err != nil && !errors.Is(err, os.ErrNotExist) {
			continue
		}
		total -= file.size
	}
	return nil
}

// write stages the file beside its destination and renames it into place, so a
// reader never sees a partial payload.
func (c *ThumbCache) write(name string, data []byte) (result error) {
	c.mu.Lock()
	if !c.ready {
		if err := os.MkdirAll(c.dir, 0700); err != nil {
			c.mu.Unlock()
			return err
		}
		if err := checkLocalPath(context.Background(), c.dir, true); err != nil {
			c.mu.Unlock()
			return err
		}
		c.ready = true
	}
	c.mu.Unlock()
	target := filepath.Join(c.dir, name)
	if err := checkLocalPath(context.Background(), target, false); err != nil {
		return err
	}
	stage, err := os.CreateTemp(c.dir, ".thumbnail-*.tmp")
	if err != nil {
		return err
	}
	stageName := stage.Name()
	committed := false
	defer func() {
		_ = stage.Close()
		if !committed {
			if err := os.Remove(stageName); err != nil && !errors.Is(err, os.ErrNotExist) {
				result = errors.Join(result, fmt.Errorf("remove thumbnail stage: %w", err))
			}
		}
	}()
	if _, err := stage.Write(data); err != nil {
		return err
	}
	if err := stage.Close(); err != nil {
		return err
	}
	if err := checkLocalPath(context.Background(), stageName, false); err != nil {
		return err
	}
	if err := replaceFile(stageName, target); err != nil {
		return err
	}
	committed = true
	return nil
}

// parseThumb validates a stored thumbnail before its pixels are used.
func parseThumb(data []byte, edge int, now time.Time) *image.RGBA {
	if len(data) < thumbHeaderSize || string(data[0:4]) != thumbMagic || data[4] != thumbVersion {
		return nil
	}
	storedEdge := int(binary.LittleEndian.Uint16(data[5:7]))
	w := int(binary.LittleEndian.Uint16(data[7:9]))
	h := int(binary.LittleEndian.Uint16(data[9:11]))
	savedAt := int64(binary.LittleEndian.Uint64(data[11:19]))
	if storedEdge != edge || storedEdge <= 0 || storedEdge > thumbMaxEdge {
		return nil
	}
	if w <= 0 || h <= 0 || w > edge || h > edge {
		return nil
	}
	saved := time.Unix(savedAt, 0)
	if now.Sub(saved) > thumbTTL || saved.After(now.Add(thumbClockSkew)) {
		return nil
	}
	payload := data[thumbHeaderSize:]
	if len(payload) != 4*w*h {
		return nil
	}
	pixels := image.NewRGBA(image.Rect(0, 0, w, h))
	copy(pixels.Pix, payload)
	return pixels
}

func thumbName(id string, edge int) string {
	sum := sha256.Sum256([]byte(id))
	return fmt.Sprintf("thumb-%x-%d.odwi", sum[:16], edge)
}

// validThumbName is the ownership proof used before deleting a file.
func validThumbName(name string) bool {
	if !strings.HasPrefix(name, "thumb-") || !strings.HasSuffix(name, ".odwi") {
		return false
	}
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(name, "thumb-"), ".odwi"), "-")
	if len(parts) != 2 || len(parts[0]) != 32 {
		return false
	}
	for _, r := range parts[0] {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	edge, err := strconv.Atoi(parts[1])
	return err == nil && edge > 0 && edge <= thumbMaxEdge
}
