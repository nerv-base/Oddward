// SPDX-License-Identifier: GPL-3.0-or-later

package database_test

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/database"
	"github.com/nerv-base/Oddward/internal/database/dbtest"
)

func TestOpenCreatesMigratedSchema(t *testing.T) {
	db := dbtest.Open(t)
	ctx := context.Background()

	if got, want := db.Version(), database.SchemaVersion(); got != want {
		t.Fatalf("schema version = %d, want %d", got, want)
	}
	for _, table := range []string{
		"schema_migrations", "app_meta", "migration_state", "compat_mirrors",
		"launch_settings", "gacha_archives", "gacha_records", "gacha_snapshots",
		"gacha_snapshot_pools", "gacha_item_names", "gacha_arcs", "gacha_arcs_meta",
		"accounts", "account_tajiduo", "account_state",
		"checkin_records", "checkin_auto",
		"download_tasks", "download_transfers", "download_rebuilds", "download_targets",
		"install_records", "install_targets", "asset_cache",
	} {
		var name string
		err := db.QueryRow(ctx, "SELECT name FROM sqlite_master WHERE type = 'table' AND name = ?", table).Scan(&name)
		if err != nil {
			t.Fatalf("table %s: %v", table, err)
		}
	}
}

func TestOpenAppliesPragmasToEveryConnection(t *testing.T) {
	db := dbtest.Open(t)
	ctx := context.Background()
	for name, pool := range map[string]*sql.DB{"write": db.Writer(), "read": db.Reader()} {
		var journal string
		if err := pool.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil {
			t.Fatalf("%s journal_mode: %v", name, err)
		}
		if journal != "wal" {
			t.Fatalf("%s journal_mode = %q, want wal", name, journal)
		}
		var foreignKeys int
		if err := pool.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
			t.Fatalf("%s foreign_keys: %v", name, err)
		}
		if foreignKeys != 1 {
			t.Fatalf("%s foreign_keys = %d, want 1", name, foreignKeys)
		}
		var busy int
		if err := pool.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy); err != nil {
			t.Fatalf("%s busy_timeout: %v", name, err)
		}
		if busy != 5000 {
			t.Fatalf("%s busy_timeout = %d, want 5000", name, busy)
		}
	}
}

func TestSchemaRecordsMigrationChecksums(t *testing.T) {
	db := dbtest.Open(t)
	ctx := context.Background()
	var count int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM schema_migrations").Scan(&count); err != nil {
		t.Fatalf("count migrations: %v", err)
	}
	if want := database.SchemaVersion(); count != want {
		t.Fatalf("recorded migrations = %d, want %d", count, want)
	}
	var checksum string
	if err := db.QueryRow(ctx, "SELECT checksum FROM schema_migrations WHERE version = 1").Scan(&checksum); err != nil {
		t.Fatalf("checksum: %v", err)
	}
	if len(checksum) != 64 {
		t.Fatalf("checksum = %q, want 64 hex characters", checksum)
	}
}

func TestSchemaRequiresItsIndexes(t *testing.T) {
	db := dbtest.Open(t)
	ctx := context.Background()
	for _, index := range []string{
		"gacha_records_identity", "gacha_records_recent", "gacha_records_item",
		"accounts_identity", "accounts_saved_at",
		"checkin_records_date",
		"download_tasks_state", "download_transfers_object",
		"asset_cache_last_used", "asset_cache_expiry",
	} {
		var name string
		err := db.QueryRow(ctx, "SELECT name FROM sqlite_master WHERE type = 'index' AND name = ?", index).Scan(&name)
		if err != nil {
			t.Fatalf("index %s: %v", index, err)
		}
	}
}

func TestOpenRefusesFutureSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, database.FileName)
	db := dbtest.OpenAt(t, dir)
	if _, err := db.Writer().ExecContext(context.Background(), "PRAGMA user_version = 99"); err != nil {
		t.Fatalf("set user_version: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	_, err := database.Open(context.Background(), database.Options{Path: path, NoBackup: true})
	if !errors.Is(err, database.ErrFutureSchema) {
		t.Fatalf("open future schema: err = %v, want ErrFutureSchema", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("future schema file must be left alone: %v", err)
	}
}

func TestOpenRefusesCorruptFileAndKeepsIt(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, database.FileName)
	junk := make([]byte, 8192)
	for i := range junk {
		junk[i] = 'x'
	}
	if err := os.WriteFile(path, junk, 0o600); err != nil {
		t.Fatalf("write junk: %v", err)
	}

	_, err := database.Open(context.Background(), database.Options{Path: path, NoBackup: true})
	if !errors.Is(err, database.ErrCorrupt) {
		t.Fatalf("open corrupt file: err = %v, want ErrCorrupt", err)
	}
	info, statErr := os.Stat(path)
	if statErr != nil {
		t.Fatalf("corrupt file must not be removed: %v", statErr)
	}
	if info.Size() != int64(len(junk)) {
		t.Fatalf("corrupt file was rewritten: size = %d, want %d", info.Size(), len(junk))
	}
}

func TestOpenRejectsUnsafePaths(t *testing.T) {
	ctx := context.Background()
	root := os.Getenv("SystemDrive") + string(os.PathSeparator)
	for name, path := range map[string]string{
		"relative": filepath.Join("oddward", database.FileName),
		"volume":   root,
	} {
		_, err := database.Open(ctx, database.Options{Path: path, NoBackup: true})
		if !errors.Is(err, database.ErrUnsafePath) {
			t.Fatalf("%s path %q: err = %v, want ErrUnsafePath", name, path, err)
		}
	}
}

func TestOpenReadOnlyDoesNotWrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, database.FileName)
	db := dbtest.OpenAt(t, dir)
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}

	ro, err := database.Open(context.Background(), database.Options{Path: path, ReadOnly: true})
	if err != nil {
		t.Fatalf("open read-only: %v", err)
	}
	defer ro.Close()
	if got, want := ro.Version(), database.SchemaVersion(); got != want {
		t.Fatalf("read-only version = %d, want %d", got, want)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after: %v", err)
	}
	if before.Size() != after.Size() {
		t.Fatalf("read-only open changed the file: %d -> %d bytes", before.Size(), after.Size())
	}
}

func TestTxCommitsAndRollsBack(t *testing.T) {
	db := dbtest.Open(t)
	ctx := context.Background()

	if err := db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, "INSERT INTO app_meta(key, value) VALUES('committed', '1')")
		return err
	}); err != nil {
		t.Fatalf("commit transaction: %v", err)
	}
	if got := metaValue(t, db, "committed"); got != "1" {
		t.Fatalf("committed value = %q, want 1", got)
	}

	boom := errors.New("boom")
	err := db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO app_meta(key, value) VALUES('rolled', '1')"); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("rollback transaction: err = %v, want boom", err)
	}
	var count int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM app_meta WHERE key = 'rolled'").Scan(&count); err != nil {
		t.Fatalf("count rolled: %v", err)
	}
	if count != 0 {
		t.Fatalf("rolled back row is visible: count = %d", count)
	}
}

func TestTxAbortsWhenContextIsCancelled(t *testing.T) {
	db := dbtest.Open(t)
	ctx := context.Background()

	cancelCtx, cancel := context.WithCancel(ctx)
	err := db.Tx(cancelCtx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, "INSERT INTO app_meta(key, value) VALUES('half', '1')"); err != nil {
			return err
		}
		cancel()
		_, err := tx.ExecContext(ctx, "INSERT INTO app_meta(key, value) VALUES('done', '1')")
		return err
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled transaction: err = %v, want context.Canceled", err)
	}
	var count int
	if err := db.QueryRow(ctx, "SELECT count(*) FROM app_meta WHERE key IN ('half', 'done')").Scan(&count); err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 0 {
		t.Fatalf("cancelled transaction left %d rows behind", count)
	}
}

