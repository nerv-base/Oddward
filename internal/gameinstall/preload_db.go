// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/nerv-base/Oddward/internal/database"
	"github.com/nerv-base/Oddward/internal/gameres"
)

// Preload records follow the same rule as installation records: the database is
// authoritative and preload.json stays a mirror, because a build the self-updater
// rolled back to cannot read the database. The advisory plan.json is not tracked:
// it is never an authority, so a disagreement about it is not worth reconciling.
const (
	// PreloadDomain is this package's preload row in compat_mirrors.
	PreloadDomain = "preload"
	// PreloadImportVersion is the preload.json shape ImportLegacy understands.
	// PreloadImportVersion 2 registers every preload mirror for the same reason.
	PreloadImportVersion = 2
)

const (
	// preloadFileName is the legacy record document, one per branch and version.
	preloadFileName = "preload.json"
	// preloadPlanName is the advisory snapshot callers may display but must not
	// execute; it is written next to the record but never read back as authority.
	preloadPlanName = "plan.json"
	// preloadMaxBytes bounds every preload document this package reads or writes.
	preloadMaxBytes = 16 << 20
)

// NewPreloadStore binds preload records to the database. StateRoot keeps the
// per-record mirror and the advisory plan, WorkRoot keeps the downloaded blobs,
// which stay on the filesystem.
func NewPreloadStore(db *database.DB, stateRoot, workRoot string, engineCache bool) (PreloadStore, error) {
	if db == nil {
		return PreloadStore{}, errors.New("preload store needs an open database")
	}
	if !filepath.IsAbs(stateRoot) || !filepath.IsAbs(workRoot) {
		return PreloadStore{}, fmt.Errorf("%w: state and work roots must be absolute", ErrPreloadRecord)
	}
	return PreloadStore{
		db:          db,
		StateRoot:   filepath.Clean(stateRoot),
		WorkRoot:    filepath.Clean(workRoot),
		EngineCache: engineCache,
	}, nil
}

// preloadJSON encodes a record exactly as the legacy document was encoded.
func preloadJSON(r PreloadRecord) ([]byte, error) {
	data, err := json.Marshal(r)
	if err != nil {
		return nil, fmt.Errorf("%w: preload record: %v", ErrPreloadRecord, err)
	}
	if len(data) > preloadMaxBytes {
		return nil, fmt.Errorf("%w: preload record is larger than %d bytes", ErrPreloadRecord, preloadMaxBytes)
	}
	return data, nil
}

// preloadQuerier is the read half of the store and of its import transaction.
type preloadQuerier interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// preloadEntry is a stored record plus the bookkeeping column the importer needs.
type preloadEntry struct {
	record PreloadRecord
	// updatedAt is the record's own recency, which the importer compares against a
	// mirror's modification time without decoding the stored documents.
	updatedAt time.Time
}

const preloadSelect = `SELECT task_id, apply_txn_id, schema, state, route, list_hash,
	saved_at, updated_at, target_seen_at, budget, observed_versions, targets,
	reused, files, patches
	FROM preload_records WHERE install_key = ? AND branch = ? AND version = ?`

const preloadUpsert = `INSERT INTO preload_records (
	install_key, branch, version, task_id, apply_txn_id, schema, state, route,
	list_hash, saved_at, updated_at, target_seen_at, budget, observed_versions,
	targets, reused, files, patches, plan
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(install_key, branch, version) DO UPDATE SET
	task_id = excluded.task_id,
	apply_txn_id = excluded.apply_txn_id,
	schema = excluded.schema,
	state = excluded.state,
	route = excluded.route,
	list_hash = excluded.list_hash,
	saved_at = excluded.saved_at,
	updated_at = excluded.updated_at,
	target_seen_at = excluded.target_seen_at,
	budget = excluded.budget,
	observed_versions = excluded.observed_versions,
	targets = excluded.targets,
	reused = excluded.reused,
	files = excluded.files,
	patches = excluded.patches,
	plan = excluded.plan`

