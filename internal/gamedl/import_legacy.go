// SPDX-License-Identifier: GPL-3.0-or-later

package gamedl

import (
	"context"
	"errors"
	"os"
	"path/filepath"

	"github.com/nerv-base/Oddward/internal/database"
)

// ImportLegacy reconciles the legacy task mirrors of every installation beneath
// installsRoot into the database. The launcher calls it once at startup after
// opening the database. Each installation keeps its own task directory, so a
// state root whose files were written by an older build has to be folded in even
// when another installation already completed the import; Store.ImportLegacy
// handles that, and the whole operation stays a no-op when no legacy files exist.
func ImportLegacy(ctx context.Context, db *database.DB, installsRoot string) error {
	if err := ctx.Err(); err != nil {
		return classify("import tasks", "", err)
	}
	if db == nil {
		return failure(ErrPathUnsafe, "store database", "", nil)
	}
	if err := safeRootPath(installsRoot); errors.Is(err, os.ErrNotExist) {
		// A fresh launcher state has no installations yet; there is nothing to
		// reconcile and no directory to create.
		return nil
	} else if err != nil {
		return err
	}
	entries, err := os.ReadDir(installsRoot)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		store, err := NewStore(db, filepath.Join(installsRoot, entry.Name()))
		if err != nil {
			errs = append(errs, err)
			continue
		}
		_, _, importErr := store.ImportLegacy(ctx)
		closeErr := store.Close()
		if importErr != nil {
			errs = append(errs, importErr)
			continue
		}
		if closeErr != nil {
			errs = append(errs, closeErr)
		}
	}
	return errors.Join(errs...)
}
