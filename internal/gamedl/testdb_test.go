// SPDX-License-Identifier: GPL-3.0-or-later
package gamedl_test

import (
	"testing"

	"github.com/nerv-base/Oddward/internal/database"
	"github.com/nerv-base/Oddward/internal/database/dbtest"
)

// testDB gives each test an isolated migrated database. Tests that reopen an
// engine on the same state root must reuse the returned handle: the database,
// not the state root, holds the task rows.
func testDB(t *testing.T) *database.DB {
	t.Helper()
	return dbtest.Open(t)
}

// testDBAt opens the database a killed helper process was already using.
func testDBAt(t *testing.T, dir string) *database.DB {
	t.Helper()
	return dbtest.OpenAt(t, dir)
}
