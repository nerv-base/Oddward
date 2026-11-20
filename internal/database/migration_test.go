// SPDX-License-Identifier: GPL-3.0-or-later

package database

import (
	"context"
	"path/filepath"
	"testing"
)

// openMigrationProbe opens a migrated database in a temporary directory.
func openMigrationProbe(t *testing.T) *DB {
	t.Helper()
	dir := t.TempDir()
	db, err := Open(context.Background(), Options{Path: filepath.Join(dir, FileName), NoBackup: true})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func (db *DB) probeUserVersion(t *testing.T) int {
	t.Helper()
	var version int
	if err := db.QueryRow(context.Background(), "PRAGMA user_version").Scan(&version); err != nil {
		t.Fatalf("read user_version: %v", err)
	}
	return version
}

func (db *DB) probeTableExists(t *testing.T, name string) bool {
	t.Helper()
	var count int
	if err := db.QueryRow(context.Background(), "SELECT count(*) FROM sqlite_master WHERE type = 'table' AND name = ?", name).Scan(&count); err != nil {
		t.Fatalf("inspect schema: %v", err)
	}
	return count > 0
}

// Two launcher processes can race an upgrade: both read the current version
// before either takes the write lock. The second one must notice, inside its own
// transaction, that the work is already done instead of re-running the schema
// body and failing on tables that now exist.
func TestApplyMigrationSkipsVersionAnotherProcessApplied(t *testing.T) {
	db := openMigrationProbe(t)
	ctx := context.Background()
	applied := migration{
		version:  2,
		name:     "0002_preload.sql",
		body:     "CREATE TABLE rerun_probe (x INTEGER) STRICT",
		checksum: "not-the-real-checksum",
	}
	if err := db.applyMigration(ctx, applied); err != nil {
		t.Fatalf("apply already-applied migration: %v", err)
	}
	if db.probeTableExists(t, "rerun_probe") {
		t.Fatal("migration body ran again and created its table")
	}
	var checksum string
	if err := db.QueryRow(ctx, "SELECT checksum FROM schema_migrations WHERE version = 2").Scan(&checksum); err != nil {
		t.Fatalf("read recorded checksum: %v", err)
	}
	if checksum == applied.checksum {
		t.Fatal("recorded checksum was replaced by the skipped migration")
	}

	// A migration that is genuinely new still runs, and records itself, its
	// version and its schema body in one transaction.
	fresh := migration{
		version:  SchemaVersion() + 1,
		name:     "9999_probe.sql",
		body:     "CREATE TABLE fresh_probe (x INTEGER) STRICT",
		checksum: "fresh",
	}
	if err := db.applyMigration(ctx, fresh); err != nil {
		t.Fatalf("apply fresh migration: %v", err)
	}
	if !db.probeTableExists(t, "fresh_probe") {
		t.Fatal("fresh migration did not create its table")
	}
	if got := db.probeUserVersion(t); got != fresh.version {
		t.Fatalf("user_version = %d, want %d", got, fresh.version)
	}
}

// A schema body that fails must leave the previous version, schema and bookkeeping
// untouched, so an interrupted upgrade is indistinguishable from "not started".
func TestApplyMigrationRollsBackOnFailure(t *testing.T) {
	db := openMigrationProbe(t)
	ctx := context.Background()
	before := db.probeUserVersion(t)
	broken := migration{
		version:  SchemaVersion() + 1,
		name:     "9998_broken.sql",
		body:     "CREATE TABLE broken_probe (x INTEGER) STRICT; CREATE TABLE broken_probe (x INTEGER) STRICT;",
		checksum: "broken",
	}
	if err := db.applyMigration(ctx, broken); err == nil {
		t.Fatal("broken migration reported success")
	}
	if got := db.probeUserVersion(t); got != before {
		t.Fatalf("user_version = %d after failed migration, want %d", got, before)
	}
	var recorded int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM schema_migrations WHERE version = ?", broken.version).Scan(&recorded); err != nil {
		t.Fatalf("read migrations: %v", err)
	}
	if recorded != 0 {
		t.Fatal("failed migration was recorded")
	}
	if db.probeTableExists(t, "broken_probe") {
		t.Fatal("failed migration left a table behind")
	}
}

// The embedded set must be contiguous and checksummed: a gap or duplicate would
// silently skip schema that the program depends on.
func TestEmbeddedMigrationsAreContiguous(t *testing.T) {
	migrations, err := loadOnce()
	if err != nil {
		t.Fatalf("load migrations: %v", err)
	}
	if len(migrations) == 0 {
		t.Fatal("no migrations embedded")
	}
	for i, m := range migrations {
		if m.version != i+1 {
			t.Fatalf("migration %d has version %d", i, m.version)
		}
		if len(m.checksum) != 64 {
			t.Fatalf("migration %d checksum %q", m.version, m.checksum)
		}
		if m.body == "" {
			t.Fatalf("migration %d has an empty body", m.version)
		}
	}
	if got := SchemaVersion(); got != len(migrations) {
		t.Fatalf("SchemaVersion = %d, want %d", got, len(migrations))
	}
}
