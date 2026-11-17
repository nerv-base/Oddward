// SPDX-License-Identifier: GPL-3.0-or-later
package system

import (
	"context"
	"errors"
	"github.com/nerv-base/Oddward/internal/gacha"
	"github.com/nerv-base/Oddward/internal/launcher"
	"os"
)

// MergeSnapshot shares the import/delete serialization boundary. A corrupt
// archive is returned unchanged; only a missing role starts a new archive.
func (w *WishArchives) MergeSnapshot(ctx context.Context, snapshot gacha.Snapshot) (launcher.WishImportReport, error) {
	release, err := w.acquire(ctx)
	if err != nil {
		return launcher.WishImportReport{}, err
	}
	defer release()
	archive, err := w.store.Load(ctx, snapshot.RoleID)
	if errors.Is(err, os.ErrNotExist) {
		archive = gacha.Archive{RoleID: snapshot.RoleID}
	} else if err != nil {
		return launcher.WishImportReport{}, err
	}
	merged, report, err := gacha.Merge(archive, snapshot)
	if err != nil {
		return launcher.WishImportReport{}, err
	}
	if err = w.store.Save(ctx, merged); err != nil {
		return launcher.WishImportReport{}, err
	}
	return launcher.WishImportReport{Archive: merged, Report: report}, nil
}
