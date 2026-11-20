// SPDX-License-Identifier: GPL-3.0-or-later

// Package database owns Oddward's single SQLite database. It provides the
// connection pools, every pragma the launcher depends on, schema migrations,
// integrity checks, backups and the cross-process upgrade lock. It must not
// depend on the UI, on internal/launcher or on any domain package: domains
// import this package, never the other way around.
//
// # Pragma scope
//
// WAL journalling is a property of the database file itself: once a database
// has been switched to WAL it stays in WAL across processes and is not reset by
// a reopen. Everything else is per connection and therefore set in the DSN so
// that every connection the pool opens carries it:
//
//   - foreign_keys=ON      enforcing the referential integrity declared by the schema
//   - busy_timeout         waiting instead of failing when another process holds the lock
//   - synchronous=FULL     committing durably: critical user data must survive power loss
//   - wal_autocheckpoint   bounding WAL growth during long downloads
//
// # Concurrency
//
// SQLite allows many concurrent readers and exactly one writer. The write pool
// therefore holds a single connection and every write transaction is opened with
// BEGIN IMMEDIATE so that a transaction either owns the write lock from its first
// statement or waits in busy_timeout. Read connections never take a write lock.
package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite" // pure Go driver, no CGO and no external DLL
)

// FileName is the database file kept inside the private data directory.
const FileName = "oddward.db"

const (
	defaultBusyTimeout = 5 * time.Second
	defaultReaders     = 4
	// walAutocheckpointPages bounds how much WAL is allowed to accumulate before
	// SQLite copies pages back into the main database file. 1024 pages is 4 MiB
	// with the default page size.
	walAutocheckpointPages = 1024
	maxBusyTimeout         = 60 * time.Second
	maxReaders             = 16
)

// Errors reported by this package. Callers must treat them as described here
// rather than matching on driver error strings.
var (
	// ErrClosed is returned when the database has been closed.
	ErrClosed = errors.New("database: closed")
	// ErrFutureSchema means the file was written by a newer launcher. The build
	// refuses to touch it so an older version can never corrupt newer data.
	ErrFutureSchema = errors.New("database: schema is newer than this build")
	// ErrCorrupt means the file is not a usable Oddward database. It is never
	// deleted or recreated automatically.
	ErrCorrupt = errors.New("database: file is corrupt")
	// ErrUnsafePath means the database path or one of its ancestors is not a
	// plain local directory.
	ErrUnsafePath = errors.New("database: unsafe database path")
	// ErrBusy means another process kept the lock for the whole wait window.
	ErrBusy = errors.New("database: locked by another process")
	// ErrTxReentrant means a statement asked for the writer while a transaction
	// was already open. The writer has a single connection, so waiting for it
	// would deadlock; the caller has to use the transaction handle instead.
	ErrTxReentrant = errors.New("database: writer requested inside a transaction; use the transaction handle")
)

// txScope marks the context handed to a Tx callback so a nested Exec fails fast
// instead of waiting forever on the single write connection.
type txScope struct{}

func inTx(ctx context.Context) bool {
	marked, _ := ctx.Value(txScope{}).(bool)
	return marked
}

// Options configures Open.
type Options struct {
	// Path is the absolute path of the database file.
	Path string
	// BusyTimeout bounds how long a statement waits for a contested lock.
	BusyTimeout time.Duration
	// Readers is the size of the read-only connection pool.
	Readers int
	// ReadOnly opens an existing file without migrating or writing to it. Used
	// to verify backups.
	ReadOnly bool
	// NoBackup suppresses the automatic pre-migration backup. Only tests set it.
	NoBackup bool
	// Now overrides the clock used for migration bookkeeping. Tests set it.
	Now func() time.Time
}

// DB is an open database. It is safe for concurrent use.
type DB struct {
	path    string
	opts    Options
	version int
	write   *sql.DB
	read    *sql.DB
	// mu serialises write transactions in this process. SQLite already allows a
	// single writer, so queueing here turns contention into ordinary waiting
	// instead of SQLITE_BUSY.
	mu        sync.Mutex
	closed    atomic.Bool
	closeOnce sync.Once
}

