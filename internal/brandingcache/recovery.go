// SPDX-License-Identifier: GPL-3.0-or-later

package brandingcache

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nerv-base/Oddward/internal/imagecheck"
)

func (c *Cache) recover(ctx context.Context) error {
	journalPath := filepath.Join(c.dir, "transaction.json")
	data, err := readRegularBounded(journalPath, maxTransactionBytes)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("brandingcache: read transaction journal: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var tx transaction
	if err := json.Unmarshal(data, &tx); err != nil {
		return fmt.Errorf("brandingcache: decode transaction journal: %w", err)
	}
	if len(tx.Image) > maxImageBytes || len(tx.Metadata) > maxMetadataBytes {
		return errors.New("brandingcache: transaction journal exceeds size limits")
	}
	if err := restoreBytes(ctx, filepath.Join(c.dir, "slogan.png"), tx.ImagePresent, tx.Image); err != nil {
		return fmt.Errorf("brandingcache: restore slogan image: %w", err)
	}
	if err := restoreBytes(ctx, filepath.Join(c.dir, "metadata.json"), tx.MetadataPresent, tx.Metadata); err != nil {
		return fmt.Errorf("brandingcache: restore metadata: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Remove(journalPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("brandingcache: remove transaction journal: %w", err)
	}
	return nil
}

func restoreBytes(ctx context.Context, path string, present bool, data []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !present {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	stage, err := stageFile(filepath.Dir(path), "branding-recovery-", data, ctx)
	if err != nil {
		return err
	}
	defer os.Remove(stage)
	return replaceFile(stage, path)
}

func readRegularBounded(path string, limit int64) ([]byte, error) {
	f, err := openRegular(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return imagecheck.ReadBounded(f, limit)
}
