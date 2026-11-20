// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/nerv-base/Oddward/internal/database"
)

// The database is authoritative for installation records. install.json stays a
// mirror: the self-updater can roll the launcher back to a build that cannot read
// the database, and that build must still see what was published. compat_mirrors
// remembers the digest of the mirror written with each record; when the file
// disagrees at startup an older build changed it and RunImport reconciles the rows
// with it.
const (
	// InstallDomain is this package's row in compat_mirrors and migration_state.
	InstallDomain = "install"
	// InstallImportVersion is the install.json shape ImportLegacy understands. It
	// changes when the importer learns a legacy document it did not migrate before.
	// InstallImportVersion 2 registers every installation mirror, so a database
	// filled by the first importer runs once more and learns the untracked files.
	InstallImportVersion = 2
)

// installFileName is the legacy record kept per installation.
const installFileName = "install.json"

// installMirrorPath is the legacy document for one installation. It stays inside
// the state root next to the journals so an installation's files travel together.
func installMirrorPath(stateRoot, key string) string {
	return filepath.Join(stateRoot, key, installFileName)
}

// installJSON encodes a record exactly as the legacy file was encoded, so a
// rolled-back build reads the document this build used to write.
func installJSON(record InstallRecord) ([]byte, error) {
	data, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("%w: install record: %v", ErrInvalidState, err)
	}
	if len(data) > stateMaxBytes {
		return nil, fmt.Errorf("%w: install record is larger than %d bytes", ErrInvalidState, stateMaxBytes)
	}
	return data, nil
}

