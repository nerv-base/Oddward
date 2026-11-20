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
	legacyVersion = 1

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
