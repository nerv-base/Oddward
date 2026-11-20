// SPDX-License-Identifier: GPL-3.0-or-later

package accounts

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nerv-base/Oddward/internal/database"
)

// The stub can roll the launcher back to a build that does not know the
// database. The database stays authoritative, so every change also rewrites the
// legacy files that build reads, and compat_mirrors records the digest of what
// this build wrote.
//
// compat_mirrors holds one row per domain, so the accounts domain cannot record
// one row per file. It records the index file below instead: the index lists the
// digest of every legacy file, so a file another build changed, added or deleted
// changes the index, and the next start re-runs the importer. The index is not a
// legacy file; the previous build ignores names it does not know.
const (
	// accountsDomain is the domain this package owns in compat_mirrors and
	// migration_state.
	accountsDomain = "accounts"
	// legacyVersion is the version of the on-disk layout the importer reads.
	legacyVersion = 2

	mirrorIndexName    = "mirrors.json"
	mirrorIndexVersion = 1
)

// mirrorIndex is the content of mirrorIndexName: one sorted digest per legacy
// file this build wrote.
type mirrorIndex struct {
	Version int          `json:"version"`
	Files   []mirrorFile `json:"files"`
}

type mirrorFile struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

// mirrorChange is what one write publishes: the legacy files it replaces and the
// files it removes. The removal order is the caller's, because removing an
// attachment before its account keeps a crash from leaving an orphan.
type mirrorChange struct {
	write  map[string][]byte
	remove []string
}

func (s *Store) mirrorPath(name string) string { return filepath.Join(s.dir, name) }

// isMirrorName reports whether name is one of the legacy files this store keeps
// in sync with its rows.
func isMirrorName(name string) bool {
	switch name {
	case subjectFileName, lastFileName, noticeFileName:
		return true
	}
	for _, ext := range []string{recordExt, tajiduoExt} {
		if id, ok := strings.CutSuffix(name, ext); ok && isLowerHex(id, 32) {
			return true
		}
	}
	return false
}

// mirrorSnapshot reads every legacy file currently in the directory. A file this
// package cannot read as a plain file is left out: it is not a mirror this build
// wrote, and the importer reports it instead of treating it as data.
func (s *Store) mirrorSnapshot() (map[string][]byte, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("accounts: read mirror directory: %w", err)
	}
	files := make(map[string][]byte, len(entries))
	for _, e := range entries {
		if !isMirrorName(e.Name()) {
			continue
		}
		raw, err := s.readFile(e.Name(), MaxRecordSize)
		if err != nil {
			continue
		}
		files[e.Name()] = raw
	}
	return files, nil
}

// mirrorIndexBytes encodes the digests of files. The order is fixed, so the same
// directory always produces the same bytes and an unchanged directory keeps the
// recorded digest.
func mirrorIndexBytes(files map[string][]byte) ([]byte, error) {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	index := mirrorIndex{Version: mirrorIndexVersion, Files: make([]mirrorFile, 0, len(names))}
	for _, name := range names {
		index.Files = append(index.Files, mirrorFile{Name: name, Digest: database.Digest(files[name])})
	}
	return json.Marshal(index)
}

// writeTx runs write in one transaction together with the mirror index record,
// then publishes the legacy files it reports. The rows and the digest of the
// mirrored bytes therefore never disagree; if the process dies before the files
// are written, the digest no longer matches and the next start reconciles the
// files into the database.
func (s *Store) writeTx(ctx context.Context, write func(context.Context, *sql.Tx) (mirrorChange, error)) error {
	if err := s.checkDir(); err != nil {
		return err
	}
	files, err := s.mirrorSnapshot()
	if err != nil {
		return err
	}
	var change mirrorChange
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var err error
		if change, err = write(ctx, tx); err != nil {
			return err
		}
		for name, data := range change.write {
			files[name] = data
		}
		for _, name := range change.remove {
			delete(files, name)
		}
		index, err := mirrorIndexBytes(files)
		if err != nil {
			return err
		}
		return s.db.RecordMirror(ctx, tx, accountsDomain, s.mirrorPath(mirrorIndexName), database.Digest(index))
	})
	if err != nil {
		return err
	}
	return s.publishMirrors(change, files)
}

// publishMirrors writes the changed legacy files, removes the stale ones and
// writes the index last, so the index never advertises bytes that are not on
// disk yet.
func (s *Store) publishMirrors(change mirrorChange, files map[string][]byte) error {
	names := make([]string, 0, len(change.write))
	for name := range change.write {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := database.WriteMirror(s.mirrorPath(name), change.write[name]); err != nil {
			return fmt.Errorf("accounts: write %s mirror: %w", name, err)
		}
	}
	for _, name := range change.remove {
		if err := s.removeMirror(name); err != nil {
			return err
		}
	}
	index, err := mirrorIndexBytes(files)
	if err != nil {
		return err
	}
	if err := database.WriteMirror(s.mirrorPath(mirrorIndexName), index); err != nil {
		return fmt.Errorf("accounts: write mirror index: %w", err)
	}
	return nil
}

