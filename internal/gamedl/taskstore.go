// SPDX-License-Identifier: GPL-3.0-or-later
package gamedl

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nerv-base/Oddward/internal/database"
	"github.com/nerv-base/Oddward/internal/gameres"
)

const (
	// Domain names this store in the migration ledger and in compat_mirrors.
	Domain = "downloads"
	// ImportVersion is the importer revision recorded once a legacy scan has
	// been reconciled into the database.
	ImportVersion = 1

	taskRecordSchema = 2
	maxTaskBytes     = 16 << 20
	maxTasks         = 10000
	// progressThrottle bounds how often a single transfer writes its progress.
	// One row per file per window is cheap, while a state change still writes
	// immediately because it is rare.
	progressThrottle = 200 * time.Millisecond
)

// Store keeps download tasks in SQLite. The state root still owns the legacy
// task mirrors and the caller's file lease; the database is authoritative and
// the JSON files exist only so a self-update rollback to a build that cannot
// read the database still has a resume point.
type Store struct {
	mu   sync.Mutex
	db   *database.DB
	root *os.Root
	dir  string

	now      func() time.Time
	throttle time.Duration
	// pending holds the newest progress sample per transfer. A sample is only
	// written once the throttle window has elapsed since the previous write, so
	// a slow link cannot turn chunk completion into a write per chunk.
	pending map[transferKey]progressSample
	flushes int
}

type transferKey struct {
	task    string
	ordinal int
}

// progressSample remembers the value last committed and the newest observed
// value. observed < written is impossible; observed > written is dirty.
type progressSample struct {
	at       time.Time
	written  int64
	observed int64
}

func NewStore(db *database.DB, stateRoot string) (*Store, error) {
	if db == nil {
		return nil, failure(ErrPathUnsafe, "store database", "", nil)
	}
	if err := safeRootPath(stateRoot); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(stateRoot)
	if err != nil {
		return nil, err
	}
	if err = mkdirSafe(root, "tasks"); err != nil {
		root.Close()
		return nil, err
	}
	return &Store{
		db:       db,
		root:     root,
		dir:      filepath.Clean(stateRoot),
		now:      time.Now,
		throttle: progressThrottle,
		pending:  map[transferKey]progressSample{},
	}, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Flush what the throttle still holds so a clean shutdown does not lose the
	// last observed byte counts. The chunk bitmap stays the resume authority, so
	// even a failed flush can only under-report progress.
	err := s.flushPending(context.Background(), "")
	return errors.Join(err, s.root.Close())
}

type taskRecord struct {
	Schema int             `json:"schema"`
	Task   json.RawMessage `json:"task"`
	CRC    uint32          `json:"crc32"`
}

func (s *Store) mirrorPath(id string) string {
	return filepath.Join(s.dir, "tasks", id+".json")
}

func (s *Store) Save(ctx context.Context, task Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.save(ctx, task)
}

func (s *Store) save(ctx context.Context, task Task) error {
	if err := ctx.Err(); err != nil {
		return classify("save task", task.ID, err)
	}
	if task.State == 0 {
		task.State = StatePlanning
	}
	task = normalizedTask(task)
	if err := validateTask(task); err != nil {
		return err
	}
	task.UpdatedAt = s.now().UTC()
	if task.CreatedAt.IsZero() {
		task.CreatedAt = task.UpdatedAt
	}
	raw, err := json.Marshal(task)
	if err != nil {
		return err
	}
	data, err := json.Marshal(taskRecord{Schema: taskRecordSchema, Task: raw, CRC: crc32.ChecksumIEEE(raw)})
	if err != nil {
		return err
	}
	if len(data) > maxTaskBytes {
		return failure(ErrRecoveryRequired, "task too large", task.ID, nil)
	}
	mirror := s.mirrorPath(task.ID)
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := putTask(ctx, tx, task); err != nil {
			return err
		}
		return s.db.RecordMirror(ctx, tx, Domain, mirror, database.Digest(data))
	})
	if err != nil {
		return err
	}
	// The full document supersedes every throttled sample for this task.
	for key := range s.pending {
		if key.task == task.ID {
			delete(s.pending, key)
		}
	}
	// The mirror is written after the commit on purpose: the row is already
	// authoritative, and a failure here only leaves files for the next start to
	// reconcile.
	if err := database.WriteMirror(mirror, data); err != nil {
		return fmt.Errorf("task committed, legacy mirror not written: %w", err)
	}
	return nil
}