// readPreloadEntry reads one record. A document column that does not decode is a
// corrupt row, not a missing one: the state machine reads these documents whole,
// so half a record cannot be repaired here.
func readPreloadEntry(ctx context.Context, q preloadQuerier, installKey, branch, version string) (preloadEntry, bool, error) {
	entry := preloadEntry{}
	var savedAt, updatedAt, seenAt sql.NullInt64
	var budget, observed, targets, reused, files, patches sql.NullString
	err := q.QueryRowContext(ctx, preloadSelect, installKey, branch, version).Scan(
		&entry.record.TaskID, &entry.record.ApplyTxnID, &entry.record.Schema,
		&entry.record.State, &entry.record.Route, &entry.record.ListHash,
		&savedAt, &updatedAt, &seenAt, &budget, &observed, &targets, &reused,
		&files, &patches,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return preloadEntry{}, false, nil
	}
	if err != nil {
		return preloadEntry{}, false, fmt.Errorf("preload record %s %s: %w", branch, version, err)
	}
	entry.record.Branch = branch
	entry.record.Version = version
	entry.record.SavedAt = database.StampTime(savedAt)
	entry.record.UpdatedAt = database.StampTime(updatedAt)
	entry.record.TargetVersionSeenAt = database.StampTime(seenAt)
	entry.updatedAt = entry.record.UpdatedAt
	// A NULL document column reads back as the empty value it was saved with; the
	// schema allows NULL because only identity and recency have to be queryable.
	for _, column := range []struct {
		name string
		text sql.NullString
		out  any
	}{
		{"budget", budget, &entry.record.Budget},
		{"observedVersions", observed, &entry.record.ObservedFormalVersions},
		{"targets", targets, &entry.record.Targets},
		{"reused", reused, &entry.record.Reused},
		{"files", files, &entry.record.Files},
		{"patches", patches, &entry.record.Patches},
	} {
		if !column.text.Valid {
			continue
		}
		if err := json.Unmarshal([]byte(column.text.String), column.out); err != nil {
			return preloadEntry{}, false, fmt.Errorf("%w: preload record %s %s %s: %v", ErrPreloadRecord, branch, version, column.name, err)
		}
	}
	return entry, true, nil
}

// putPreloadRecord writes one record inside the caller's transaction. plan is the
// advisory snapshot the record was written with, or nil when an older build wrote
// the document and only the record itself was recovered.
func putPreloadRecord(ctx context.Context, tx *sql.Tx, installKey string, r PreloadRecord, plan []byte) error {
	budget, err := json.Marshal(r.Budget)
	if err != nil {
		return fmt.Errorf("%w: preload record %s %s: %v", ErrPreloadRecord, r.Branch, r.Version, err)
	}
	observed, err := json.Marshal(r.ObservedFormalVersions)
	if err != nil {
		return fmt.Errorf("%w: preload record %s %s: %v", ErrPreloadRecord, r.Branch, r.Version, err)
	}
	targets, err := json.Marshal(r.Targets)
	if err != nil {
		return fmt.Errorf("%w: preload record %s %s: %v", ErrPreloadRecord, r.Branch, r.Version, err)
	}
	files, err := json.Marshal(r.Files)
	if err != nil {
		return fmt.Errorf("%w: preload record %s %s: %v", ErrPreloadRecord, r.Branch, r.Version, err)
	}
	reused, err := preloadTargetsColumn(r.Reused)
	if err != nil {
		return fmt.Errorf("%w: preload record %s %s: %v", ErrPreloadRecord, r.Branch, r.Version, err)
	}
	patches, err := preloadObjectsColumn(r.Patches)
	if err != nil {
		return fmt.Errorf("%w: preload record %s %s: %v", ErrPreloadRecord, r.Branch, r.Version, err)
	}
	if _, err := tx.ExecContext(ctx, preloadUpsert,
		installKey, r.Branch, r.Version, r.TaskID, r.ApplyTxnID, r.Schema, string(r.State),
		string(r.Route), r.ListHash, database.Stamp(r.SavedAt),
		database.Stamp(r.UpdatedAt), database.Stamp(r.TargetVersionSeenAt),
		string(budget), string(observed), string(targets), reused, string(files),
		patches, planColumn(plan),
	); err != nil {
		return fmt.Errorf("preload record %s %s: %w", r.Branch, r.Version, err)
	}
	return nil
}

