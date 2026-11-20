// SPDX-License-Identifier: GPL-3.0-or-later

package database_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/nerv-base/Oddward/internal/database"
	"github.com/nerv-base/Oddward/internal/database/dbtest"
)

func TestMirrorDetectsChangesFromAnotherBuild(t *testing.T) {
	db := dbtest.Open(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.json")
	data := []byte(`{"version":6}`)

	if err := db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO app_meta(key, value) VALUES('mirror', '1')"); err != nil {
			return err
		}
		return db.RecordMirror(ctx, tx, "settings", path, database.Digest(data))
	}); err != nil {
		t.Fatalf("record mirror: %v", err)
	}
	if err := database.WriteMirror(path, data); err != nil {
		t.Fatalf("write mirror: %v", err)
	}
	if stale, err := db.MirrorStale(ctx, "settings"); err != nil || stale {
		t.Fatalf("fresh mirror: stale = %v, err = %v", stale, err)
	}

	// Another build (here: the test) accepts a new value.
	if err := os.WriteFile(path, []byte(`{"version":6,"gameRoot":"D:\\Games"}`), 0o600); err != nil {
		t.Fatalf("rewrite mirror: %v", err)
	}
	if stale, err := db.MirrorStale(ctx, "settings"); err != nil || !stale {
		t.Fatalf("changed mirror: stale = %v, err = %v", stale, err)
	}

	// A mirror that disappeared is stale too: it cannot be trusted either way.
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove mirror: %v", err)
	}
	if stale, err := db.MirrorStale(ctx, "settings"); err != nil || !stale {
		t.Fatalf("missing mirror: stale = %v, err = %v", stale, err)
	}
}

func TestMirrorThenCommitWritesFileAndRows(t *testing.T) {
	db := dbtest.Open(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.json")
	data := []byte(`{"version":6,"gameRoot":"D:\\Games"}`)

	if err := db.MirrorThenCommit(ctx, "settings", path, data, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO app_meta(key, value) VALUES('gameRoot', 'D:\\Games')")
		return err
	}); err != nil {
		t.Fatalf("mirror then commit: %v", err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read mirror: %v", err)
	}
	if string(written) != string(data) {
		t.Fatalf("mirror = %q, want %q", written, data)
	}
	if stale, err := db.MirrorStale(ctx, "settings"); err != nil || stale {
		t.Fatalf("fresh mirror: stale = %v, err = %v", stale, err)
	}
}

func TestMirrorThenCommitKeepsRowsWhenMirrorFails(t *testing.T) {
	db := dbtest.Open(t)
	ctx := context.Background()
	dir := t.TempDir()
	// A regular file where the mirror's directory should be makes the write fail
	// with the same kind of error a full or read-only disk produces.
	blocked := filepath.Join(dir, "blocked")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("block: %v", err)
	}
	path := filepath.Join(blocked, "legacy.json")

	err := db.MirrorThenCommit(ctx, "settings", path, []byte(`{"version":6}`), func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO app_meta(key, value) VALUES('should-not-land', '1')")
		return err
	})
	if err == nil {
		t.Fatal("mirror write to an impossible path reported success")
	}
	var count int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM app_meta WHERE key = 'should-not-land'").Scan(&count); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("rows committed although the mirror failed: %d", count)
	}
	if _, found, err := db.Mirror(ctx, "settings"); err != nil || found {
		t.Fatalf("mirror record: found = %v, err = %v", found, err)
	}
}