func putTask(ctx context.Context, tx *sql.Tx, task Task) error {
	const statement = `INSERT INTO download_tasks
		(id, schema, install_key, purpose, cache_root, branch, target_version, list_hash, kind, state, err,
		 network_bytes, written_bytes, reused_bytes, cached_bytes, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
		 schema = excluded.schema, install_key = excluded.install_key, purpose = excluded.purpose,
		 cache_root = excluded.cache_root, branch = excluded.branch, target_version = excluded.target_version,
		 list_hash = excluded.list_hash, kind = excluded.kind, state = excluded.state, err = excluded.err,
		 network_bytes = excluded.network_bytes, written_bytes = excluded.written_bytes,
		 reused_bytes = excluded.reused_bytes, cached_bytes = excluded.cached_bytes,
		 created_at = excluded.created_at, updated_at = excluded.updated_at`
	_, err := tx.ExecContext(ctx, statement,
		task.ID, taskRecordSchema, task.InstallKey, task.Purpose, task.CacheRoot, task.Branch, task.TargetVersion,
		task.ListHash, int(task.Kind), int(task.State), task.Err, task.Counters.NetworkBytes,
		task.Counters.WrittenBytes, task.Counters.ReusedBytes, task.Counters.CachedBytes,
		database.Stamp(task.CreatedAt), database.Stamp(task.UpdatedAt))
	if err != nil {
		return err
	}
	for _, table := range []string{"download_transfers", "download_rebuilds", "download_targets"} {
		if _, err = tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE task_id = ?", task.ID); err != nil {
			return err
		}
	}
	for ordinal, tr := range task.Transfers {
		blocks, err := json.Marshal(tr.Blocks)
		if err != nil {
			return err
		}
		marks, err := json.Marshal(tr.Marks)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO download_transfers
			(task_id, ordinal, object_md5, object_size, object_key, source_kind, source_md5, source_size,
			 source_offset, source_length, chunk_size, bytes_done, state, patch_blocks_trusted, blocks, marks, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			task.ID, ordinal, tr.Object.MD5, storedObjectSize(tr.Object.Size), tr.Object.String(),
			int(tr.Source.Kind), tr.Source.Object.MD5, tr.Source.Object.Size, tr.Source.Offset, tr.Source.Size,
			tr.ChunkSize, tr.BytesDone, int(tr.State), boolInt(tr.PatchBlocksTrusted), string(blocks), string(marks),
			database.Stamp(task.UpdatedAt))
		if err != nil {
			return err
		}
	}
	for ordinal, rebuild := range task.Rebuilds {
		steps, err := json.Marshal(rebuild.Steps)
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO download_rebuilds
			(task_id, ordinal, target_md5, target_size, output, state, steps) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			task.ID, ordinal, rebuild.Target.MD5, rebuild.Target.Size, rebuild.Output, int(rebuild.State), string(steps))
		if err != nil {
			return err
		}
	}
	// download_targets has no ordinal column, so insertion order is the only
	// ordering signal; the primary key keeps paths unique per task.
	for _, target := range task.Targets {
		_, err = tx.ExecContext(ctx, `INSERT INTO download_targets
			(task_id, path, object_md5, object_size, action, state) VALUES (?, ?, ?, ?, ?, ?)`,
			task.ID, target.Path, target.Object.MD5, target.Object.Size, int(target.Action), int(target.State))
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) Load(ctx context.Context, id string) (Task, error) {
	return s.load(ctx, id)
}