// preloadTargetsColumn encodes an optional target list. A list that was never
// built stays NULL so the record reads back with the same nil it was saved with.
func preloadTargetsColumn(v []PreloadTarget) (any, error) {
	if v == nil {
		return nil, nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(data), nil
}

func preloadObjectsColumn(v []gameres.Object) (any, error) {
	if v == nil {
		return nil, nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return string(data), nil
}

func planColumn(plan []byte) any {
	if len(plan) == 0 {
		return nil
	}
	return string(plan)
}

// preloadPlanSnapshot is the advisory document preload state machine callers may
// display. It is deliberately smaller than the record: a plan a user sees must not
// be mistaken for the authority the record holds.
type preloadPlanSnapshot struct {
	Schema   int             `json:"schema"`
	Branch   string          `json:"branch"`
	Version  string          `json:"version"`
	ListHash string          `json:"listHash"`
	Route    PreloadRoute    `json:"route"`
	Targets  []PreloadTarget `json:"targets"`
	Budget   PreloadBudget   `json:"budget"`
}

// Save makes a preload record durable.
//
// The advisory plan is written first, exactly as before, so a crash there leaves a
// plan newer than the record and never a record newer than its plan. The record and
// its mirror digest then commit together; the mirror itself is written after the
// commit, because a rolled-back build reads it and this build must never present a
// mirror the database does not agree with.
func (s PreloadStore) Save(ctx context.Context, r PreloadRecord) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	if err := s.requireDB(); err != nil {
		return err
	}
	if e := preloadValidateRecord(r); e != nil {
		return e
	}
	dir, e := preloadDirectory(s.StateRoot, r.Branch, r.Version, true)
	if e != nil {
		return e
	}
	plan, e := json.Marshal(preloadPlanSnapshot{
		r.Schema, r.Branch, r.Version, r.ListHash, r.Route, r.Targets, r.Budget,
	})
	if e != nil {
		return e
	}
	if e = preloadAtomicJSON(ctx, dir, preloadPlanName, plan); e != nil {
		return e
	}
	document, e := preloadJSON(r)
	if e != nil {
		return e
	}
	mirror := filepath.Join(dir, preloadFileName)
	if e := s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if e := putPreloadRecord(ctx, tx, s.StateRoot, r, plan); e != nil {
			return e
		}
		return s.db.RecordMirror(ctx, tx, PreloadDomain, mirror, database.Digest(document))
	}); e != nil {
		return fmt.Errorf("preload record not committed: %w", e)
	}
	if e := database.WriteMirror(mirror, document); e != nil {
		return fmt.Errorf("preload record committed, legacy mirror not written: %w", e)
	}
	return nil
}

// Load returns the record of one branch and version. Only the database is read: a
// missing mirror must not lose a record, and a stale mirror must not resurrect one.
func (s PreloadStore) Load(ctx context.Context, branch, version string) (PreloadRecord, error) {
	if e := ctx.Err(); e != nil {
		return PreloadRecord{}, e
	}
	if err := s.requireDB(); err != nil {
		return PreloadRecord{}, err
	}
	if !preloadBranchOK(branch) {
		return PreloadRecord{}, fmt.Errorf("%w: branch %q", ErrPreloadRecord, branch)
	}
	if _, ok := preloadCompareVersion(version, "0"); !ok {
		return PreloadRecord{}, fmt.Errorf("%w: version %q", ErrPreloadRecord, version)
	}
	entry, found, e := readPreloadEntry(ctx, s.db.Reader(), s.StateRoot, branch, version)
	if e != nil {
		return PreloadRecord{}, e
	}
	if !found {
		return PreloadRecord{}, &os.PathError{
			Op:   "open",
			Path: filepath.Join(s.StateRoot, "preload", branch, version, preloadFileName),
			Err:  os.ErrNotExist,
		}
	}
	if e := preloadValidateRecord(entry.record); e != nil {
		return PreloadRecord{}, fmt.Errorf("%w: preload record %s %s", e, branch, version)
	}
	return entry.record, ctx.Err()
}

