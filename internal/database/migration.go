// SPDX-License-Identifier: GPL-3.0-or-later

package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

// migration is one versioned, checksummed section of the schema. Migrations are
// applied in version order inside a transaction that also bumps user_version, so
// an interrupted upgrade either applies completely or not at all.
type migration struct {
	version  int
	name     string
	body     string
	checksum string
}

var loadOnce = sync.OnceValues(loadMigrations)

// SchemaVersion reports the schema version this build writes. A database stored
// by a higher version is rejected instead of being opened.
func SchemaVersion() int {
	migrations, err := loadOnce()
	if err != nil || len(migrations) == 0 {
		return 0
	}
	return migrations[len(migrations)-1].version
}

// loadMigrations reads and validates the embedded migration set.
func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || path.Ext(entry.Name()) != ".sql" {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	migrations := make([]migration, 0, len(names))
	for i, name := range names {
		digits, _, ok := strings.Cut(name, "_")
		if !ok {
			return nil, fmt.Errorf("database: migration %q has no version prefix", name)
		}
		version, err := strconv.Atoi(digits)
		if err != nil {
			return nil, fmt.Errorf("database: migration %q has a bad version: %w", name, err)
		}
		if version != i+1 {
			return nil, fmt.Errorf("database: migration %q is version %d, want %d", name, version, i+1)
		}
		body, err := fs.ReadFile(migrationFiles, path.Join("migrations", name))
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(body)
		migrations = append(migrations, migration{
			version:  version,
			name:     name,
			body:     string(body),
			checksum: hex.EncodeToString(sum[:]),
		})
	}
	if len(migrations) == 0 {
		return nil, errors.New("database: no migrations embedded")
	}
	return migrations, nil
}

// migrate brings the file from current to the version this build writes.
func (db *DB) migrate(ctx context.Context, current int) error {
	migrations, err := loadOnce()
	if err != nil {
		return err
	}
	latest := migrations[len(migrations)-1].version
	if current > latest {
		return fmt.Errorf("%w: file is version %d, this build writes %d; keep the launcher that upgraded it", ErrFutureSchema, current, latest)
	}
	var pending []migration
	applied := make(map[int]string, current)
	if current > 0 {
		rows, err := db.write.QueryContext(ctx, "SELECT version, checksum FROM schema_migrations ORDER BY version")
		if err != nil {
			return classify(err, "read applied migrations")
		}
		for rows.Next() {
			var version int
			var checksum string
			if err := rows.Scan(&version, &checksum); err != nil {
				rows.Close()
				return classify(err, "read applied migrations")
			}
			applied[version] = checksum
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return classify(err, "read applied migrations")
		}
		rows.Close()
	}
	for _, m := range migrations {
		if m.version <= current {
			if got, ok := applied[m.version]; !ok || got != m.checksum {
				return fmt.Errorf("%w: migration %d does not match the recorded checksum", ErrCorrupt, m.version)
			}
			continue
		}
		pending = append(pending, m)
	}
	if len(pending) == 0 {
		return nil
	}
	if current > 0 {
		if err := db.backupBeforeMigration(ctx, current); err != nil {
			return err
		}
	}
	for _, m := range pending {
		if err := db.applyMigration(ctx, m); err != nil {
			return err
		}
	}
	return nil
}

// applyMigration executes one migration and records it atomically.
func (db *DB) applyMigration(ctx context.Context, m migration) error {
	return db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		// A second launcher process may have applied this migration while this
		// one waited for the write lock. The version is re-read inside the
		// transaction that already holds it, so the schema body can never run
		// twice and fail on existing tables.
		var version int
		if err := tx.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
			return fmt.Errorf("database: read schema version before %s: %w", m.name, err)
		}
		if version >= m.version {
			return nil
		}
		if _, err := tx.ExecContext(ctx, m.body); err != nil {
			return fmt.Errorf("database: migration %s failed: %w", m.name, err)
		}
		if _, err := tx.ExecContext(ctx,
			"INSERT INTO schema_migrations(version, name, checksum, applied_at) VALUES(?, ?, ?, ?)",
			m.version, m.name, m.checksum, db.now().UnixNano()); err != nil {
			return fmt.Errorf("database: record migration %s: %w", m.name, err)
		}
		// user_version is stored in the database header and is updated inside the
		// same transaction, so a rollback leaves the old version in place.
		if _, err := tx.ExecContext(ctx, "PRAGMA user_version = "+strconv.Itoa(m.version)); err != nil {
			return fmt.Errorf("database: set schema version %d: %w", m.version, err)
		}
		return nil
	})
}

// backupBeforeMigration keeps a verified copy of the current file before the
// schema changes. A failed upgrade can then be inspected or restored instead of
// being guessed at.
func (db *DB) backupBeforeMigration(ctx context.Context, from int) error {
	if db.opts.NoBackup {
		return nil
	}
	dir := filepath.Join(db.Dir(), "backups")
	if err := ensureDataDir(dir, true); err != nil {
		return err
	}
	dest := filepath.Join(dir, fmt.Sprintf("pre-migration-v%d-%s.db", from, db.now().UTC().Format("20060102T150405")))
	if err := db.Backup(ctx, dest); err != nil {
		return fmt.Errorf("database: back up before upgrading schema from %d: %w", from, err)
	}
	return pruneBackups(dir, 3)
}

// pruneBackups removes the oldest pre-migration copies beyond keep.
func pruneBackups(dir string, keep int) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	type backup struct {
		name string
		mod  time.Time
	}
	found := make([]backup, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".db") {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		found = append(found, backup{name: entry.Name(), mod: info.ModTime()})
	}
	if len(found) <= keep {
		return nil
	}
	sort.Slice(found, func(i, j int) bool { return found[i].mod.After(found[j].mod) })
	for _, old := range found[keep:] {
		if err := os.Remove(filepath.Join(dir, old.name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (db *DB) now() time.Time {
	if db.opts.Now != nil {
		return db.opts.Now().UTC()
	}
	return time.Now().UTC()
}