func (s *Store) load(ctx context.Context, id string) (Task, error) {
	if !validID(id) {
		return Task{}, failure(ErrPathUnsafe, "task ID", id, nil)
	}
	if err := ctx.Err(); err != nil {
		return Task{}, classify("load task", id, err)
	}
	var (
		schema, kind, state                        int
		installKey, purpose, branch, targetVersion string
		cacheRoot, errText, listHash               sql.NullString
		network, written, reused, cached           int64
		createdAt, updatedAt                       sql.NullInt64
	)
	err := s.db.QueryRow(ctx, `SELECT schema, install_key, purpose, cache_root, branch, target_version, list_hash,
		kind, state, err, network_bytes, written_bytes, reused_bytes, cached_bytes, created_at, updated_at
		FROM download_tasks WHERE id = ?`, id).
		Scan(&schema, &installKey, &purpose, &cacheRoot, &branch, &targetVersion, &listHash,
			&kind, &state, &errText, &network, &written, &reused, &cached, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Task{}, os.ErrNotExist
	}
	if err != nil {
		return Task{}, err
	}
	if schema != taskRecordSchema {
		return Task{}, failure(ErrStaleTask, "task integrity", id, nil)
	}
	task := Task{
		ID:            id,
		InstallKey:    installKey,
		Purpose:       purpose,
		CacheRoot:     cacheRoot.String,
		Branch:        branch,
		TargetVersion: targetVersion,
		ListHash:      listHash.String,
		Kind:          TaskKind(kind),
		State:         TaskState(state),
		Err:           errText.String,
		Counters: Counters{
			NetworkBytes: network,
			WrittenBytes: written,
			ReusedBytes:  reused,
			CachedBytes:  cached,
		},
		CreatedAt: database.StampTime(createdAt),
		UpdatedAt: database.StampTime(updatedAt),
	}
	if task.Transfers, err = s.loadTransfers(ctx, id); err != nil {
		return Task{}, err
	}
	if task.Rebuilds, err = s.loadRebuilds(ctx, id); err != nil {
		return Task{}, err
	}
	if task.Targets, err = s.loadTargets(ctx, id); err != nil {
		return Task{}, err
	}
	if err = validateTask(task); err != nil {
		return Task{}, failure(ErrStaleTask, "task fields", id, err)
	}
	return task, nil
}