// List returns every record of this installation, newest first. Callers that used
// to enumerate the preload mirror directories must read this instead: the database
// is authoritative, so a record whose mirror write failed, or whose mirror the
// older build removed, still shows up.
func (s PreloadStore) List(ctx context.Context) ([]PreloadRecord, error) {
	if e := ctx.Err(); e != nil {
		return nil, e
	}
	if err := s.requireDB(); err != nil {
		return nil, err
	}
	rows, err := s.db.Query(ctx,
		"SELECT branch, version FROM preload_records WHERE install_key = ? ORDER BY updated_at DESC, branch, version",
		s.StateRoot)
	if err != nil {
		return nil, err
	}
	type key struct{ branch, version string }
	keys := []key{}
	for rows.Next() {
		var k key
		if err := rows.Scan(&k.branch, &k.version); err != nil {
			rows.Close()
			return nil, err
		}
		keys = append(keys, k)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	records := make([]PreloadRecord, 0, len(keys))
	for _, k := range keys {
		r, err := s.Load(ctx, k.branch, k.version)
		if err != nil {
			return nil, err
		}
		records = append(records, r)
	}
	return records, ctx.Err()
}

// ImportPreloadLegacy adopts the preload.json mirrors below one installation state
// root. Startup knows the state roots before it knows their work roots, and this
// import reads only the mirrors: it never touches a blob, so the work root it binds
// is unused and equals the state root on purpose.
func ImportPreloadLegacy(ctx context.Context, db *database.DB, stateRoot string) error {
	if db == nil {
		return errors.New("preload store needs an open database")
	}
	if !filepath.IsAbs(stateRoot) {
		return fmt.Errorf("%w: state root must be absolute", ErrPreloadRecord)
	}
	store, err := NewPreloadStore(db, stateRoot, stateRoot, false)
	if err != nil {
		return err
	}
	_, err = db.RunImport(ctx, PreloadDomain, PreloadImportVersion, store.ImportLegacy)
	return err
}

// legacyPreload is one preload.json mirror under the state root.
type legacyPreload struct {
	path    string
	record  PreloadRecord
	digest  string
	modTime time.Time
}

// ImportLegacy reconciles the preload rows with the preload.json mirrors an older
// build may have written while it was in charge. It is the database.ImportFunc for
// PreloadDomain: no mirror is a no-op, and a mirror that cannot be read or
// validated fails naming the file before any row is touched.
//
// As for installations, a mirror is imported only when it is at least as new as the
// row it replaces; a mirror this build failed to update is older and is rewritten
// from the row after the commit instead.
func (s PreloadStore) ImportLegacy(ctx context.Context) (string, string, error) {
	if err := s.requireDB(); err != nil {
		return "", "", err
	}
	mirrors, err := s.legacyPreloads(ctx)
	if err != nil {
		return "", "", err
	}
	if len(mirrors) == 0 {
		return s.StateRoot, "", nil
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
	if err := s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		for _, mirror := range mirrors {
			stored, found, err := readPreloadEntry(ctx, tx, s.StateRoot, mirror.record.Branch, mirror.record.Version)
			if err != nil {
				return err
			}
			if found && mirror.modTime.Before(stored.updatedAt) {
				if err := preloadValidateRecord(stored.record); err != nil {
					return fmt.Errorf("%s: %w", mirror.path, err)
				}
				document, err := preloadJSON(stored.record)
				if err != nil {
					return err
				}
				if database.Digest(document) != mirror.digest {
					rewrites = append(rewrites, mirrorWrite{path: mirror.path, document: document})
					mirror.digest = database.Digest(document)
				}
				recorded = append(recorded, database.MirrorFile{Path: mirror.path, Digest: mirror.digest})
				continue
			}
			// The plan column stays NULL: the advisory payload of an older build is
			// regenerated by the next Save and is never an authority to recover.
			if err := putPreloadRecord(ctx, tx, s.StateRoot, mirror.record, nil); err != nil {
				return fmt.Errorf("%s: %w", mirror.path, err)
			}
			recorded = append(recorded, database.MirrorFile{Path: mirror.path, Digest: mirror.digest})
		}
		if err := s.db.DropMirrorsUnder(ctx, tx, PreloadDomain, filepath.Join(s.StateRoot, "preload")); err != nil {
			return err
		}
		return s.db.RecordMirrors(ctx, tx, PreloadDomain, recorded)
	}); err != nil {
		return "", "", err
	}
	for _, write := range rewrites {
		if err := database.WriteMirror(write.path, write.document); err != nil {
			return "", "", fmt.Errorf("%s: %w", write.path, err)
		}
	}
	return tracked.path, fmt.Sprintf("%d preload records", len(mirrors)), nil
}