func TestMirrorThenCommitLeavesFileAheadWhenTransactionFails(t *testing.T) {
	db := dbtest.Open(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.json")
	first := []byte(`{"n":1}`)
	if err := db.MirrorThenCommit(ctx, "settings", path, first, func(context.Context, *sql.Tx) error { return nil }); err != nil {
		t.Fatalf("first commit: %v", err)
	}

	// The second save writes the mirror, then its transaction is rejected. The
	// file is ahead of the rows, which is exactly what a reconcile must see.
	boom := errors.New("constraint")
	second := []byte(`{"n":2}`)
	err := db.MirrorThenCommit(ctx, "settings", path, second, func(context.Context, *sql.Tx) error { return boom })
	if !errors.Is(err, boom) {
		t.Fatalf("second commit: err = %v, want boom", err)
	}
	if stale, err := db.MirrorStale(ctx, "settings"); err != nil || !stale {
		t.Fatalf("file ahead of rows: stale = %v, err = %v", stale, err)
	}
}

func TestMirrorInTxWritesFileBeforeTheCommit(t *testing.T) {
	db := dbtest.Open(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.json")
	data := []byte(`{"records":[]}`)

	if err := db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO app_meta(key, value) VALUES('in-tx', '1')"); err != nil {
			return err
		}
		return db.MirrorInTx(ctx, tx, "settings", path, data)
	}); err != nil {
		t.Fatalf("mirror in tx: %v", err)
	}
	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read mirror: %v", err)
	}
	if string(written) != string(data) {
		t.Fatalf("mirror = %q, want %q", written, data)
	}
	if stale, err := db.MirrorStale(ctx, "settings"); err != nil || stale {
		t.Fatalf("fresh mirror: stale = %v, err = %v", stale, err)
	}

	// A transaction that dies after writing the file leaves the file ahead of
	// the rows: that difference is the reconcile trigger, not lost data.
	boom := errors.New("constraint")
	err = db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := db.MirrorInTx(ctx, tx, "settings", path, []byte(`{"records":[1]}`)); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("failing tx: err = %v", err)
	}
	if stale, err := db.MirrorStale(ctx, "settings"); err != nil || !stale {
		t.Fatalf("file ahead of rows: stale = %v, err = %v", stale, err)
	}
}

func TestRunImportSkipsDoneWorkAndRetriesAfterRollback(t *testing.T) {
	db := dbtest.Open(t)
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.json")
	runs := 0

	importOnce := func(ctx context.Context) (string, string, error) {
		runs++
		data := []byte(`{"n":1}`)
		err := db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			if _, err := tx.ExecContext(ctx, "INSERT OR REPLACE INTO app_meta(key, value) VALUES('imported', ?)", string(data)); err != nil {
				return err
			}
			return db.RecordMirror(ctx, tx, "settings", path, database.Digest(data))
		})
		if err != nil {
			return "", "", err
		}
		if err := database.WriteMirror(path, data); err != nil {
			return "", "", err
		}
		return "launcher-v1.json", "1 row", nil
	}

	ran, err := db.RunImport(ctx, "settings", 1, importOnce)
	if err != nil || !ran {
		t.Fatalf("first import: ran = %v, err = %v", ran, err)
	}
	ran, err = db.RunImport(ctx, "settings", 1, importOnce)
	if err != nil || ran {
		t.Fatalf("second import must be skipped: ran = %v, err = %v", ran, err)
	}
	if runs != 1 {
		t.Fatalf("import function ran %d times, want 1", runs)
	}

	state, found, err := db.MigrationState(ctx, "settings")
	if err != nil || !found {
		t.Fatalf("state: found = %v, err = %v", found, err)
	}
	if state.Status != database.MigrationDone || state.Source != "launcher-v1.json" {
		t.Fatalf("state = %+v, want done from launcher-v1.json", state)
	}

	// The stub rolled back to an older build, which then accepted new data.
	if err := os.WriteFile(path, []byte(`{"n":2}`), 0o600); err != nil {
		t.Fatalf("rewrite legacy file: %v", err)
	}
	ran, err = db.RunImport(ctx, "settings", 1, importOnce)
	if err != nil || !ran {
		t.Fatalf("reconcile after rollback: ran = %v, err = %v", ran, err)
	}
	if runs != 2 {
		t.Fatalf("import function ran %d times, want 2", runs)
	}
}

func TestRunImportRecordsFailureAndRetries(t *testing.T) {
	db := dbtest.Open(t)
	ctx := context.Background()
	boom := errors.New("legacy file is corrupt")

	if _, err := db.RunImport(ctx, "gacha", 1, func(context.Context) (string, string, error) {
		return "gacha/", "", boom
	}); !errors.Is(err, boom) {
		t.Fatalf("failed import: err = %v, want boom", err)
	}
	state, found, err := db.MigrationState(ctx, "gacha")
	if err != nil || !found {
		t.Fatalf("state: found = %v, err = %v", found, err)
	}
	if state.Status != database.MigrationFailed {
		t.Fatalf("status = %q, want failed", state.Status)
	}
	if state.Detail != boom.Error() {
		t.Fatalf("detail = %q, want %q", state.Detail, boom.Error())
	}

	// A failed import must be retried on the next start.
	runs := 0
	ran, err := db.RunImport(ctx, "gacha", 1, func(context.Context) (string, string, error) {
		runs++
		return "gacha/", "ok", nil
	})
	if err != nil || !ran || runs != 1 {
		t.Fatalf("retry: ran = %v, runs = %d, err = %v", ran, runs, err)
	}
}
