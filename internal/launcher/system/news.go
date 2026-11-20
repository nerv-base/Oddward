// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"github.com/nerv-base/Oddward/internal/database"
	"github.com/nerv-base/Oddward/internal/news"
)

// LauncherNews provides the official announcement board, its local fallback,
// and the client's independent live branding reads.
type LauncherNews struct{ *news.CachedClient }

// NewLauncherNews keeps the last usable news in the app-owned cache directory.
func NewLauncherNews(db *database.DB, client *news.Client, dir string) *LauncherNews {
	return &LauncherNews{CachedClient: news.NewCachedClient(db, client, dir)}
}
