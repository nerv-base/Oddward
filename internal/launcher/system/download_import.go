// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nerv-base/Oddward/internal/gamedl"
	"github.com/nerv-base/Oddward/internal/gameinstall"
)

// ImportLegacy adopts the download, install and preload records an older build kept
// as files. It runs once per start before any transfer can begin, so a paused task,
// a half-committed installation or a staged preload is still there afterwards.
func (b *GameDownloadBackend) ImportLegacy(ctx context.Context) error {
	installs, err := b.installStore()
	if err != nil {
		return err
	}
	if _, err := b.DB.RunImport(ctx, gameinstall.InstallDomain, gameinstall.InstallImportVersion, installs.ImportLegacy); err != nil {
		return err
	}
	root := filepath.Join(b.StateRoot, "installs")
	if err := gamedl.ImportLegacy(ctx, b.DB, root); err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("download state %s: %w", root, err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		dir := filepath.Join(root, entry.Name())
		if err := gameinstall.ImportPreloadLegacy(ctx, b.DB, dir); err != nil {
			return err
		}
	}
	return nil
}
