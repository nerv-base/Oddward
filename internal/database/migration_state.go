// SPDX-License-Identifier: GPL-3.0-or-later

package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Migration states recorded in migration_state.
const (
	MigrationRunning = "running"
	MigrationDone    = "done"
	MigrationFailed  = "failed"
)

// migrationDetailLimit keeps a failure report readable inside the database.
const migrationDetailLimit = 512

// Migration is the recorded import state of one domain.
type Migration struct {
	Domain     string
	Status     string
	Source     string
	Detail     string
	Version    int
	StartedAt  time.Time
	FinishedAt time.Time
}

// MigrationState returns the recorded state for a domain.
func (db *DB) MigrationState(ctx context.Context, domain string) (Migration, bool, error) {
	var (
		state      Migration
		startedAt  sql.NullInt64
		finishedAt sql.NullInt64
	)
	err := db.QueryRow(ctx, `
		SELECT domain, status, source, detail, version, started_at, finished_at
		FROM migration_state WHERE domain = ?`, domain).
		Scan(&state.Domain, &state.Status, &state.Source, &state.Detail, &state.Version, &startedAt, &finishedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Migration{}, false, nil
	}
	if err != nil {
		return Migration{}, false, err
	}
	state.StartedAt = StampTime(startedAt)
	state.FinishedAt = StampTime(finishedAt)
	return state, true, nil
}

// SetMigrationState records the outcome of one domain import. It runs outside the
// import transaction on purpose: the rows are the authoritative result, so a crash
// between the two only costs one repeated, idempotent import.
func (db *DB) SetMigrationState(ctx context.Context, domain, status, source, detail string, version int) error {
	if len(detail) > migrationDetailLimit {
		detail = detail[:migrationDetailLimit]
	}
	var started, finished any
	now := db.now().UnixNano()
	switch status {
	case MigrationRunning:
		started = now
	case MigrationDone, MigrationFailed:
		finished = now
	default:
		return fmt.Errorf("database: unknown migration status %q", status)
	}
	_, err := db.Exec(ctx, `
		INSERT INTO migration_state(domain, status, source, detail, version, started_at, finished_at)
		VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(domain) DO UPDATE SET
			status = excluded.status,
			source = excluded.source,
			detail = excluded.detail,
			version = excluded.version,
			started_at = COALESCE(excluded.started_at, migration_state.started_at),
			finished_at = excluded.finished_at`,
		domain, status, source, detail, version, started, finished)
	if err != nil {
		return fmt.Errorf("database: record %s migration: %w", domain, err)
	}
	return nil
}

// ImportFunc replaces one domain's rows with the contents of its legacy files. It
// must be idempotent and must leave the rows untouched when the legacy source does
// not exist. It reports where the data came from and a one-line summary for the
// migration record.
type ImportFunc func(ctx context.Context) (source, detail string, err error)

// RunImport runs a domain's legacy import when it has not completed yet, and again
// whenever the domain's mirror file was changed by another build (the stub rolled
// back to a version that does not know the database, and that version may have
// accepted new data).
//
// It reports whether fn ran. A failed import is recorded and returned; the caller
// decides whether the domain can continue with the rows it already has.
func (db *DB) RunImport(ctx context.Context, domain string, version int, fn ImportFunc) (bool, error) {
	state, found, err := db.MigrationState(ctx, domain)
	if err != nil {
		return false, err
	}
	stale, err := db.MirrorStale(ctx, domain)
	if err != nil {
		return false, err
	}
	if found && state.Status == MigrationDone && !stale {
		return false, nil
	}
	if err := db.SetMigrationState(ctx, domain, MigrationRunning, "", "", version); err != nil {
		return false, err
	}
	source, detail, err := fn(ctx)
	if err != nil {
		_ = db.SetMigrationState(ctx, domain, MigrationFailed, source, err.Error(), version)
		return true, err
	}
	if err := db.SetMigrationState(ctx, domain, MigrationDone, source, detail, version); err != nil {
		return true, err
	}
	return true, nil
}