func (s *Store) loadTransfers(ctx context.Context, id string) ([]Transfer, error) {
	rows, err := s.db.Query(ctx, `SELECT ordinal, object_md5, object_size, object_key, source_kind, source_md5,
		source_size, source_offset, source_length, chunk_size, bytes_done, state, patch_blocks_trusted, blocks, marks
		FROM download_transfers WHERE task_id = ? ORDER BY ordinal`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var transfers []Transfer
	for rows.Next() {
		var (
			ordinal, sourceKind, objectState, trusted int
			sourceSize, sourceOffset, sourceLength    int64
			chunkSize, bytesDone, size                int64
			objectKey, objectMD5, sourceMD5           string
			blocks, marks                             string
		)
		if err = rows.Scan(&ordinal, &objectMD5, &size, &objectKey, &sourceKind, &sourceMD5, &sourceSize,
			&sourceOffset, &sourceLength, &chunkSize, &bytesDone, &objectState, &trusted, &blocks, &marks); err != nil {
			return nil, err
		}
		if ordinal != len(transfers) {
			return nil, failure(ErrStaleTask, "transfer order", id, nil)
		}
		object, err := objectFromRow(objectKey, size)
		if err != nil || object.MD5 != objectMD5 {
			return nil, failure(ErrStaleTask, "object identity", id, err)
		}
		transfer := Transfer{
			Object:             object,
			Source:             gameres.Source{Kind: gameres.SourceKind(sourceKind), Object: gameres.Object{MD5: sourceMD5, Size: sourceSize}, Offset: sourceOffset, Size: sourceLength},
			ChunkSize:          chunkSize,
			BytesDone:          bytesDone,
			State:              TransferState(objectState),
			PatchBlocksTrusted: trusted == 1,
		}
		if err = json.Unmarshal([]byte(blocks), &transfer.Blocks); err != nil {
			return nil, failure(ErrStaleTask, "transfer blocks", id, err)
		}
		if err = json.Unmarshal([]byte(marks), &transfer.Marks); err != nil {
			return nil, failure(ErrStaleTask, "object marks", id, err)
		}
		transfers = append(transfers, transfer)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return normalizedSlice(transfers), nil
}

func objectFromRow(key string, size int64) (gameres.Object, error) {
	n, ok := objectSizeFromKey(key)
	if !ok {
		return gameres.Object{}, errors.New("invalid object key")
	}
	object := gameres.Object{MD5: key[:strings.LastIndexByte(key, '.')], Size: n}
	if object.String() != key || storedObjectSize(n) != size {
		return gameres.Object{}, errors.New("object identity mismatch")
	}
	return object, nil
}

func objectSizeFromKey(key string) (int64, bool) {
	dot := strings.LastIndexByte(key, '.')
	if dot <= 0 {
		return 0, false
	}
	n, err := strconv.ParseInt(key[dot+1:], 10, 64)
	if err != nil || n < 0 || n > 1<<40 {
		return 0, false
	}
	return n, true
}

func (s *Store) loadRebuilds(ctx context.Context, id string) ([]Rebuild, error) {
	rows, err := s.db.Query(ctx, `SELECT ordinal, target_md5, target_size, output, state, steps
		FROM download_rebuilds WHERE task_id = ? ORDER BY ordinal`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rebuilds []Rebuild
	for rows.Next() {
		var ordinal, state int
		var targetMD5, output, steps string
		var targetSize int64
		if err = rows.Scan(&ordinal, &targetMD5, &targetSize, &output, &state, &steps); err != nil {
			return nil, err
		}
		if ordinal != len(rebuilds) {
			return nil, failure(ErrStaleTask, "rebuild order", id, nil)
		}
		rebuild := Rebuild{Target: gameres.Object{MD5: targetMD5, Size: targetSize}, Output: output, State: RebuildState(state)}
		if err = json.Unmarshal([]byte(steps), &rebuild.Steps); err != nil {
			return nil, failure(ErrStaleTask, "rebuild steps", id, err)
		}
		rebuilds = append(rebuilds, rebuild)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return normalizedSlice(rebuilds), nil
}

func (s *Store) loadTargets(ctx context.Context, id string) ([]InstallTarget, error) {
	// download_targets keys on path, so insertion order (rowid) is the only
	// stable ordering signal; it preserves the order Save wrote.
	rows, err := s.db.Query(ctx, `SELECT path, object_md5, object_size, action, state
		FROM download_targets WHERE task_id = ? ORDER BY rowid`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var targets []InstallTarget
	for rows.Next() {
		var path, objectMD5 string
		var objectSize int64
		var action, state int
		if err = rows.Scan(&path, &objectMD5, &objectSize, &action, &state); err != nil {
			return nil, err
		}
		targets = append(targets, InstallTarget{Path: path, Object: gameres.Object{MD5: objectMD5, Size: objectSize}, Action: gameres.ActionKind(action), State: TargetState(state)})
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	return normalizedSlice(targets), nil
}

func (s *Store) List(ctx context.Context) ([]Task, error) {
	if err := ctx.Err(); err != nil {
		return nil, classify("list tasks", "", err)
	}
	rows, err := s.db.Query(ctx, `SELECT id FROM download_tasks ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		if len(ids) == maxTasks {
			return nil, failure(ErrRecoveryRequired, "too many tasks", "tasks", nil)
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	tasks := make([]Task, 0, len(ids))
	for _, id := range ids {
		task, err := s.load(ctx, id)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
}

func strictDecode(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if decoder.Decode(new(any)) != io.EOF {
		return errors.New("trailing task data")
	}
	return nil
}

func openDirectory(root *os.Root, path string) (*os.File, error) {
	if err := checkPath(root, path); err != nil {
		return nil, err
	}
	return root.Open(path)
}

func validID(id string) bool {
	if len(id) == 0 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func validMD5(value string) bool {
	b, err := hex.DecodeString(value)
	return err == nil && len(b) == 16 && strings.ToLower(value) == value
}

func validObject(obj gameres.Object) bool {
	return validMD5(obj.MD5) && obj.Size >= 0 && obj.Size <= 1<<40
}

// storedObjectSize keeps the schema's positive object_size invariant without
// losing empty pak entries: the real size is part of object_key, and this
// column stores max(size, 1) so the row is still unique on (md5, size).
func storedObjectSize(size int64) int64 {
	return max(size, 1)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func validateTransfer(tr Transfer) error {
	if !validObject(tr.Object) || !validObject(tr.Source.Object) || tr.BytesDone < 0 || tr.BytesDone > tr.Object.Size || tr.State > TransferFailed {
		return failure(ErrPathUnsafe, "transfer identity", tr.Object.String(), nil)
	}
	switch tr.Source.Kind {
	case gameres.SourceCAS, gameres.SourcePatchBlob:
		if tr.Source.Object != tr.Object || tr.Source.Offset != 0 || (tr.Source.Size != 0 && tr.Source.Size != tr.Object.Size) {
			return failure(ErrPathUnsafe, "CAS identity", tr.Object.String(), nil)
		}
	case gameres.SourcePakRange:
		if tr.Source.Offset < 0 || tr.Source.Size != tr.Object.Size || tr.Source.Size > tr.Source.Object.Size || tr.Source.Offset > tr.Source.Object.Size-tr.Source.Size {
			return failure(ErrPathUnsafe, "pak range", tr.Object.String(), nil)
		}
	default:
		return failure(ErrPathUnsafe, "source kind", tr.Object.String(), nil)
	}
	if tr.ChunkSize < 0 || tr.ChunkSize > 64<<20 || (tr.ChunkSize > 0 && tr.ChunkSize < 1<<20 && tr.ChunkSize != tr.Object.Size) {
		return failure(ErrPathUnsafe, "chunk size", tr.Object.String(), nil)
	}
	if len(tr.Blocks) > 65536 || len(tr.Marks) > 64 {
		return failure(ErrPathUnsafe, "transfer metadata limit", tr.Object.String(), nil)
	}
	if len(tr.Blocks) > 0 {
		var offset int64
		for i, b := range tr.Blocks {
			if b.Index != i || b.Start != offset || b.Size <= 0 || b.Size > tr.Object.Size-offset || !validMD5(b.MD5) {
				return failure(ErrPathUnsafe, "block map", tr.Object.String(), nil)
			}
			offset += b.Size
		}
		if offset != tr.Object.Size {
			return failure(ErrPathUnsafe, "block coverage", tr.Object.String(), nil)
		}
	}
	for _, m := range tr.Marks {
		if len(m.Host) == 0 || len(m.Host) > 512 || strings.ContainsAny(m.Host, "/@\\\r\n") || len(m.URLKey) != 64 || len(m.ETag) > 1024 || len(m.LastModified) > 128 || strings.ContainsAny(m.ETag+m.LastModified, "\r\n") {
			return failure(ErrRecoveryRequired, "object mark", tr.Object.String(), nil)
		}
	}
	return nil
}

func validateTask(task Task) error {
	if !validID(task.ID) || !validMD5(task.ListHash) || task.Kind < TaskInstallFiles || task.Kind > TaskVerifyOnly || task.State > StateStale || len(task.Transfers) > 65536 || len(task.Targets) > 65536 || len(task.Rebuilds) > 4096 {
		return failure(ErrRecoveryRequired, "task fields", task.ID, nil)
	}
	if task.CacheRoot != "" && (task.Kind != TaskInstallFiles || !filepath.IsAbs(task.CacheRoot) || len(task.CacheRoot) > 1024 || strings.ContainsAny(task.CacheRoot, "\x00\r\n")) {
		return failure(ErrRecoveryRequired, "invalid external cache", task.ID, nil)
	}
	for _, s := range []string{task.InstallKey, task.Purpose, task.Branch, task.TargetVersion, task.Err} {
		if len(s) > 512 || strings.ContainsAny(s, "\x00\r\n") {
			return failure(ErrRecoveryRequired, "task string", task.ID, nil)
		}
	}
	objects := map[gameres.Object]bool{}
	for _, tr := range task.Transfers {
		if err := validateTransfer(tr); err != nil {
			return err
		}
		if objects[tr.Object] {
			return failure(ErrRecoveryRequired, "duplicate transfer", tr.Object.String(), nil)
		}
		objects[tr.Object] = true
	}
	paths := map[string]bool{}
	for _, target := range task.Targets {
		path, err := gameres.NormalizePath(target.Path)
		key := strings.ToUpper(path)
		if err != nil || path != target.Path || paths[key] || !validObject(target.Object) || target.Action < gameres.ActionReuseInstalled || target.Action > gameres.ActionFull || target.State > TargetFailed {
			return failure(ErrPathUnsafe, "install target", target.Path, err)
		}
		paths[key] = true
	}
	for path := range paths {
		for p, _, ok := strings.Cut(path, "/"); ok; {
			if paths[p] {
				return failure(ErrPathUnsafe, "target parent collision", path, nil)
			}
			rest := strings.TrimPrefix(path, p+"/")
			next, _, more := strings.Cut(rest, "/")
			p += "/" + next
			ok = more
		}
	}
	for _, r := range task.Rebuilds {
		if !validObject(r.Target) || len(r.Steps) == 0 || len(r.Steps) > 32 || r.State > RebuildFailed || (r.Output != "" && !safeRelative(r.Output)) {
			return failure(ErrRecoveryRequired, "rebuild fields", task.ID, nil)
		}
		for i, step := range r.Steps {
			if !validObject(step.Blob) || !validObject(step.Old) || !validObject(step.New) || (i > 0 && step.Old != r.Steps[i-1].New) {
				return failure(ErrRecoveryRequired, "rebuild chain", task.ID, nil)
			}
		}
		if r.Steps[len(r.Steps)-1].New != r.Target {
			return failure(ErrRecoveryRequired, "rebuild target", task.ID, nil)
		}
	}
	return nil
}

// normalizedTask makes every empty slice nil. The legacy document encoded nil
// and empty slices differently for transfers ("objects": null versus []), while
// validateTask treats them as the same value; normalizing at the write boundary
// lets Load rebuild exactly one of those forms instead of guessing.
func normalizedTask(task Task) Task {
	task.Transfers = normalizedSlice(task.Transfers)
	task.Rebuilds = normalizedSlice(task.Rebuilds)
	task.Targets = normalizedSlice(task.Targets)
	for i := range task.Transfers {
		task.Transfers[i].Blocks = normalizedSlice(task.Transfers[i].Blocks)
		task.Transfers[i].Marks = normalizedSlice(task.Transfers[i].Marks)
		// The schema names the four transfer states explicitly; a zero state only
		// ever meant "not started", which is TransferPending.
		if task.Transfers[i].State == 0 {
			task.Transfers[i].State = TransferPending
		}
	}
	for i := range task.Targets {
		if task.Targets[i].State == 0 {
			task.Targets[i].State = TargetPending
		}
	}
	for i := range task.Rebuilds {
		if task.Rebuilds[i].State == 0 {
			task.Rebuilds[i].State = RebuildPending
		}
	}
	return task
}

func normalizedSlice[T any](values []T) []T {
	if len(values) == 0 {
		return nil
	}
	return values
}

// UpdateTransferProgress records bytes_done for one transfer without rewriting
// the task document. It is throttled per transfer: samples inside the window are
// kept in memory and written by a later sample, a state update, a full Save or
// Close. Resume stays correct because Recover rebuilds BytesDone from the chunk
// bitmap, so a value lost by the throttle can only under-report progress; it can
// never claim bytes that are not on disk.
func (s *Store) UpdateTransferProgress(ctx context.Context, taskID string, ordinal int, bytesDone int64) error {
	if !validID(taskID) || ordinal < 0 || ordinal > 65535 || bytesDone < 0 {
		return failure(ErrPathUnsafe, "transfer progress", taskID, nil)
	}
	if err := ctx.Err(); err != nil {
		return classify("update transfer progress", taskID, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := transferKey{task: taskID, ordinal: ordinal}
	now := s.now()
	if sample, ok := s.pending[key]; ok && now.Sub(sample.at) < s.throttle {
		sample.observed = bytesDone
		s.pending[key] = sample
		return nil
	}
	if err := s.writeProgress(ctx, taskID, ordinal, bytesDone); err != nil {
		return err
	}
	s.pending[key] = progressSample{at: now, written: bytesDone, observed: bytesDone}
	s.flushes++
	return nil
}

func (s *Store) writeProgress(ctx context.Context, taskID string, ordinal int, bytesDone int64) error {
	now := s.now()
	return s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var objectKey string
		err := tx.QueryRowContext(ctx, `SELECT object_key FROM download_transfers WHERE task_id = ? AND ordinal = ?`, taskID, ordinal).Scan(&objectKey)
		if errors.Is(err, sql.ErrNoRows) {
			return failure(ErrStaleTask, "task transfer", taskID, nil)
		}
		if err != nil {
			return err
		}
		size, ok := objectSizeFromKey(objectKey)
		if !ok || bytesDone > size {
			return failure(ErrPathUnsafe, "transfer progress", taskID, nil)
		}
		_, err = tx.ExecContext(ctx, `UPDATE download_transfers SET bytes_done = ?, updated_at = ? WHERE task_id = ? AND ordinal = ?`,
			bytesDone, database.Stamp(now), taskID, ordinal)
		return err
	})
}

func (s *Store) flushPending(ctx context.Context, taskID string) error {
	var errs []error
	for key, sample := range s.pending {
		if taskID != "" && key.task != taskID {
			continue
		}
		delete(s.pending, key)
		if sample.observed == sample.written {
			continue
		}
		if err := s.writeProgress(ctx, key.task, key.ordinal, sample.observed); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// UpdateTransfer writes progress together with a state change, which is rare and
// therefore never throttled. The transition is checked against the stored state
// so a caller cannot resurrect a verified transfer or skip validation by using
// this path instead of Save.
func (s *Store) UpdateTransfer(ctx context.Context, taskID string, ordinal int, bytesDone int64, state TransferState) error {
	if !validID(taskID) || ordinal < 0 || ordinal > 65535 || bytesDone < 0 || state < TransferPending || state > TransferFailed {
		return failure(ErrPathUnsafe, "transfer update", taskID, nil)
	}
	if err := ctx.Err(); err != nil {
		return classify("update transfer", taskID, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	key := transferKey{task: taskID, ordinal: ordinal}
	err := s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		var objectKey string
		var stored int
		err := tx.QueryRowContext(ctx, `SELECT object_key, state FROM download_transfers WHERE task_id = ? AND ordinal = ?`, taskID, ordinal).Scan(&objectKey, &stored)
		if errors.Is(err, sql.ErrNoRows) {
			return failure(ErrStaleTask, "task transfer", taskID, nil)
		}
		if err != nil {
			return err
		}
		size, ok := objectSizeFromKey(objectKey)
		if !ok || bytesDone > size {
			return failure(ErrPathUnsafe, "transfer update", taskID, nil)
		}
		if !transferStateAllowed(TransferState(stored), state) {
			return failure(ErrRecoveryRequired, "transfer state", taskID, nil)
		}
		if _, err = tx.ExecContext(ctx, `UPDATE download_transfers SET bytes_done = ?, state = ?, updated_at = ? WHERE task_id = ? AND ordinal = ?`,
			bytesDone, int(state), database.Stamp(now), taskID, ordinal); err != nil {
			return err
		}
		result, err := tx.ExecContext(ctx, `UPDATE download_tasks SET updated_at = ? WHERE id = ?`, database.Stamp(now), taskID)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return failure(ErrStaleTask, "task state", taskID, nil)
		}
		return nil
	})
	if err != nil {
		return err
	}
	delete(s.pending, key)
	return nil
}

func transferStateAllowed(from, to TransferState) bool {
	if from == to {
		return true
	}
	switch from {
	case TransferPending:
		return to == TransferDownloading || to == TransferFailed
	case TransferDownloading:
		return to == TransferPending || to == TransferVerified || to == TransferFailed
	case TransferFailed:
		return to == TransferPending || to == TransferDownloading
	default: // TransferVerified is terminal until a full Save replaces the task.
		return false
	}
}

// UpdateTaskState records a task-level pause, failure or completion and flushes
// any progress the throttle still holds, so a paused task reports the bytes its
// transfers already wrote.
func (s *Store) UpdateTaskState(ctx context.Context, taskID string, state TaskState, errText string) error {
	if !validID(taskID) || state < StatePlanning || state > StateStale || len(errText) > 512 || strings.ContainsAny(errText, "\x00\r\n") {
		return failure(ErrPathUnsafe, "task state", taskID, nil)
	}
	if err := ctx.Err(); err != nil {
		return classify("update task state", taskID, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.flushPending(ctx, taskID); err != nil {
		return err
	}
	now := s.now()
	err := s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `UPDATE download_tasks SET state = ?, err = ?, updated_at = ? WHERE id = ?`,
			int(state), errText, database.Stamp(now), taskID)
		if err != nil {
			return err
		}
		n, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return failure(ErrStaleTask, "task state", taskID, nil)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for key := range s.pending {
		if key.task == taskID {
			delete(s.pending, key)
		}
	}
	return nil
}

// delete is the Retire path: it removes the mirror first, so a failure to delete
// rows cannot leave a file that would import the task back on the next start.
func (s *Store) delete(ctx context.Context, id string) error {
	if !validID(id) {
		return failure(ErrPathUnsafe, "task ID", id, nil)
	}
	if err := ctx.Err(); err != nil {
		return classify("delete task", id, err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	mirror := s.mirrorPath(id)
	if err := removeSafe(s.root, "tasks/"+id+".json"); err != nil {
		return err
	}
	err := s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DELETE FROM download_tasks WHERE id = ?`, id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `DELETE FROM compat_mirrors WHERE domain = ? AND path = ?`, Domain, mirror)
		return err
	})
	if err != nil {
		return err
	}
	for key := range s.pending {
		if key.task == id {
			delete(s.pending, key)
		}
	}
	return nil
}

type legacyTask struct {
	path string
	data []byte
	task Task
}

// ImportLegacy reconciles legacy task mirrors into the database. RunImport skips
// the scan once this build's written digest still matches the mirror; a mismatch
// means an older build changed the file after a rollback, so the scan runs again.
func (s *Store) ImportLegacy(ctx context.Context) (string, string, error) {
	var source, detail string
	ran, err := s.db.RunImport(ctx, Domain, ImportVersion, func(ctx context.Context) (string, string, error) {
		s.mu.Lock()
		defer s.mu.Unlock()
		var scanErr error
		source, detail, scanErr = s.importLegacy(ctx)
		return source, detail, scanErr
	})
	if err != nil {
		return source, "", err
	}
	if ran {
		return source, detail, nil
	}
	// The migration record belongs to the shared database, while a task store is
	// opened per installation. An installation whose files were never imported
	// still has to be folded in even though another installation already
	// completed the import, so look for task IDs this database has not seen.
	missing, err := s.hasUnimported(ctx)
	if err != nil {
		return filepath.Join(s.dir, "tasks"), "", err
	}
	if !missing {
		return source, "database already current", nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	source, detail, err = s.importLegacy(ctx)
	return source, detail, err
}

// hasUnimported reports whether the legacy task directory of this state root
// still contains a document whose row is absent from the database.
func (s *Store) hasUnimported(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, classify("import tasks", "", err)
	}
	dir, err := openDirectory(s.root, "tasks")
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	entries, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		return false, err
	}
	known := map[string]bool{}
	rows, err := s.db.Query(ctx, `SELECT id FROM download_tasks`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return false, err
		}
		known[id] = true
	}
	if err := rows.Err(); err != nil {
		return false, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if !known[strings.TrimSuffix(entry.Name(), ".json")] {
			return true, nil
		}
	}
	return false, nil
}

func (s *Store) importLegacy(ctx context.Context) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", classify("import tasks", "", err)
	}
	files, err := s.scanLegacy()
	if err != nil {
		return "", "", err
	}
	source := filepath.Join(s.dir, "tasks")
	if len(files) == 0 {
		return source, "no legacy task files", nil
	}
	keys := make([]string, 0, len(files))
	seen := map[string]bool{}
	representative := files[0]
	for _, file := range files {
		if !seen[file.task.InstallKey] {
			seen[file.task.InstallKey] = true
			keys = append(keys, file.task.InstallKey)
		}
		if file.task.UpdatedAt.After(representative.task.UpdatedAt) ||
			(file.task.UpdatedAt.Equal(representative.task.UpdatedAt) && file.task.ID > representative.task.ID) {
			representative = file
		}
	}
	sort.Strings(keys)
	// A task store is opened per installation, but the table is shared. Deleting
	// only the install keys seen in this state root replaces the rows this store
	// owns and leaves other installations' tasks alone.
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(keys)), ",")
		args := make([]any, len(keys))
		for i, key := range keys {
			args[i] = key
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM download_tasks WHERE install_key IN (`+placeholders+`)`, args...); err != nil {
			return err
		}
		for _, file := range files {
			if err := putTask(ctx, tx, file.task); err != nil {
				return err
			}
		}
		return s.db.RecordMirror(ctx, tx, Domain, representative.path, database.Digest(representative.data))
	})
	if err != nil {
		return representative.path, "", err
	}
	return representative.path, fmt.Sprintf("%d tasks", len(files)), nil
}

func (s *Store) scanLegacy() ([]legacyTask, error) {
	dir, err := openDirectory(s.root, "tasks")
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
	if len(entries) > maxTasks {
		return nil, failure(ErrRecoveryRequired, "too many tasks", "tasks", nil)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	files := make([]legacyTask, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		path := s.mirrorPath(id)
		if !validID(id) {
			return nil, fmt.Errorf("%s: %w", path, failure(ErrPathUnsafe, "task ID", id, nil))
		}
		data, err := readBounded(s.root, "tasks/"+entry.Name(), maxTaskBytes)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		task, err := decodeTaskRecord(data, path)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		files = append(files, legacyTask{path: path, data: data, task: task})
	}
	return files, nil
}

func decodeTaskRecord(data []byte, path string) (Task, error) {
	var record taskRecord
	if err := strictDecode(data, &record); err != nil {
		return Task{}, failure(ErrStaleTask, "task integrity", "", err)
	}
	if record.Schema != taskRecordSchema || crc32.ChecksumIEEE(record.Task) != record.CRC {
		return Task{}, failure(ErrStaleTask, "task integrity", "", nil)
	}
	var task Task
	if err := strictDecode(record.Task, &task); err != nil {
		return Task{}, failure(ErrStaleTask, "task decode", "", err)
	}
	if task.ID != strings.TrimSuffix(filepath.Base(path), ".json") {
		return Task{}, failure(ErrStaleTask, "task identity", task.ID, nil)
	}
	if err := validateTask(task); err != nil {
		return Task{}, failure(ErrStaleTask, "task fields", task.ID, err)
	}
	return normalizedTask(task), nil
}