// legacyPreloads lists the preload.json mirrors under the state root. Branch and
// version directory names are validated, so only a record this package could have
// written is considered.
func (s PreloadStore) legacyPreloads(ctx context.Context) ([]legacyPreload, error) {
	root, err := os.OpenRoot(s.StateRoot)
	if err != nil {
		return nil, fmt.Errorf("read preload mirrors: %w", err)
	}
	defer root.Close()
	branches, err := preloadDirs(root, "preload")
	if err != nil {
		return nil, fmt.Errorf("read preload mirrors: %w", err)
	}
	var mirrors []legacyPreload
	for _, branch := range branches {
		if !preloadBranchOK(branch) {
			continue
		}
		versions, err := preloadDirs(root, "preload/"+branch)
		if err != nil {
			return nil, fmt.Errorf("read preload mirrors: %w", err)
		}
		for _, version := range versions {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if _, ok := preloadCompareVersion(version, "0"); !ok {
				continue
			}
			file := "preload/" + branch + "/" + version + "/" + preloadFileName
			path := filepath.Join(s.StateRoot, "preload", branch, version, preloadFileName)
			document, modTime, err := preloadMirror(root, file)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			var record PreloadRecord
			if err := json.Unmarshal(document, &record); err != nil {
				return nil, fmt.Errorf("%s: %w: preload record: %v", path, ErrPreloadRecord, err)
			}
			if record.Branch != branch || record.Version != version {
				return nil, fmt.Errorf("%s: %w: record belongs to %s %s", path, ErrPreloadRecord, record.Branch, record.Version)
			}
			if err := preloadValidateRecord(record); err != nil {
				return nil, fmt.Errorf("%s: %w", path, err)
			}
			mirrors = append(mirrors, legacyPreload{
				path:    path,
				record:  record,
				digest:  database.Digest(document),
				modTime: modTime,
			})
		}
	}
	return mirrors, nil
}

// preloadDirs lists the plain directories below name inside root.
func preloadDirs(root *os.Root, name string) ([]string, error) {
	dir, err := root.Open(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	entries, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

// preloadMirror reads one bounded preload document and its modification time.
func preloadMirror(root *os.Root, name string) ([]byte, time.Time, error) {
	info, err := root.Lstat(name)
	if err != nil {
		return nil, time.Time{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > preloadMaxBytes {
		return nil, time.Time{}, ErrPreloadRecord
	}
	file, err := root.Open(name)
	if err != nil {
		return nil, time.Time{}, err
	}
	defer file.Close()
	document, err := io.ReadAll(io.LimitReader(file, preloadMaxBytes+1))
	if err != nil {
		return nil, time.Time{}, err
	}
	return document, info.ModTime(), nil
}
