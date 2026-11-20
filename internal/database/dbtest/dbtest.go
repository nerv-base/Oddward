// SPDX-License-Identifier: GPL-3.0-or-later

// Package dbtest opens throwaway databases for tests. It keeps every test's
// database inside its own temporary directory so parallel packages never share a
// file, and it refuses the automatic pre-migration backup so tests cannot leave
// backups behind.
package dbtest

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/nerv-base/Oddward/internal/database"
)

// Open returns a migrated database in a fresh temporary directory.
func Open(tb testing.TB) *database.DB {
	tb.Helper()
	return OpenAt(tb, tb.TempDir())
}

// OpenAt returns a migrated database at <dir>/oddward.db.
func OpenAt(tb testing.TB, dir string) *database.DB {
	tb.Helper()
	db, err := database.Open(context.Background(), database.Options{
		Path:     filepath.Join(dir, database.FileName),
		NoBackup: true,
	})
	if err != nil {
		tb.Fatalf("open test database: %v", err)
	}
	tb.Cleanup(func() {
		if err := db.Close(); err != nil {
			tb.Errorf("close test database: %v", err)
		}
	})
	return db
}
