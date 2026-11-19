// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/nerv-base/Oddward/internal/bwiki"
	"github.com/nerv-base/Oddward/internal/gacha"
	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// arcWiki is the small read surface WishArchives needs from the community
// supplement; *bwiki.Store implements it.
type arcWiki interface {
	Catalog(context.Context) (bwiki.Catalog, error)
}

// WishArchives implements launcher.WishArchiveBackend over a local, launcher-
// owned gacha store. Mutating operations (Import, Delete, Export) are serialized
// by a cancelable capacity-one lock so a load-merge-save cannot interleave with
// a delete. List and Load delegate to the store.
type WishArchives struct {
	store *gacha.Store
	wiki  arcWiki
	now   func() time.Time
	ops   chan struct{}
}

var _ launcher.WishArchiveBackend = (*WishArchives)(nil)

// NewWishArchives opens the archive directory under dataDir, which must be an
// absolute path, and prepares the serializing lock.
func NewWishArchives(dataDir string) (*WishArchives, error) {
	if !filepath.IsAbs(dataDir) {
		return nil, errors.New("wish archive data directory must be an absolute path")
	}
	store, err := gacha.NewStore(filepath.Join(dataDir, "gacha"))
	if err != nil {
		return nil, err
	}
	w := &WishArchives{store: store, now: time.Now, ops: make(chan struct{}, 1)}
	w.ops <- struct{}{}
	return w, nil
}

// acquire takes the serializing lock, honoring ctx cancellation while waiting.
func (w *WishArchives) acquire(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-w.ops:
		if err := ctx.Err(); err != nil {
			w.ops <- struct{}{}
			return nil, err
		}
		return func() { w.ops <- struct{}{} }, nil
	}
}

func (w *WishArchives) List(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return w.store.List(ctx)
}

func (w *WishArchives) Load(ctx context.Context, roleID string) (gacha.Archive, error) {
	if err := ctx.Err(); err != nil {
		return gacha.Archive{}, err
	}
	archive, err := w.store.Load(ctx, roleID)
	if errors.Is(err, gacha.ErrCorrupt) {
		// Keep the broken bytes as a .corrupt-<time> sibling instead of
		// overwriting or silently deleting evidence. A rename failure is
		// reported by the original load error; the file stays untouched.
		_, _ = w.store.Quarantine(ctx, roleID)
	}
	return archive, err
}

// SetBWiki adds the optional community-wiki supplement. It only fills missing
// arc names and rarity; official names always win.
func (w *WishArchives) SetBWiki(store *bwiki.Store) { w.wiki = store }

// Catalog reads the official name supplement and fills arcs the official
// source lacks from the optional community cache. A missing file is an empty
// catalog: names never block the page. The community supplement is best-effort
// and capped so a slow wiki cannot hold the page open.
func (w *WishArchives) Catalog(ctx context.Context) (gacha.Catalog, error) {
	if err := ctx.Err(); err != nil {
		return gacha.Catalog{}, err
	}
	c, err := w.store.LoadCatalog(ctx)
	if err != nil || w.wiki == nil {
		return c, err
	}
	fetchCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	wiki, wikiErr := w.wiki.Catalog(fetchCtx)
	if wikiErr != nil {
		return c, nil
	}
	if c.Items == nil {
		c.Items = make(map[string]gacha.Item)
	}
	for key, entry := range wiki.Items {
		id := "fork_" + key
		if item, ok := c.Items[id]; ok {
			if item.Rarity == "" {
				item.Rarity = entry.Rarity
				c.Items[id] = item
			}
			continue
		}
		c.Items[id] = gacha.Item{Kind: gacha.Arc, Name: entry.Name, Rarity: entry.Rarity}
	}
	return c, nil
}

// SaveCatalog replaces the name supplement. It uses its own file and store
// lock, so it neither waits for nor blocks archive mutations.
func (w *WishArchives) SaveCatalog(ctx context.Context, c gacha.Catalog) error {
	return w.store.SaveCatalog(ctx, c)
}