func TestConcurrentReadersKeepWorkingWhileWriting(t *testing.T) {
	db := dbtest.Open(t)
	ctx := context.Background()
	if _, err := db.Writer().ExecContext(ctx,
		"INSERT INTO gacha_archives(role_id, source, imported_at, updated_at) VALUES('100000001', 'test', 1, 1)"); err != nil {
		t.Fatalf("seed archive: %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 8)
	stop := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				var count int
				if err := db.QueryRow(ctx, "SELECT count(*) FROM gacha_records").Scan(&count); err != nil {
					errs <- err
					return
				}
			}
		}()
	}

	for i := 0; i < 40; i++ {
		err := db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx,
				"INSERT INTO gacha_records(role_id, pool, item_id, at_ms, pity, luck, created_at) VALUES('100000001', 'limited', ?, ?, 0, -1, 1)",
				"item"+string(rune('a'+i%26)), int64(1700000000000+i))
			return err
		})
		if err != nil {
			t.Fatalf("write %d: %v", i, err)
		}
	}
	close(stop)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("reader failed while writing: %v", err)
	}
}

func TestConstraintsAreEnforced(t *testing.T) {
	db := dbtest.Open(t)
	ctx := context.Background()

	// A record cannot exist without its archive.
	if _, err := db.Writer().ExecContext(ctx,
		"INSERT INTO gacha_records(role_id, pool, item_id, at_ms, pity, luck, created_at) VALUES('999999999', 'limited', 'x', 1, 0, -1, 1)"); err == nil {
		t.Fatal("foreign key violation was accepted")
	}

	if _, err := db.Writer().ExecContext(ctx,
		"INSERT INTO gacha_archives(role_id, source, imported_at, updated_at) VALUES('100000001', 'test', 1, 1)"); err != nil {
		t.Fatalf("seed archive: %v", err)
	}
	insert := "INSERT INTO gacha_records(role_id, pool, item_id, at_ms, pity, luck, created_at) VALUES('100000001', 'limited', 'item', 1700000000000, 3, -1, 1)"
	if _, err := db.Writer().ExecContext(ctx, insert); err != nil {
		t.Fatalf("insert record: %v", err)
	}
	if _, err := db.Writer().ExecContext(ctx, insert); err == nil {
		t.Fatal("duplicate record identity was accepted")
	}
	if _, err := db.Writer().ExecContext(ctx,
		"INSERT INTO gacha_records(role_id, pool, item_id, at_ms, pity, luck, created_at) VALUES('100000001', 'banner', 'item', 1700000000001, 3, -1, 1)"); err == nil {
		t.Fatal("unknown pool was accepted")
	}
	// STRICT tables refuse a value of the wrong storage class.
	if _, err := db.Writer().ExecContext(ctx,
		"UPDATE gacha_records SET at_ms = 'not a number' WHERE item_id = 'item'"); err == nil {
		t.Fatal("text in an INTEGER column was accepted")
	}
}

func TestBackupVerifiesAndRestores(t *testing.T) {
	db := dbtest.Open(t)
	ctx := context.Background()
	if _, err := db.Writer().ExecContext(ctx, "INSERT INTO app_meta(key, value) VALUES('backup', '42')"); err != nil {
		t.Fatalf("seed: %v", err)
	}

	dest := filepath.Join(t.TempDir(), "oddward-backup.db")
	if err := db.Backup(ctx, dest); err != nil {
		t.Fatalf("backup: %v", err)
	}
	if err := database.Verify(ctx, dest, db.Version()); err != nil {
		t.Fatalf("verify: %v", err)
	}
	ro, err := database.Open(ctx, database.Options{Path: dest, ReadOnly: true})
	if err != nil {
		t.Fatalf("open backup: %v", err)
	}
	defer ro.Close()
	if got := metaValue(t, ro, "backup"); got != "42" {
		t.Fatalf("backup value = %q, want 42", got)
	}
	if err := db.Backup(ctx, dest); err == nil {
		t.Fatal("backup overwrote an existing file")
	}
	if err := database.Verify(ctx, dest, db.Version()+1); err == nil {
		t.Fatal("verify accepted the wrong schema version")
	}
}

