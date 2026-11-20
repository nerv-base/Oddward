// SPDX-License-Identifier: GPL-3.0-or-later

package database

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// The launcher can be rolled back to the previous build by the stub when a new
// version fails to start. The previous build does not know about the database, so
// every domain that used to keep a file keeps writing that file as a mirror: the
// database is authoritative, and the mirror exists only so that a rollback finds
// the data the newer build had already accepted.
//
// A mirror write is recorded in compat_mirrors with the digest of the bytes this
// build wrote. If the file on disk later differs from that digest, an older build
// ran and changed the file, so the domain must reconcile it into the database
// before its own rows are used again. RunImport does that automatically.

// Digest returns the digest recorded for a mirror, in the form compat_mirrors
// stores it.
func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Mirror is the recorded state of one legacy file.
type Mirror struct {
	Domain     string
	Path       string
	Digest     string
	MirroredAt time.Time
}

// RecordMirror stores the digest of the bytes a domain is writing to path. It must
// be called inside the same transaction as the domain's rows so that the record
// and the data can never disagree.
func (db *DB) RecordMirror(ctx context.Context, tx *sql.Tx, domain, path, digest string) error {
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO compat_mirrors(domain, path, digest, mirrored_at) VALUES(?, ?, ?, ?)
		ON CONFLICT(domain, path) DO UPDATE SET digest = excluded.digest, mirrored_at = excluded.mirrored_at`,
		domain, path, digest, db.now().UnixNano()); err != nil {
		return fmt.Errorf("database: record %s mirror: %w", domain, err)
	}
	return nil
}

// MirrorInTx writes the legacy file for domain inside the transaction that is
// about to carry the domain's rows, and records its digest in the same
// transaction. Use it when the file's bytes can only be derived from the rows
// the transaction just wrote: the file still lands before the commit, so a
// refused commit leaves the file ahead of the database, which MirrorStale
// reports and the next start reconciles by importing it.
//
// The file write happens while the transaction holds the single writer
// connection, so callers must keep the document small (these files are all a few
// hundred KiB at most) rather than streaming media through it.
func (db *DB) MirrorInTx(ctx context.Context, tx *sql.Tx, domain, path string, data []byte) error {
	if err := WriteMirror(path, data); err != nil {
		return fmt.Errorf("database: write %s mirror: %w", domain, err)
	}
	return db.RecordMirror(ctx, tx, domain, path, Digest(data))
}

// MirrorThenCommit writes the legacy file for domain and then commits fn along
// with the mirror record. The order matters: the file is written first, so a
// failed mirror write leaves the rows untouched and a later start can never
// mistake an older file for an edit made by a rolled-back build. If the process
// dies between the two steps the file is ahead of the database, and MirrorStale
// reports exactly that, which is what makes the import reconcile it.
//
// fn must only write rows; the caller validates the domain data before calling,
// because a rejecting fn still leaves the mirror written.
func (db *DB) MirrorThenCommit(ctx context.Context, domain, path string, data []byte, fn func(context.Context, *sql.Tx) error) error {
	digest := Digest(data)
	if err := WriteMirror(path, data); err != nil {
		return fmt.Errorf("database: write %s mirror: %w", domain, err)
	}
	return db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := fn(ctx, tx); err != nil {
			return err
		}
		return db.RecordMirror(ctx, tx, domain, path, digest)
	})
}

// WriteMirror replaces the legacy file with data through a private temporary file
// and a write-through rename. It is safe to call outside a transaction: a file
// whose digest is not recorded yet is treated as changed by another build and
// reconciled on the next start.
func WriteMirror(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := ensureDataDir(dir, true); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".mirror-*.tmp")
	if err != nil {
		return err
	}
	temp := f.Name()
	fail := func(err error) error {
		_ = f.Close()
		_ = os.Remove(temp)
		return err
	}
	if err := f.Chmod(0o600); err != nil {
		return fail(err)
	}
	if _, err := f.Write(data); err != nil {
		return fail(err)
	}
	if err := f.Sync(); err != nil {
		return fail(err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(temp)
		return err
	}
	if err := replaceMirrorFile(temp, path); err != nil {
		_ = os.Remove(temp)
		return err
	}
	return protectFile(path)
}

// MirrorStale reports whether any legacy file of domain was changed by another
// build since this one last wrote it. A file that no longer exists counts as
// changed; a domain with no recorded mirror is not stale.
func (db *DB) MirrorStale(ctx context.Context, domain string) (bool, error) {
	records, err := db.Mirrors(ctx, domain)
	if err != nil {
		return false, err
	}
	for _, record := range records {
		current, err := os.ReadFile(record.Path)
		if errors.Is(err, os.ErrNotExist) {
			return true, nil
		}
		if err != nil {
			return false, err
		}
		if Digest(current) != record.Digest {
			return true, nil
		}
	}
	return false, nil
}

// Mirrors returns every legacy file recorded for domain, ordered by path. A
// domain can write many files: gacha keeps one document per role, downloads one
// per task, and installs and preloads one per installation, so the rollback check
// has to look at all of them rather than just the last one written.
func (db *DB) Mirrors(ctx context.Context, domain string) ([]Mirror, error) {
	rows, err := db.Query(ctx,
		"SELECT domain, path, digest, mirrored_at FROM compat_mirrors WHERE domain = ? ORDER BY path", domain)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []Mirror{}
	for rows.Next() {
		var (
			record Mirror
			at     int64
		)
		if err := rows.Scan(&record.Domain, &record.Path, &record.Digest, &at); err != nil {
			return nil, err
		}
		record.MirroredAt = time.Unix(0, at).UTC()
		records = append(records, record)
	}
	return records, rows.Err()
}
