// SPDX-License-Identifier: GPL-3.0-or-later
package gamedl

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/nerv-base/Oddward/internal/gameres"
)

const maxTaskBytes = 16 << 20

// Store atomically persists independently checksummed task records. Its root is
// separate from the cache so a discarded cache cannot invent task state.
type Store struct {
	mu   sync.Mutex
	root *os.Root
}

func NewStore(stateRoot string) (*Store, error) {
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
	return &Store{root: root}, nil
}
func (s *Store) Close() error { s.mu.Lock(); defer s.mu.Unlock(); return s.root.Close() }

type taskRecord struct {
	Schema int             `json:"schema"`
	Task   json.RawMessage `json:"task"`
	CRC    uint32          `json:"crc32"`
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
	if err := validateTask(task); err != nil {
		return err
	}
	task.UpdatedAt = time.Now().UTC()
	if task.CreatedAt.IsZero() {
		task.CreatedAt = task.UpdatedAt
	}
	raw, err := json.Marshal(task)
	if err != nil {
		return err
	}
	data, err := json.Marshal(taskRecord{Schema: 2, Task: raw, CRC: crc32.ChecksumIEEE(raw)})
	if err != nil {
		return err
	}
	if len(data) > maxTaskBytes {
		return failure(ErrRecoveryRequired, "task too large", task.ID, nil)
	}
	return atomicWrite(s.root, "tasks/"+task.ID+".json", data)
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
func (s *Store) Load(ctx context.Context, id string) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.load(ctx, id)
}
func (s *Store) load(ctx context.Context, id string) (Task, error) {
	if !validID(id) {
		return Task{}, failure(ErrPathUnsafe, "task ID", id, nil)
	}
	if err := ctx.Err(); err != nil {
		return Task{}, classify("load task", id, err)
	}
	data, err := readBounded(s.root, "tasks/"+id+".json", maxTaskBytes)
	if err != nil {
		return Task{}, err
	}
	var record taskRecord
	if err = strictDecode(data, &record); err != nil || record.Schema != 2 || crc32.ChecksumIEEE(record.Task) != record.CRC {
		return Task{}, failure(ErrStaleTask, "task integrity", id, err)
	}
	var task Task
	if err = strictDecode(record.Task, &task); err != nil {
		return Task{}, failure(ErrStaleTask, "task decode", id, err)
	}
	if task.ID != id {
		return Task{}, failure(ErrStaleTask, "task identity", id, nil)
	}
	if err = validateTask(task); err != nil {
		return Task{}, failure(ErrStaleTask, "task fields", id, err)
	}
	return task, nil
}
func (s *Store) List(ctx context.Context) ([]Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	dir, err := openDirectory(s.root, "tasks")
	if err != nil {
		return nil, err
	}
	entries, err := dir.ReadDir(-1)
	dir.Close()
	if err != nil {
		return nil, err
	}
	if len(entries) > 10000 {
		return nil, failure(ErrRecoveryRequired, "too many tasks", "tasks", nil)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	tasks := make([]Task, 0, len(entries))
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		task, err := s.load(ctx, strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	return tasks, nil
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
