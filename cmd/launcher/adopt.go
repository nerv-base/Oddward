// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/nerv-base/Oddward/internal/brandingcache"
	"github.com/nerv-base/Oddward/internal/checkin"
	"github.com/nerv-base/Oddward/internal/cnlogin/accounts"
	"github.com/nerv-base/Oddward/internal/database"
	"github.com/nerv-base/Oddward/internal/launcher/system"
	"github.com/nerv-base/Oddward/internal/launchsettings"
	"github.com/nerv-base/Oddward/internal/news"
)

// adoptTimeout bounds the whole adoption pass. Every importer is a one-shot read of
// files this or an older build wrote, so a slow or hung disk must not delay the
// window forever: whatever is not adopted now is retried on the next start.
const adoptTimeout = 2 * time.Minute

// adoptLegacyRecords makes the database the authority for records an older build
// left in files. Each domain importer is idempotent and reconciles the mirror a
// downgraded build may have rewritten, so running this on every start is safe and
// is what keeps a rollback from losing the previous version's writes.
//
// A cache domain that cannot be adopted is only reported: the rows stay as they
// are, the importer contract leaves both source and rows untouched, and the cache
// rebuilds itself. A user-data domain is different. Letting the launcher run on
// rows that do not describe the files would overwrite what the previous build
// wrote, so the failure is returned and the launcher exits without writing: the
// startup stub then falls back to the build that owns those files.
func adoptLegacyRecords(
	db *database.DB,
	settings *launchsettings.Store,
	archives *system.WishArchives,
	accountStore *accounts.Store,
	records *checkin.Store,
	downloads *system.GameDownloadBackend,
	announcements *system.LauncherNews,
	brandingStore *brandingcache.Cache,
) error {
	ctx, cancel := context.WithTimeout(context.Background(), adoptTimeout)
	defer cancel()
	var failed []string
	keep := func(what string, err error) {
		if err != nil {
			failed = append(failed, fmt.Sprintf("%s: %v", what, err))
		}
	}
	report := func(what string, err error) {
		if err != nil {
			log.Printf("%s not adopted, keeping stored records: %v", what, err)
		}
	}
	_, err := db.RunImport(ctx, launchsettings.Domain, launchsettings.ImportVersion, settings.ImportLegacy)
	keep("launcher settings", err)
	keep("gacha archives", archives.ImportLegacy(ctx))
	keep("account records", accountStore.ImportLegacy(ctx))
	keep("check-in records", records.ImportLegacy(ctx))
	keep("download, install and preload records", downloads.ImportLegacy(ctx))
	_, err = db.RunImport(ctx, news.Domain, news.ImportVersion, announcements.ImportLegacy)
	report("news cache", err)
	if brandingStore != nil {
		_, err = db.RunImport(ctx, brandingcache.Domain, brandingcache.ImportVersion, brandingStore.ImportLegacy)
		report("branding cache", err)
	}
	if len(failed) > 0 {
		return fmt.Errorf("legacy records could not be adopted: %s", strings.Join(failed, "; "))
	}
	return nil
}