// installKeyName reports whether a directory name is shaped like an install key.
// Anything else under the state root belongs to another part of the launcher.
func installKeyName(name string) bool {
	if len(name) != 16 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// installQuerier is the read half of the store and of its import transaction.
type installQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// installEntry is a stored record plus the bookkeeping column the importer needs to
// tell a mirror an older build wrote from a mirror this build failed to update.
type installEntry struct {
	record    InstallRecord
	updatedAt time.Time
}

const installSelect = `SELECT schema, root, mode, state, branch, version, list_hash,
	manifest_digest, txn_id, base_version, verify_coverage, verified_targets,
	published_at, created_at, root_file_id, official_digest, baseline_mismatch, updated_at
	FROM install_records WHERE install_key = ?`

// installTargetsSelect relies on rowid order. install_targets has no ordering
// column and the record's order is the manifest order the publication comparison
// and the verification progress report rely on, so putInstallRecord rewrites the
// rows in record order and they read back in that order.
const installTargetsSelect = `SELECT path, md5, size FROM install_targets
	WHERE install_key = ? ORDER BY rowid`

const installUpsert = `INSERT INTO install_records (
	install_key, schema, root, mode, state, branch, version, list_hash,
	manifest_digest, txn_id, base_version, verify_coverage, verified_targets,
	published_at, created_at, root_file_id, official_digest, baseline_mismatch, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(install_key) DO UPDATE SET
	schema = excluded.schema,
	root = excluded.root,
	mode = excluded.mode,
	state = excluded.state,
	branch = excluded.branch,
	version = excluded.version,
	list_hash = excluded.list_hash,
	manifest_digest = excluded.manifest_digest,
	txn_id = excluded.txn_id,
	base_version = excluded.base_version,
	verify_coverage = excluded.verify_coverage,
	verified_targets = excluded.verified_targets,
	published_at = excluded.published_at,
	created_at = excluded.created_at,
	root_file_id = excluded.root_file_id,
	official_digest = excluded.official_digest,
	baseline_mismatch = excluded.baseline_mismatch,
	updated_at = excluded.updated_at`

// readInstallEntry reads one record with its targets.
func readInstallEntry(ctx context.Context, q installQuerier, key string) (installEntry, bool, error) {
	entry := installEntry{}
	var publishedAt, createdAt, updatedAt sql.NullInt64
	var mismatch int64
	err := q.QueryRowContext(ctx, installSelect, key).Scan(
		&entry.record.Schema, &entry.record.Root, &entry.record.Mode, &entry.record.State,
		&entry.record.Branch, &entry.record.Version, &entry.record.ListHash,
		&entry.record.ManifestDigest, &entry.record.TxnID, &entry.record.BaseVersion,
		&entry.record.VerifyCoverage, &entry.record.VerifiedTargets, &publishedAt,
		&createdAt, &entry.record.RootFileID, &entry.record.OfficialDigest, &mismatch,
		&updatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return installEntry{}, false, nil
	}
	if err != nil {
		return installEntry{}, false, fmt.Errorf("installation record %s: %w", key, err)
	}
	entry.record.InstallKey = key
	entry.record.PublishedAt = database.StampTime(publishedAt)
	entry.record.CreatedAt = database.StampTime(createdAt)
	entry.record.BaselineMismatch = mismatch != 0
	entry.updatedAt = database.StampTime(updatedAt)

	rows, err := q.QueryContext(ctx, installTargetsSelect, key)
	if err != nil {
		return installEntry{}, false, fmt.Errorf("installation record %s: %w", key, err)
	}
	defer rows.Close()
	for rows.Next() {
		var target InstallTarget
		if err := rows.Scan(&target.Path, &target.MD5, &target.Size); err != nil {
			return installEntry{}, false, fmt.Errorf("installation record %s: %w", key, err)
		}
		entry.record.Targets = append(entry.record.Targets, target)
	}
	if err := rows.Err(); err != nil {
		return installEntry{}, false, fmt.Errorf("installation record %s: %w", key, err)
	}
	return entry, true, nil
}

// putInstallRecord writes the record and its verified targets inside the caller's
// transaction. The targets are replaced as a whole and reinserted in record order;
// see installTargetsSelect for why the order is preserved.
func putInstallRecord(ctx context.Context, tx *sql.Tx, record InstallRecord, now time.Time) error {
	if _, err := tx.ExecContext(ctx, installUpsert,
		record.InstallKey, record.Schema, record.Root, record.Mode, record.State,
		record.Branch, record.Version, record.ListHash, record.ManifestDigest,
		record.TxnID, record.BaseVersion, record.VerifyCoverage, record.VerifiedTargets,
		database.Stamp(record.PublishedAt), database.Stamp(record.CreatedAt),
		record.RootFileID, record.OfficialDigest, flagInt(record.BaselineMismatch),
		database.Stamp(now),
	); err != nil {
		return fmt.Errorf("installation record %s: %w", record.InstallKey, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM install_targets WHERE install_key = ?`, record.InstallKey); err != nil {
		return fmt.Errorf("installation record %s: %w", record.InstallKey, err)
	}
	for _, target := range record.Targets {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO install_targets (install_key, path, md5, size) VALUES (?, ?, ?, ?)`,
			record.InstallKey, target.Path, target.MD5, target.Size,
		); err != nil {
			return fmt.Errorf("installation record %s target %s: %w", record.InstallKey, target.Path, err)
		}
	}
	return nil
}

// ReadInstall returns the record of one canonical installation root.
//
// It keeps the signature it had when the record was a file: callers read the
// record while inspecting an installation and only some of them have a context,
// and the read is a single indexed row. The database is asked with a background
// context, exactly like the unbounded file read it replaces.
func (s *Store) ReadInstall(root string) (InstallRecord, error) {
	key, err := InstallKey(root)
	if err != nil {
		return InstallRecord{}, err
	}
	entry, found, err := readInstallEntry(context.Background(), s.db.Reader(), key)
	if err != nil {
		return InstallRecord{}, err
	}
	if !found {
		// An installation that was never adopted has no record. Report it as the
		// missing install.json it replaced so callers keep treating absence as
		// "not ours" instead of as a database failure.
		return InstallRecord{}, &os.PathError{Op: "open", Path: installMirrorPath(s.stateRoot, key), Err: os.ErrNotExist}
	}
	record := entry.record
	if err := stateValidateRecord(record); err != nil {
		return InstallRecord{}, fmt.Errorf("%w: installation record %s", err, key)
	}
	id, err := installRootFileID(root)
	if err != nil {
		return InstallRecord{}, fmt.Errorf("%w: root identity unavailable: %v", ErrStaleState, err)
	}
	if record.RootFileID != "" && record.RootFileID != "unknown" && record.RootFileID != id {
		return InstallRecord{}, fmt.Errorf("%w: root identity changed", ErrStaleState)
	}
	if record.OfficialDigest != "" {
		installRoot, err := os.OpenRoot(root)
		if err != nil {
			return InstallRecord{}, err
		}
		err = verifyCheckOfficial(installRoot, record)
		installRoot.Close()
		if err != nil {
			return InstallRecord{}, err
		}
	}
	return record, nil
}

func (s *Store) writeInstall(ctx context.Context, record InstallRecord) error {
	return s.writeInstallCheckpoint(ctx, record, nil)
}

// writeInstallCheckpoint makes a record durable. Its single transaction replaces
// the rename the filesystem protocol used, and beforeCommit replaces the callback
// that ran before that rename, so the ordering is unchanged: the caller fsyncs and
// publishes the new files first, and only then does the record start to exist.
//
// Both failure directions keep a recovery rule:
//
//   - The commit succeeded and the mirror write failed. The row is authoritative
//     and the installation is published: txn_recover sees the published record,
//     finishes the transaction and never rolls the filesystem back. The mirror is
//     stale until the next successful write, and ImportLegacy refuses to import a
//     mirror older than its row, so the row cannot be rolled back either.
//   - The files were published and the commit failed. No row changed and the
//     journal still records the transaction, so txn_recover validates every
//     candidate and backup file and rolls the filesystem back to the base record.
//
// The record, its targets and the mirror digest commit together, which is why a
// failed transaction cannot leave half a record behind.
func (s *Store) writeInstallCheckpoint(ctx context.Context, record InstallRecord, beforeCommit func() error) error {
	if err := stateValidateRecord(record); err != nil {
		return err
	}
	if record.RootFileID == "" {
		id, err := installRootFileID(record.Root)
		if err != nil {
			return err
		}
		record.RootFileID = id
	}
	document, err := installJSON(record)
	if err != nil {
		return err
	}
	if beforeCommit != nil {
		if err := beforeCommit(); err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	mirror := installMirrorPath(s.stateRoot, record.InstallKey)
	if err := s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := putInstallRecord(ctx, tx, record, now); err != nil {
			return err
		}
		return s.db.RecordMirror(ctx, tx, InstallDomain, mirror, database.Digest(document))
	}); err != nil {
		return fmt.Errorf("install record not committed: %w", err)
	}
	if err := database.WriteMirror(mirror, document); err != nil {
		return fmt.Errorf("install record committed, legacy mirror not written: %w", err)
	}
	return nil
}