func TestLockIsExclusiveAndReusable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade.lock")
	first, err := database.AcquireLock(context.Background(), path)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if _, err := database.AcquireLock(ctx, path); !errors.Is(err, database.ErrBusy) {
		t.Fatalf("second acquire: err = %v, want ErrBusy", err)
	}

	if err := first.Release(); err != nil {
		t.Fatalf("release: %v", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("second release must be a no-op: %v", err)
	}
	second, err := database.AcquireLock(context.Background(), path)
	if err != nil {
		t.Fatalf("reacquire: %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("release again: %v", err)
	}
}

func TestStampKeepsZeroTimeDistinct(t *testing.T) {
	if got := database.Stamp(time.Time{}); got != nil {
		t.Fatalf("zero time stamped as %v, want nil", got)
	}
	moment := time.Date(2026, 3, 4, 5, 6, 7, 8, time.UTC)
	if got := database.StampTime(sql.NullInt64{Int64: moment.UnixNano(), Valid: true}); !got.Equal(moment) {
		t.Fatalf("round trip = %v, want %v", got, moment)
	}
	if got := database.StampTime(sql.NullInt64{}); !got.IsZero() {
		t.Fatalf("null stamp = %v, want zero time", got)
	}
	if got := database.StampMillisTime(database.StampMillis(moment)); !got.Equal(moment.Truncate(time.Millisecond)) {
		t.Fatalf("millisecond round trip = %v", got)
	}
}

func TestCheckpointEmptiesWriteAheadLog(t *testing.T) {
	dir := t.TempDir()
	db := dbtest.OpenAt(t, dir)
	ctx := context.Background()
	for i := 0; i < 50; i++ {
		if _, err := db.Writer().ExecContext(ctx, "INSERT INTO app_meta(key, value) VALUES(?, '1')",
			"k"+strconv.Itoa(i)); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	if err := db.Checkpoint(ctx); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	info, err := os.Stat(db.Path() + "-wal")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return
		}
		t.Fatalf("stat wal: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("wal still holds %d bytes after checkpoint", info.Size())
	}
}

func TestCloseIsIdempotentAndRejectsWork(t *testing.T) {
	dir := t.TempDir()
	db := dbtest.OpenAt(t, dir)
	if err := db.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	ctx := context.Background()
	if _, err := db.Exec(ctx, "INSERT INTO app_meta(key, value) VALUES('x', '1')"); !errors.Is(err, database.ErrClosed) {
		t.Fatalf("exec after close: err = %v, want ErrClosed", err)
	}
	if _, err := db.Query(ctx, "SELECT 1"); !errors.Is(err, database.ErrClosed) {
		t.Fatalf("query after close: err = %v, want ErrClosed", err)
	}
	if err := db.Tx(ctx, func(context.Context, *sql.Tx) error { return nil }); !errors.Is(err, database.ErrClosed) {
		t.Fatalf("tx after close: err = %v, want ErrClosed", err)
	}
	var one int
	if err := db.QueryRow(ctx, "SELECT 1").Scan(&one); err == nil {
		t.Fatal("query row after close reported success")
	}
}

func TestWriterIsRefusedInsideTxCallback(t *testing.T) {
	dir := t.TempDir()
	db := dbtest.OpenAt(t, dir)
	ctx := context.Background()
	called := false
	err := db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		called = true
		if _, err := db.Exec(ctx, "INSERT INTO app_meta(key, value) VALUES('nested', '1')"); !errors.Is(err, database.ErrTxReentrant) {
			t.Fatalf("nested exec: err = %v, want ErrTxReentrant", err)
		}
		if err := db.Tx(ctx, func(context.Context, *sql.Tx) error { return nil }); !errors.Is(err, database.ErrTxReentrant) {
			t.Fatalf("nested tx: err = %v, want ErrTxReentrant", err)
		}
		_, err := tx.ExecContext(ctx, "INSERT INTO app_meta(key, value) VALUES('inner', '1')")
		return err
	})
	if err != nil {
		t.Fatalf("outer tx: %v", err)
	}
	if !called {
		t.Fatal("transaction callback never ran")
	}
	if got := metaValue(t, db, "inner"); got != "1" {
		t.Fatalf("inner row = %q", got)
	}
	var count int
	if err := db.QueryRow(context.Background(), "SELECT count(*) FROM app_meta WHERE key = 'nested'").Scan(&count); err != nil {
		t.Fatalf("count nested: %v", err)
	}
	if count != 0 {
		t.Fatalf("nested write landed %d rows", count)
	}
}

func metaValue(t *testing.T, db *database.DB, key string) string {
	t.Helper()
	var value string
	if err := db.QueryRow(context.Background(), "SELECT value FROM app_meta WHERE key = ?", key).Scan(&value); err != nil {
		t.Fatalf("read meta %s: %v", key, err)
	}
	return value
}