// Delete removes one archive through the serializing lock. A missing archive is
// reported as success.
func (w *WishArchives) Delete(ctx context.Context, roleID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	release, err := w.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	if err := w.store.Delete(ctx, roleID); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// Import probes or merges an archive file. A new role is only written once
// confirmed is true; a confirmed import requires targetRoleID to match the
// file's role so the bytes cannot be swapped between probe and confirmation.
func (w *WishArchives) Import(ctx context.Context, path, targetRoleID string, confirmed bool) (launcher.WishImportReport, error) {
	if err := ctx.Err(); err != nil {
		return launcher.WishImportReport{}, err
	}
	release, err := w.acquire(ctx)
	if err != nil {
		return launcher.WishImportReport{}, err
	}
	defer release()

	data, err := readImportFile(ctx, path)
	if err != nil {
		return launcher.WishImportReport{}, err
	}

	imported, err := gacha.DecodeImport(data)
	if err != nil {
		if errors.Is(err, gacha.ErrUnknownFormat) {
			return launcher.WishImportReport{}, err
		}
		return launcher.WishImportReport{}, fmt.Errorf("%w: %v", gacha.ErrUnknownFormat, err)
	}
	role := imported.Archive.RoleID

	existing, err := w.store.Load(ctx, role)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		// Corrupt or unreadable archives are reported as-is and never overwritten.
		return launcher.WishImportReport{}, err
	}
	if err == nil {
		if targetRoleID != "" && targetRoleID != role {
			return launcher.WishImportReport{}, gacha.ErrRoleMismatch
		}
		merged, report, mergeErr := gacha.MergeImported(existing, imported)
		if mergeErr != nil {
			return launcher.WishImportReport{}, mergeErr
		}
		if err := w.store.Save(ctx, merged); err != nil {
			return launcher.WishImportReport{}, err
		}
		return launcher.WishImportReport{Archive: merged, Report: report}, nil
	}

	if !confirmed {
		_, report, mergeErr := gacha.MergeImported(gacha.Archive{RoleID: role}, imported)
		if mergeErr != nil {
			return launcher.WishImportReport{}, mergeErr
		}
		return launcher.WishImportReport{
			Archive:           imported.Archive,
			Report:            report,
			NeedsConfirmation: true,
		}, nil
	}

	if targetRoleID != role {
		return launcher.WishImportReport{}, gacha.ErrRoleMismatch
	}
	merged, report, mergeErr := gacha.MergeImported(gacha.Archive{RoleID: role}, imported)
	if mergeErr != nil {
		return launcher.WishImportReport{}, mergeErr
	}
	if err := w.store.Save(ctx, merged); err != nil {
		return launcher.WishImportReport{}, err
	}
	return launcher.WishImportReport{Archive: merged, Report: report}, nil
}

// Export loads one archive and writes it in the requested interchange format
// via a synced temporary file and an atomic same-directory replace. It never
// adds an extension.
func (w *WishArchives) Export(ctx context.Context, path, roleID string, format state.WishFormat) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !filepath.IsAbs(path) {
		return errors.New("wish export path must be an absolute path")
	}
	release, err := w.acquire(ctx)
	if err != nil {
		return err
	}
	defer release()

	archive, err := w.store.Load(ctx, roleID)
	if err != nil {
		return err
	}
	var data []byte
	switch format {
	case state.WishOddward:
		data, err = gacha.ExportOddward(archive, w.now())
	case state.WishNTEUID:
		data, err = gacha.ExportNTEUID(archive, w.now())
	default:
		return fmt.Errorf("unknown wish export format %d", format)
	}
	if err != nil {
		return err
	}
	return writeExportFile(ctx, path, data)
}

// readImportFile reads a bounded import document. It requires an ordinary file
// no larger than gacha.MaxDocumentBytes and never reads past that bound.
func readImportFile(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("wish import must be an ordinary file")
	}
	if info.Size() > gacha.MaxDocumentBytes {
		return nil, fmt.Errorf("wish import exceeds %d bytes", gacha.MaxDocumentBytes)
	}
	data, err := io.ReadAll(io.LimitReader(file, gacha.MaxDocumentBytes+1))
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(data) > gacha.MaxDocumentBytes {
		return nil, fmt.Errorf("wish import exceeds %d bytes", gacha.MaxDocumentBytes)
	}
	return data, nil
}

// writeExportFile stages data in the target directory and atomically replaces
// the destination. Any failure removes the stage.
func writeExportFile(ctx context.Context, path string, data []byte) (result error) {
	stage, err := os.CreateTemp(filepath.Dir(path), ".oddward-export-*.tmp")
	if err != nil {
		return err
	}
	stagedName := stage.Name()
	committed := false
	defer func() {
		_ = stage.Close()
		if !committed {
			if err := os.Remove(stagedName); err != nil && !errors.Is(err, os.ErrNotExist) {
				result = errors.Join(result, fmt.Errorf("remove owned wish export stage: %w", err))
			}
		}
	}()
	if _, err := stage.Write(data); err != nil {
		return err
	}
	if err := stage.Sync(); err != nil {
		return err
	}
	if err := stage.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := replaceExport(stagedName, path); err != nil {
		return err
	}
	committed = true
	return nil
}