// Open validates the path, applies migrations and returns a ready database.
// A missing file is created; an existing one is never deleted or recreated.
func Open(ctx context.Context, opts Options) (*DB, error) {
	if opts.Now == nil {
		opts.Now = func() time.Time { return time.Now().UTC() }
	}
	if opts.BusyTimeout == 0 {
		opts.BusyTimeout = defaultBusyTimeout
	}
	if opts.Readers == 0 {
		opts.Readers = defaultReaders
	}
	if opts.BusyTimeout < 0 || opts.BusyTimeout > maxBusyTimeout {
		return nil, fmt.Errorf("database: busy timeout %s out of range", opts.BusyTimeout)
	}
	if opts.Readers < 1 || opts.Readers > maxReaders {
		return nil, fmt.Errorf("database: reader count %d out of range", opts.Readers)
	}
	path, err := checkDataPath(opts.Path)
	if err != nil {
		return nil, err
	}
	opts.Path = path
	if err := ensureDataDir(filepath.Dir(path), !opts.ReadOnly); err != nil {
		return nil, err
	}

	db := &DB{path: path, opts: opts}
	if opts.ReadOnly {
		if err := db.openPools(false); err != nil {
			db.closePools()
			return nil, err
		}
		version, err := db.userVersion(ctx)
		if err != nil {
			db.closePools()
			return nil, err
		}
		db.version = version
		return db, nil
	}

	existed, err := statDatabase(path)
	if err != nil {
		return nil, err
	}
	if err := db.openPools(true); err != nil {
		db.closePools()
		return nil, err
	}
	if err := db.prepare(ctx, existed); err != nil {
		db.closePools()
		return nil, err
	}
	return db, nil
}

// prepare checks the stored schema and applies pending migrations.
func (db *DB) prepare(ctx context.Context, existed bool) error {
	version, err := db.userVersion(ctx)
	if err != nil {
		return err
	}
	if existed && version > 0 {
		// A quick check costs a few milliseconds on a healthy file and catches a
		// truncated or overwritten database before any migration touches it.
		if err := db.QuickCheck(ctx); err != nil {
			return err
		}
	}
	if err := db.migrate(ctx, version); err != nil {
		return err
	}
	current, err := db.userVersion(ctx)
	if err != nil {
		return err
	}
	db.version = current
	return db.protectFiles()
}

func (db *DB) openPools(writable bool) error {
	write, err := sql.Open("sqlite", db.dsn(true))
	if err != nil {
		return fmt.Errorf("database: open %s: %w", db.path, err)
	}
	write.SetMaxOpenConns(1)
	write.SetMaxIdleConns(1)
	write.SetConnMaxLifetime(0)
	db.write = write
	if !writable {
		db.read = write
		return nil
	}
	read, err := sql.Open("sqlite", db.dsn(false))
	if err != nil {
		return fmt.Errorf("database: open %s: %w", db.path, err)
	}
	read.SetMaxOpenConns(db.opts.Readers)
	read.SetMaxIdleConns(db.opts.Readers)
	read.SetConnMaxLifetime(0)
	db.read = read
	return nil
}

func (db *DB) closePools() {
	if db.read != nil && db.read != db.write {
		_ = db.read.Close()
	}
	if db.write != nil {
		_ = db.write.Close()
	}
}

// dsn builds the driver URI. Paths are escaped because the URI parser inside
// SQLite decodes percent escapes, so a directory that happens to contain one of
// the three URI delimiters must not be able to change the parameters.
func (db *DB) dsn(primary bool) string {
	var b strings.Builder
	b.WriteString("file:")
	b.WriteString(escapeURI(db.path))
	b.WriteString("?mode=")
	if db.opts.ReadOnly {
		b.WriteString("ro")
	} else {
		b.WriteString("rwc")
	}
	b.WriteString("&_pragma=busy_timeout(")
	b.WriteString(strconv.FormatInt(db.opts.BusyTimeout.Milliseconds(), 10))
	b.WriteString(")")
	b.WriteString("&_pragma=foreign_keys(1)")
	if !db.opts.ReadOnly {
		b.WriteString("&_pragma=journal_mode(WAL)")
		b.WriteString("&_pragma=synchronous(FULL)")
		b.WriteString("&_pragma=wal_autocheckpoint(")
		b.WriteString(strconv.Itoa(walAutocheckpointPages))
		b.WriteString(")")
	}
	b.WriteString("&_pragma=temp_store(memory)")
	if primary && !db.opts.ReadOnly {
		// BEGIN IMMEDIATE: take the write lock up front so contention is handled
		// by busy_timeout instead of failing to upgrade a read snapshot later.
		b.WriteString("&_txlock=immediate")
	}
	return b.String()
}

func escapeURI(path string) string {
	r := strings.NewReplacer("%", "%25", "?", "%3F", "#", "%23")
	return r.Replace(filepath.ToSlash(path))
}