// legacyInstall is one install.json mirror under the state root.
type legacyInstall struct {
	path    string
	record  InstallRecord
	digest  string
	modTime time.Time
}

// ImportLegacy reconciles the installation rows with the install.json mirrors an
// older build may have written while it was in charge. It is the
// database.ImportFunc for InstallDomain: no mirror is a no-op, and a mirror that
// cannot be read or validated fails naming the file before any row is touched.
//
// A mirror is imported only when it is at least as new as the row it replaces,
// because a mirror this build failed to update is older than the committed row and
// importing it would publish an installation that was already rolled forward. Such
// a mirror is rewritten from the row after the commit, which is also what ends the
// disagreement compat_mirrors reported.
func (s *Store) ImportLegacy(ctx context.Context) (string, string, error) {
	mirrors, err := s.legacyInstalls(ctx)
	if err != nil {
		return "", "", err
	}
	if len(mirrors) == 0 {
		return s.stateRoot, "", nil
	}
	tracked := mirrors[0]
	for _, mirror := range mirrors[1:] {
		if mirror.modTime.After(tracked.modTime) {
			tracked = mirror
		}
	}
	type mirrorWrite struct {
		path     string
		document []byte
	}
	var rewrites []mirrorWrite
	recorded := make([]database.MirrorFile, 0, len(mirrors))
	now := time.Now().UTC()
	if err := s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for _, mirror := range mirrors {
			stored, found, err := readInstallEntry(ctx, tx, mirror.record.InstallKey)
			if err != nil {
				return err
			}
			if found && mirror.modTime.Before(stored.updatedAt) {
				if err := stateValidateRecord(stored.record); err != nil {
					return fmt.Errorf("%s: %w", mirror.path, err)
				}
				document, err := installJSON(stored.record)
				if err != nil {
					return err
				}
				if database.Digest(document) != mirror.digest {
					rewrites = append(rewrites, mirrorWrite{path: mirror.path, document: document})
					mirror.digest = database.Digest(document)
				}
				// Every installation keeps its own mirror; a rolled-back build can
				// edit any of them, so all of them are tracked.
				recorded = append(recorded, database.MirrorFile{Path: mirror.path, Digest: mirror.digest})
				continue
			}
			if err := putInstallRecord(ctx, tx, mirror.record, now); err != nil {
				return fmt.Errorf("%s: %w", mirror.path, err)
			}
			recorded = append(recorded, database.MirrorFile{Path: mirror.path, Digest: mirror.digest})
		}
		if err := s.db.DropMirrorsUnder(ctx, tx, InstallDomain, s.stateRoot); err != nil {
			return err
		}
		return s.db.RecordMirrors(ctx, tx, InstallDomain, recorded)
	}); err != nil {
		return "", "", err
	}
	for _, write := range rewrites {
		if err := database.WriteMirror(write.path, write.document); err != nil {
			return "", "", fmt.Errorf("%s: %w", write.path, err)
		}
	}
	return tracked.path, fmt.Sprintf("%d installation records", len(mirrors)), nil
}