// removeMirror deletes one legacy file. A missing file is not an error, so
// removals stay idempotent.
func (s *Store) removeMirror(name string) error {
	path := s.mirrorPath(name)
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("accounts: remove %s mirror: %w", name, err)
	}
	if err := removeRegular(path, fi); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("accounts: remove %s mirror: %w", name, err)
	}
	return nil
}

// refreshMirrorIndex rewrites the index from the files the directory holds now,
// without recording it. ImportLegacy calls it before RunImport so that the
// recorded digest only matches while no other build changed a mirror file.
func (s *Store) refreshMirrorIndex() error {
	files, err := s.mirrorSnapshot()
	if err != nil {
		return err
	}
	index, err := mirrorIndexBytes(files)
	if err != nil {
		return err
	}
	if current, err := os.ReadFile(s.mirrorPath(mirrorIndexName)); err == nil && string(current) == string(index) {
		return nil
	}
	if err := database.WriteMirror(s.mirrorPath(mirrorIndexName), index); err != nil {
		return fmt.Errorf("accounts: write mirror index: %w", err)
	}
	return nil
}

// mirrorChanged reports whether the legacy files differ from what this build last
// published. The recorded digest covers the index file, which lists every mirror
// file, so an edit, an addition and a deletion all change it.
func (s *Store) mirrorChanged(ctx context.Context) (bool, error) {
	mirrors, err := s.db.Mirrors(ctx, accountsDomain)
	if err != nil {
		return false, err
	}
	if len(mirrors) == 0 {
		return true, nil
	}
	recorded := mirrors[0]
	files, err := s.mirrorSnapshot()
	if err != nil {
		return false, err
	}
	index, err := mirrorIndexBytes(files)
	if err != nil {
		return false, err
	}
	return database.Digest(index) != recorded.Digest, nil
}

// rowsAreAhead reports whether the rows were written after the newest mirror
// file. A committed write whose file publication failed leaves exactly that
// state, and the rows then win: the alternative is importing the older file back
// over the account the user just saved.
func (s *Store) rowsAreAhead(ctx context.Context) (bool, error) {
	var rows sql.NullInt64
	// account_state is not consulted: this build does not republish the device
	// state files from rows, so their age must not decide the rule.
	for _, query := range []string{
		`SELECT max(updated_at) FROM accounts`,
		`SELECT max(updated_at) FROM account_tajiduo`,
	} {
		var stamp sql.NullInt64
		if err := s.db.Reader().QueryRowContext(ctx, query).Scan(&stamp); err != nil {
			return false, fmt.Errorf("accounts: read row age: %w", err)
		}
		if stamp.Int64 > rows.Int64 {
			rows = stamp
		}
	}
	if !rows.Valid {
		return false, nil
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return false, fmt.Errorf("accounts: read legacy directory: %w", err)
	}
	var newest time.Time
	for _, e := range entries {
		if !isMirrorName(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			return false, fmt.Errorf("accounts: inspect %s: %w", e.Name(), err)
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	if newest.IsZero() {
		// Nothing to reconcile: the directory holds no mirror file at all.
		return false, nil
	}
	return rows.Int64 > newest.UnixNano(), nil
}

// republishFromRows rewrites the account and attachment mirrors from the rows and
// records the new index. The device state files are left as they are: their rows
// are written by the same path, and rewriting them needs the protector the rows
// deliberately do not store.
func (s *Store) republishFromRows(ctx context.Context) error {
	files, err := s.mirrorSnapshot()
	if err != nil {
		return err
	}
	rows, err := s.db.Reader().QueryContext(ctx, `SELECT id, record FROM accounts`)
	if err != nil {
		return fmt.Errorf("accounts: read rows: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return fmt.Errorf("accounts: read rows: %w", err)
		}
		files[id+recordExt] = raw
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("accounts: read rows: %w", err)
	}
	rows, err = s.db.Reader().QueryContext(ctx, `SELECT account_id, file FROM account_tajiduo`)
	if err != nil {
		return fmt.Errorf("accounts: read attachments: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id string
		var raw []byte
		if err := rows.Scan(&id, &raw); err != nil {
			return fmt.Errorf("accounts: read attachments: %w", err)
		}
		files[id+tajiduoExt] = raw
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("accounts: read attachments: %w", err)
	}
	stored, err := s.mirrorSnapshot()
	if err != nil {
		return err
	}
	change := mirrorChange{write: map[string][]byte{}}
	for name, data := range files {
		if name == subjectFileName || name == lastFileName || name == noticeFileName {
			continue
		}
		change.write[name] = data
	}
	for name := range stored {
		if name == subjectFileName || name == lastFileName || name == noticeFileName {
			continue
		}
		if _, ok := files[name]; !ok {
			change.remove = append(change.remove, name)
		}
	}
	sort.Strings(change.remove)
	if err := s.publishMirrors(change, files); err != nil {
		return err
	}
	index, err := mirrorIndexBytes(files)
	if err != nil {
		return err
	}
	return s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return s.db.RecordMirror(ctx, tx, accountsDomain, s.mirrorPath(mirrorIndexName), database.Digest(index))
	})
}
