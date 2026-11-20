// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"context"
	"log"
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
// A failure is reported and retried later rather than being fatal: the importer
// contract leaves both the files and the stored rows untouched when it cannot read
// the source, so refusing to start would strand a user behind unreadable data that
// the next build may well be able to explain. Only the database itself failing to
// open is fatal, and that happens before this runs.
func adoptLegacyRecords(
	db *database.DB,
	settings *launchsettings.Store,
	archives *system.WishArchives,
	accountStore *accounts.Store,
	records *checkin.Store,
	downloads *system.GameDownloadBackend,
	announcements *system.LauncherNews,
	brandingStore *brandingcache.Cache,
) {
	ctx, cancel := context.WithTimeout(context.Background(), adoptTimeout)
	defer cancel()
	report := func(what string, err error) {
		if err != nil {
			log.Printf("%s not adopted, keeping stored records: %v", what, err)
		}
	}
	_, err := db.RunImport(ctx, launchsettings.Domain, launchsettings.ImportVersion, settings.ImportLegacy)
	report("launcher settings", err)
	report("gacha archives", archives.ImportLegacy(ctx))
	report("account records", accountStore.ImportLegacy(ctx))
	report("check-in records", records.ImportLegacy(ctx))
	report("download, install and preload records", downloads.ImportLegacy(ctx))
	_, err = db.RunImport(ctx, news.Domain, news.ImportVersion, announcements.ImportLegacy)
	report("news cache", err)
	if brandingStore != nil {
		_, err = db.RunImport(ctx, brandingcache.Domain, brandingcache.ImportVersion, brandingStore.ImportLegacy)
		report("branding cache", err)
	}
}
