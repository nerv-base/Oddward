// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"context"

	"github.com/nerv-base/Oddward/internal/gacha"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// WishArchiveBackend reads and writes only launcher-owned local draw archives.
// Import probes a new role without writing until confirmed is true. targetRoleID
// fences merging into the selected archive; a different existing role is rejected.
type WishArchiveBackend interface {
	List(context.Context) ([]string, error)
	Load(context.Context, string) (gacha.Archive, error)
	Delete(context.Context, string) error
	Import(ctx context.Context, path, targetRoleID string, confirmed bool) (WishImportReport, error)
	Export(ctx context.Context, path, roleID string, format state.WishFormat) error
	// Catalog returns the official item-name supplement. Names are optional:
	// callers may ignore the error and fall back to built-in names or raw ids.
	Catalog(context.Context) (gacha.Catalog, error)
}

type WishImportReport struct {
	Archive           gacha.Archive
	Report            gacha.MergeReport
	NeedsConfirmation bool
}

// wishImageSource is optional; a nil source uses the accessible neutral
// placeholder and never initiates an image request.
type wishImageSource interface {
	Image(context.Context, string) ([]byte, error)
}
