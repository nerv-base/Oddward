// SPDX-License-Identifier: GPL-3.0-or-later
package gamemedia

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nerv-base/Oddward/internal/imagecheck"
)

const maxIndexBytes = 1 << 20
const maxEntries = 4096

type entry struct {
	Key          string    `json:"key"`
	ETag         string    `json:"etag"`
	LastModified string    `json:"last_modified"`
	FetchedAt    time.Time `json:"fetched_at"`
	ExpiresAt    time.Time `json:"expires_at"`
	Size         int64     `json:"size"`
	LastUsed     time.Time `json:"last_used"`
}

var errInvalidIcon = errors.New("gamemedia: invalid cached image")

func (c *Cache) load(ctx context.Context) error {
	if err := checkLocalPath(ctx, c.dir, true); err != nil {
		return err
	}
	if err := os.MkdirAll(c.dir, 0700); err != nil {
		return err
	}
	if err := checkLocalPath(ctx, c.dir, true); err != nil {
		return err
	}
	if c.loaded {
		return ctx.Err()
	}
	name := filepath.Join(c.dir, "index.json")
	if err := checkLocalPath(ctx, name, false); err != nil {
		return err
	}
	f, err := os.Open(name)
	if errors.Is(err, os.ErrNotExist) {
		c.loaded = true
		return ctx.Err()
	}
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := imagecheck.ReadBounded(f, maxIndexBytes)
	if err == nil {
		if items, decodeErr := decodeIndex(data); decodeErr == nil {
			c.entries = items
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// A corrupt/oversized document is an empty index, never a deletion instruction.
	c.loaded = true
	return nil
}
func decodeIndex(data []byte) (map[string]entry, error) {
	if !utf8.Valid(data) || len(data) > maxIndexBytes {
		return nil, errors.New("gamemedia: invalid index bytes")
	}
	tokens := json.NewDecoder(bytes.NewReader(data))
	if err := uniqueJSON(tokens); err != nil {
		return nil, err
	}
	if _, err := tokens.Token(); err != io.EOF {
		return nil, errors.New("gamemedia: trailing index data")
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var items []entry
	if err := dec.Decode(&items); err != nil {
		return nil, err
	}
	if items == nil || len(items) > maxEntries {
		return nil, errors.New("gamemedia: invalid index array")
	}
	result := make(map[string]entry, len(items))
	for _, e := range items {
		if !validFilename(e.Key) || len(e.ETag) > 4096 || len(e.LastModified) > 4096 || e.Size < 0 || e.Size > maxIconBytes || e.FetchedAt.IsZero() || e.ExpiresAt.IsZero() || !e.ExpiresAt.After(e.FetchedAt) || e.LastUsed.IsZero() {
			return nil, errors.New("gamemedia: invalid index entry")
		}
		if _, duplicate := result[e.Key]; duplicate {
			return nil, errors.New("gamemedia: duplicate index key")
		}
		result[e.Key] = e
	}
	return result, nil
}

// uniqueJSON rejects duplicate object names (including nested objects), which
// encoding/json otherwise silently accepts even with DisallowUnknownFields.
func uniqueJSON(d *json.Decoder) error {
	token, err := d.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		fields := make(map[string]struct{})
		for d.More() {
			key, err := d.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return errors.New("gamemedia: invalid object key")
			}
			if _, exists := fields[name]; exists {
				return errors.New("gamemedia: duplicate JSON field")
			}
			fields[name] = struct{}{}
			if err := uniqueJSON(d); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err := uniqueJSON(d); err != nil {
				return err
			}
		}
	default:
		return errors.New("gamemedia: unexpected JSON delimiter")
	}
	end, err := d.Token()
	if err != nil {
		return err
	}
	if (delimiter == '{' && end != json.Delim('}')) || (delimiter == '[' && end != json.Delim(']')) {
		return errors.New("gamemedia: unclosed JSON container")
	}
	return nil
}
func validFilename(name string) bool {
	for _, kind := range []string{"avatar", "fork"} {
		prefix := kind + "-"
		if strings.HasPrefix(name, prefix) && strings.HasSuffix(name, ".png") {
			id := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".png")
			k, err := KeyFor(id)
			return err == nil && k.Kind == kind && k.filename() == name
		}
	}
	return false
}
func (c *Cache) readIcon(ctx context.Context, name string) ([]byte, error) {
	path := filepath.Join(c.dir, name)
	if err := checkLocalPath(ctx, path, false); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := imagecheck.ReadBounded(f, maxIconBytes)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errInvalidIcon, err)
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if err = checkIcon(data); err != nil {
		return nil, fmt.Errorf("%w: %w", errInvalidIcon, err)
	}
	return data, nil
}
func (c *Cache) removeIcon(ctx context.Context, name string) error {
	if !validFilename(name) {
		return errors.New("gamemedia: refusing to remove non-icon file")
	}
	path := filepath.Join(c.dir, name)
	if err := checkLocalPath(ctx, path, false); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
func (c *Cache) saveIndex(ctx context.Context) error {
	items := make([]entry, 0, len(c.entries))
	for _, e := range c.entries {
		items = append(items, e)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Key < items[j].Key })
	data, err := json.Marshal(items)
	if err != nil {
		return err
	}
	if len(data) > maxIndexBytes {
		return errors.New("gamemedia: index exceeds byte budget")
	}
	return c.atomicWrite(ctx, "index.json", data)
}
func (c *Cache) atomicWrite(ctx context.Context, name string, data []byte) (result error) {
	if name != "index.json" && !validFilename(name) {
		return errors.New("gamemedia: invalid write target")
	}
	target := filepath.Join(c.dir, name)
	if err := checkLocalPath(ctx, target, false); err != nil {
		return err
	}
	stage, err := os.CreateTemp(c.dir, ".gamemedia-*.tmp")
	if err != nil {
		return err
	}
	stageName := stage.Name()
	committed := false
	defer func() {
		_ = stage.Close()
		if !committed {
			if err := os.Remove(stageName); err != nil && !errors.Is(err, os.ErrNotExist) {
				result = errors.Join(result, fmt.Errorf("remove owned icon stage: %w", err))
			}
		}
	}()
	if _, err = stage.Write(data); err != nil {
		return err
	}
	if err = stage.Sync(); err != nil {
		return err
	}
	if err = stage.Close(); err != nil {
		return err
	}
	if err = checkLocalPath(ctx, stageName, false); err != nil {
		return err
	}
	if err = checkLocalPath(ctx, target, false); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = replaceFile(stageName, target); err != nil {
		return err
	}
	committed = true
	return nil
}
func (c *Cache) evict(ctx context.Context) error {
	// Count actual owned PNG files, including orphans left by an interrupted
	// commit or a discarded index. Index corruption cannot disable the budget.
	candidates := make(map[string]entry, len(c.entries))
	for name, e := range c.entries {
		e.Size = 0
		candidates[name] = e
	}
	dir, err := os.Open(c.dir)
	if err != nil {
		return err
	}
	defer dir.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		files, readErr := dir.ReadDir(128)
		for _, file := range files {
			name := file.Name()
			if !validFilename(name) {
				continue
			}
			if err := checkLocalPath(ctx, filepath.Join(c.dir, name), false); err != nil {
				return err
			}
			info, err := file.Info()
			if err != nil {
				return err
			}
			e := candidates[name]
			e.Key = name
			e.Size = info.Size()
			candidates[name] = e
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return readErr
		}
	}
	var total int64
	items := make([]entry, 0, len(candidates))
	for _, e := range candidates {
		total += e.Size
		items = append(items, e)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].LastUsed.Equal(items[j].LastUsed) {
			return items[i].Key < items[j].Key
		}
		return items[i].LastUsed.Before(items[j].LastUsed)
	})
	for _, e := range items {
		if total <= c.maxBytes && len(c.entries) <= maxEntries {
			break
		}
		if e.Size > 0 {
			if err := c.removeIcon(ctx, e.Key); err != nil {
				return err
			}
		}
		delete(c.entries, e.Key)
		total -= e.Size
	}
	return ctx.Err()
}
