// SPDX-License-Identifier: GPL-3.0-or-later
package gamemedia

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/nerv-base/Oddward/internal/imagecheck"
)

var errInvalidIcon = errors.New("gamemedia: invalid cached image")

// ensureDir checks and creates the cache directory once per process. Every file
// access still checks its own path, so a directory swapped for a link later is
// caught when it matters without paying for an ancestor walk on every icon.
func (c *Cache) ensureDir(ctx context.Context) error {
	c.dirMu.Lock()
	defer c.dirMu.Unlock()
	if c.dirReady {
		return ctx.Err()
	}
	if err := checkLocalPath(ctx, c.dir, true); err != nil {
		return err
	}
	if err := os.MkdirAll(c.dir, 0700); err != nil {
		return err
	}
	if err := checkLocalPath(ctx, c.dir, true); err != nil {
		return err
	}
	c.dirReady = true
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

// readIcon returns the validated bytes of one owned file. os.ErrNotExist and
// errInvalidIcon mean "no usable file"; any other error (a path the guard cannot
// vouch for, a permission problem) is reported to the caller.
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

// atomicWrite stages the image beside its destination and replaces the name in
// one call, so a concurrent reader never sees a partial PNG. Only owned icon
// names are valid targets: the cache has no other file of its own.
func (c *Cache) atomicWrite(ctx context.Context, name string, data []byte) (result error) {
	if !validFilename(name) {
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
