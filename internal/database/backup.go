// SPDX-License-Identifier: GPL-3.0-or-later

package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Backup writes a consistent copy of the database to dest, which must not exist.
// VACUUM INTO copies a committed snapshot rather than the raw pages, so a backup
// can be taken while the launcher is running its read pool. The result is
// verified before it is reported: a backup that cannot be opened and checked is
// deleted, because keeping it would only give false confidence.
func (db *DB) Backup(ctx context.Context, dest string) (err error) {
	if db.closed.Load() {
		return ErrClosed
	}
	if db.opts.ReadOnly {
		return errors.New("database: cannot back up a read-only handle")
	}
	clean, err := checkDataPath(dest)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(clean); err == nil {
		return fmt.Errorf("database: backup destination %s already exists", clean)
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := ensureDataDir(filepath.Dir(clean), true); err != nil {
		return err
	}
	db.mu.Lock()
	_, err = db.write.ExecContext(ctx, "VACUUM INTO ?", clean)
	db.mu.Unlock()
	if err != nil {
		return classify(err, "backup")
	}
	defer func() {
		if err != nil {
			_ = os.Remove(clean)
		}
	}()
	if err := protectFile(clean); err != nil {
		return err
	}
	return Verify(ctx, clean, db.version)
}

// Verify opens a database file read-only and checks that it is structurally
// sound and carries the expected schema version. It never migrates or writes.
func Verify(ctx context.Context, path string, wantVersion int) error {
	clean, err := checkDataPath(path)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(clean); err != nil {
		return err
	}
	db, err := Open(ctx, Options{Path: clean, ReadOnly: true, BusyTimeout: 2 * time.Second})
	if err != nil {
		return err
	}
	defer db.Close()
	if wantVersion > 0 && db.Version() != wantVersion {
		return fmt.Errorf("database: %s is schema version %d, want %d", clean, db.Version(), wantVersion)
	}
	return db.IntegrityCheck(ctx)
}