// legacyInstalls lists the install.json mirrors under the state root. Only plain
// key directories holding a plain file are considered, so a link or an unrelated
// directory cannot feed the importer.
func (s *Store) legacyInstalls(ctx context.Context) ([]legacyInstall, error) {
	root, err := os.OpenRoot(s.stateRoot)
	if err != nil {
		return nil, fmt.Errorf("read install mirrors: %w", err)
	}
	defer root.Close()
	dir, err := root.Open(".")
	if err != nil {
		return nil, fmt.Errorf("read install mirrors: %w", err)
	}
	names, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		return nil, fmt.Errorf("read install mirrors: %w", err)
	}
	mirrors := make([]legacyInstall, 0, len(names))
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		key := name.Name()
		if !installKeyName(key) || !name.IsDir() {
			continue
		}
		path := installMirrorPath(s.stateRoot, key)
		file := key + "/" + installFileName
		info, err := root.Lstat(file)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		data, err := stateReadBounded(root, file)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		var record InstallRecord
		if err := stateDecodeJSON(data, &record); err != nil {
			return nil, fmt.Errorf("%s: %w: install record: %v", path, ErrInvalidState, err)
		}
		if record.InstallKey != key {
			return nil, fmt.Errorf("%s: %w: record belongs to %s", path, ErrStaleState, record.InstallKey)
		}
		// The importer validates the document, never the machine: a record for an
		// installation that has since been moved must still reach the database.
		if err := stateValidateRecord(record); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		mirrors = append(mirrors, legacyInstall{
			path:    path,
			record:  record,
			digest:  database.Digest(data),
			modTime: info.ModTime(),
		})
	}
	sort.Slice(mirrors, func(i, j int) bool { return mirrors[i].path < mirrors[j].path })
	return mirrors, nil
}

// flagInt stores a Go bool in a CHECK (x IN (0, 1)) column.
func flagInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