// Path returns the absolute database file path.
func (db *DB) Path() string { return db.path }

// Dir returns the private directory that holds the database.
func (db *DB) Dir() string { return filepath.Dir(db.path) }

// Version returns the schema version currently stored in the file.
func (db *DB) Version() int { return db.version }

// Writer returns the single-connection pool used for writes. Callers that need
// several statements to be atomic must use Tx instead of this pool.
func (db *DB) Writer() *sql.DB { return db.write }

// Reader returns the read pool. Queries that must observe one another's writes
// belong in a Tx on the writer instead.
func (db *DB) Reader() *sql.DB { return db.read }

// Exec runs a statement on the writer. It refuses to run inside a Tx callback:
// the writer has one connection and the open transaction owns it, so a nested
// Exec would block until the transaction ends, which is never.
func (db *DB) Exec(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if db.closed.Load() {
		return nil, ErrClosed
	}
	if inTx(ctx) {
		return nil, ErrTxReentrant
	}
	return db.write.ExecContext(ctx, query, args...)
}

// Query runs a read-only query on the read pool.
func (db *DB) Query(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if db.closed.Load() {
		return nil, ErrClosed
	}
	return db.read.QueryContext(ctx, query, args...)
}

// QueryRow runs a read-only query for a single row on the read pool.
func (db *DB) QueryRow(ctx context.Context, query string, args ...any) *sql.Row {
	return db.read.QueryRowContext(ctx, query, args...)
}

// Tx runs fn inside one write transaction. The transaction is rolled back when
// fn returns an error or panics, and committed otherwise. The context of every
// statement inside fn belongs to fn: cancelling it aborts the whole transaction
// so a cancelled operation can never leave a half-written domain state.
func (db *DB) Tx(ctx context.Context, fn func(context.Context, *sql.Tx) error) error {
	if db.closed.Load() {
		return ErrClosed
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if inTx(ctx) {
		return ErrTxReentrant
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	tx, err := db.write.BeginTx(ctx, nil)
	if err != nil {
		return classify(err, "begin transaction")
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if err := fn(context.WithValue(ctx, txScope{}, true), tx); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return classify(err, "commit transaction")
	}
	committed = true
	return nil
}

// Checkpoint folds WAL pages back into the main file. Close does this before
// releasing the pool so a stopped launcher leaves a complete database behind.
func (db *DB) Checkpoint(ctx context.Context) error {
	if db.closed.Load() {
		return ErrClosed
	}
	return db.checkpoint(ctx)
}

func (db *DB) checkpoint(ctx context.Context) error {
	if db.opts.ReadOnly {
		return nil
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	var mode string
	var logFrames, checkpointed int
	row := db.write.QueryRowContext(ctx, "PRAGMA wal_checkpoint(TRUNCATE)")
	if err := row.Scan(&mode, &logFrames, &checkpointed); err != nil {
		return classify(err, "checkpoint")
	}
	if mode == "busy" {
		return fmt.Errorf("%w: checkpoint could not complete", ErrBusy)
	}
	return nil
}

// Close checkpoints the WAL and releases every connection. It is idempotent.
func (db *DB) Close() error {
	var err error
	db.closeOnce.Do(func() {
		db.closed.Store(true)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		if cerr := db.checkpoint(ctx); cerr != nil {
			err = cerr
		}
		cancel()
		db.closePools()
	})
	return err
}

// userVersion reads PRAGMA user_version, the schema generation stored in the
// database header.
func (db *DB) userVersion(ctx context.Context) (int, error) {
	var version int
	if err := db.write.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
		return 0, classify(err, "read schema version")
	}
	return version, nil
}

// classify turns driver errors into the package's sentinel errors so callers do
// not depend on driver strings.
func classify(err error, op string) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "database is locked"), strings.Contains(msg, "database table is locked"), strings.Contains(msg, "sqlite_busy"):
		return fmt.Errorf("%w: %s: %v", ErrBusy, op, err)
	case strings.Contains(msg, "not a database"), strings.Contains(msg, "malformed"), strings.Contains(msg, "database disk image is corrupt"), strings.Contains(msg, "sqlite_corrupt"):
		return fmt.Errorf("%w: %s: %v", ErrCorrupt, op, err)
	}
	return fmt.Errorf("database: %s: %w", op, err)
}

// statDatabase reports whether the database file already exists and rejects a
// path that is not a plain regular file.
func statDatabase(path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.Mode().IsRegular() {
		return false, fmt.Errorf("%w: %s is not a regular file", ErrUnsafePath, path)
	}
	return true, nil
}
