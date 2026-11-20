// SPDX-License-Identifier: GPL-3.0-or-later
package gamedl_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/database"
	"github.com/nerv-base/Oddward/internal/gamedl"
	"github.com/nerv-base/Oddward/internal/gameres"
)

const fixtureListHash = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

type mirrorRecord struct {
	Schema int             `json:"schema"`
	Task   json.RawMessage `json:"task"`
	CRC    uint32          `json:"crc32"`
}

// writeTaskMirror writes the exact file format legacy builds produce. With
// variant set it spaces the wrapper only, so the file digest changes while the
// embedded task bytes (and their CRC) stay identical.
func writeTaskMirror(t *testing.T, stateDir string, task gamedl.Task, variant bool) []byte {
	t.Helper()
	raw, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	var data []byte
	if variant {
		data = []byte(fmt.Sprintf(`{"schema": 2, "task": %s, "crc32": %d}`, raw, crc32.ChecksumIEEE(raw)))
	} else {
		record := mirrorRecord{Schema: 2, Task: raw, CRC: crc32.ChecksumIEEE(raw)}
		data, err = json.Marshal(record)
		if err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join(stateDir, "tasks")
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, task.ID+".json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	return data
}

func fixtureMark(host, etag string, at time.Time) gamedl.ObjectMark {
	return gamedl.ObjectMark{Host: host, URLKey: strings.Repeat("b", 64), ETag: etag, LastModified: "Wed, 21 Oct 2015 07:28:00 GMT", ObservedAt: at}
}

// importFixtures returns three legacy tasks covering the shapes the importer has
// to carry: a running download with several transfers and marks, a staged
// preload, and an update with patch rebuilds and install targets.
func importFixtures(t *testing.T) []gamedl.Task {
	t.Helper()
	now := time.Date(2026, 3, 4, 5, 6, 7, 0, time.UTC)
	blocked := identity([]byte("abcdefghij"))
	blocks := []gameres.Block{
		{Index: 0, Start: 0, Size: 4, MD5: identity([]byte("abcd")).MD5},
		{Index: 1, Start: 4, Size: 6, MD5: identity([]byte("efghij")).MD5},
	}
	container := identity([]byte(strings.Repeat("z", 100)))
	pakPart := gameres.Object{MD5: identity([]byte("pak part")).MD5, Size: 50}
	running := gamedl.Task{
		ID: "running-task", InstallKey: "ABCDEF0123456789", Purpose: "game-resources",
		CacheRoot: filepath.Join(t.TempDir(), "external-cache"), Branch: "main", TargetVersion: "1.2.3",
		ListHash: fixtureListHash, Kind: gamedl.TaskInstallFiles, State: gamedl.StateRunning,
		Counters:  gamedl.Counters{NetworkBytes: 10 << 20, WrittenBytes: 9 << 20, ReusedBytes: 2 << 20, CachedBytes: 1 << 20},
		CreatedAt: now.Add(-2 * time.Hour), UpdatedAt: now,
		Transfers: []gamedl.Transfer{
			{Object: blocked, Source: gameres.Source{Kind: gameres.SourceCAS, Object: blocked}, ChunkSize: 1 << 20, BytesDone: 4, State: gamedl.TransferDownloading, Blocks: blocks, Marks: []gamedl.ObjectMark{fixtureMark("cdn.example", `"first"`, now.Add(-time.Minute))}},
			{Object: pakPart, Source: gameres.Source{Kind: gameres.SourcePakRange, Object: container, Offset: 25, Size: 50}, ChunkSize: 1 << 20, BytesDone: 50, State: gamedl.TransferVerified, Marks: []gamedl.ObjectMark{fixtureMark("cdn.example", `"second"`, now.Add(-2*time.Minute)), fixtureMark("mirror.example", `"third"`, now.Add(-3*time.Minute))}},
		},
	}
	preloadObj := identity([]byte("preload"))
	preload := gamedl.Task{
		ID: "preload-task", InstallKey: "ABCDEF0123456789", Purpose: "preload", Branch: "main", TargetVersion: "1.3.0",
		ListHash: fixtureListHash, Kind: gamedl.TaskPreloadFiles, State: gamedl.StateStaged,
		Counters:  gamedl.Counters{NetworkBytes: 100, WrittenBytes: 90},
		CreatedAt: now.Add(-30 * time.Minute), UpdatedAt: now.Add(-20 * time.Minute),
		Transfers: []gamedl.Transfer{{Object: preloadObj, Source: gameres.Source{Kind: gameres.SourceCAS, Object: preloadObj}, ChunkSize: 1 << 20, BytesDone: preloadObj.Size, State: gamedl.TransferVerified}},
	}
	oldObj, midObj, targetObj := identity([]byte("old")), identity([]byte("mid")), identity([]byte("target"))
	blob := identity([]byte("blob"))
	update := gamedl.Task{
		ID: "update-task", InstallKey: "ABCDEF0123456789", Purpose: "game-resources", Branch: "main", TargetVersion: "1.4.0",
		ListHash: fixtureListHash, Kind: gamedl.TaskUpdateFiles, State: gamedl.StatePaused,
		Counters:  gamedl.Counters{WrittenBytes: 500, ReusedBytes: 400, CachedBytes: 300},
		CreatedAt: now.Add(-40 * time.Minute), UpdatedAt: now.Add(-10 * time.Minute),
		Transfers: []gamedl.Transfer{{Object: midObj, Source: gameres.Source{Kind: gameres.SourceCAS, Object: midObj}, ChunkSize: 1 << 20, BytesDone: 0, State: gamedl.TransferPending}},
		Rebuilds: []gamedl.Rebuild{{
			Target: targetObj,
			Steps: []gamedl.PatchStep{
				{Blob: blob, Old: oldObj, New: midObj, Source: gameres.Source{Kind: gameres.SourcePatchBlob, Object: blob}},
				{Blob: blob, Old: midObj, New: targetObj, Source: gameres.Source{Kind: gameres.SourcePatchBlob, Object: blob}},
			},
			Output: "Client/WindowsNoEditor/patch.bin", State: gamedl.RebuildProduced,
		}},
		Targets: []gamedl.InstallTarget{
			{Path: "Client/WindowsNoEditor/app.pak", Object: targetObj, Action: gameres.ActionFull, State: gamedl.TargetPlaced},
			{Path: "Client/WindowsNoEditor/app2.pak", Object: oldObj, Action: gameres.ActionPatch, State: gamedl.TargetPending},
		},
	}
	return []gamedl.Task{running, preload, update}
}

func compareTask(t *testing.T, want, got gamedl.Task) {
	t.Helper()
	if want.ID != got.ID || want.InstallKey != got.InstallKey || want.Purpose != got.Purpose ||
		want.CacheRoot != got.CacheRoot || want.Branch != got.Branch || want.TargetVersion != got.TargetVersion ||
		want.ListHash != got.ListHash || want.Kind != got.Kind || want.State != got.State || want.Err != got.Err {
		t.Fatalf("task header: want %+v got %+v", want, got)
	}
	if !want.CreatedAt.Equal(got.CreatedAt) || !want.UpdatedAt.Equal(got.UpdatedAt) {
		t.Fatalf("task timestamps: want %v/%v got %v/%v", want.CreatedAt, want.UpdatedAt, got.CreatedAt, got.UpdatedAt)
	}
	if !reflect.DeepEqual(want.Counters, got.Counters) {
		t.Fatalf("task counters: want %+v got %+v", want.Counters, got.Counters)
	}
	if len(want.Transfers) != len(got.Transfers) {
		t.Fatalf("transfer count: want %d got %d", len(want.Transfers), len(got.Transfers))
	}
	for i := range want.Transfers {
		w, g := want.Transfers[i], got.Transfers[i]
		if !reflect.DeepEqual(w.Object, g.Object) || !reflect.DeepEqual(w.Source, g.Source) ||
			w.ChunkSize != g.ChunkSize || w.BytesDone != g.BytesDone || w.State != g.State ||
			w.PatchBlocksTrusted != g.PatchBlocksTrusted || !reflect.DeepEqual(w.Blocks, g.Blocks) {
			t.Fatalf("transfer %d: want %+v got %+v", i, w, g)
		}
		if len(w.Marks) != len(g.Marks) {
			t.Fatalf("transfer %d marks: want %d got %d", i, len(w.Marks), len(g.Marks))
		}
		for j := range w.Marks {
			wm, gm := w.Marks[j], g.Marks[j]
			if wm.Host != gm.Host || wm.URLKey != gm.URLKey || wm.ETag != gm.ETag || wm.LastModified != gm.LastModified || !wm.ObservedAt.Equal(gm.ObservedAt) {
				t.Fatalf("transfer %d mark %d: want %+v got %+v", i, j, wm, gm)
			}
		}
	}
	if len(want.Rebuilds) != len(got.Rebuilds) {
		t.Fatalf("rebuild count: want %d got %d", len(want.Rebuilds), len(got.Rebuilds))
	}
	for i := range want.Rebuilds {
		w, g := want.Rebuilds[i], got.Rebuilds[i]
		if !reflect.DeepEqual(w.Target, g.Target) || w.Output != g.Output || w.State != g.State || len(w.Steps) != len(g.Steps) {
			t.Fatalf("rebuild %d: want %+v got %+v", i, w, g)
		}
		for j := range w.Steps {
			if !reflect.DeepEqual(w.Steps[j], g.Steps[j]) {
				t.Fatalf("rebuild %d step %d: want %+v got %+v", i, j, w.Steps[j], g.Steps[j])
			}
		}
	}
	if len(want.Targets) != len(got.Targets) {
		t.Fatalf("target count: want %d got %d", len(want.Targets), len(got.Targets))
	}
	for i := range want.Targets {
		if !reflect.DeepEqual(want.Targets[i], got.Targets[i]) {
			t.Fatalf("target %d: want %+v got %+v", i, want.Targets[i], got.Targets[i])
		}
	}
}

func TestImportLegacyReconstructsEveryTaskField(t *testing.T) {
	db := testDB(t)
	state := t.TempDir()
	store, err := gamedl.NewStore(db, state)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tasks := importFixtures(t)
	for _, task := range tasks {
		writeTaskMirror(t, state, task, false)
	}
	source, detail, err := store.ImportLegacy(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(source, "running-task.json") {
		t.Fatalf("mirror source=%q, want the newest task mirror", source)
	}
	if detail != "3 tasks" {
		t.Fatalf("import detail=%q", detail)
	}
	for _, want := range tasks {
		got, err := store.Load(context.Background(), want.ID)
		if err != nil {
			t.Fatalf("load %s: %v", want.ID, err)
		}
		compareTask(t, want, got)
	}
}

func dbSnapshot(t *testing.T, db *database.DB) string {
	t.Helper()
	queries := []string{
		`SELECT id, schema, install_key, purpose, cache_root, branch, target_version, list_hash, kind, state, err, network_bytes, written_bytes, reused_bytes, cached_bytes, created_at, updated_at FROM download_tasks ORDER BY id`,
		`SELECT task_id, ordinal, object_md5, object_size, object_key, source_kind, source_md5, source_size, source_offset, source_length, chunk_size, bytes_done, state, patch_blocks_trusted, blocks, marks, updated_at FROM download_transfers ORDER BY task_id, ordinal`,
		`SELECT task_id, ordinal, target_md5, target_size, output, state, steps FROM download_rebuilds ORDER BY task_id, ordinal`,
		`SELECT task_id, path, object_md5, object_size, action, state FROM download_targets ORDER BY task_id, path`,
	}
	var b strings.Builder
	ctx := context.Background()
	for _, q := range queries {
		rows, err := db.Query(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		columns, err := rows.Columns()
		if err != nil {
			rows.Close()
			t.Fatal(err)
		}
		for rows.Next() {
			values := make([]any, len(columns))
			pointers := make([]any, len(columns))
			for i := range values {
				pointers[i] = &values[i]
			}
			if err = rows.Scan(pointers...); err != nil {
				rows.Close()
				t.Fatal(err)
			}
			fmt.Fprintf(&b, "%v\n", values)
		}
		if err = rows.Err(); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		rows.Close()
	}
	return b.String()
}

func TestImportLegacyIsIdempotent(t *testing.T) {
	db := testDB(t)
	state := t.TempDir()
	store, err := gamedl.NewStore(db, state)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tasks := importFixtures(t)
	for _, task := range tasks {
		writeTaskMirror(t, state, task, false)
	}
	if _, _, err = store.ImportLegacy(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := dbSnapshot(t, db)
	// Rewriting the same task with different whitespace changes the mirror digest
	// without changing its content, which is how a stale mirror forces a re-import.
	for _, task := range tasks {
		writeTaskMirror(t, state, task, true)
	}
	if _, detail, err := store.ImportLegacy(context.Background()); err != nil {
		t.Fatal(err)
	} else if detail != "3 tasks" {
		t.Fatalf("second import detail=%q, want a re-import", detail)
	}
	if second := dbSnapshot(t, db); first != second {
		t.Fatalf("database changed across identical imports:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}

func TestImportLegacyFoldsInAdditionalInstallStateRoot(t *testing.T) {
	db := testDB(t)
	fixtures := importFixtures(t)
	first := fixtures[0]
	first.ID = "install-one-task"
	first.InstallKey = "1111111111111111"
	second := fixtures[1]
	second.ID = "install-two-task"
	second.InstallKey = "2222222222222222"
	rootOne, rootTwo := t.TempDir(), t.TempDir()
	writeTaskMirror(t, rootOne, first, false)
	writeTaskMirror(t, rootTwo, second, false)
	one, err := gamedl.NewStore(db, rootOne)
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	if _, detail, err := one.ImportLegacy(context.Background()); err != nil || detail != "1 tasks" {
		t.Fatalf("first install import: detail=%q err=%v", detail, err)
	}
	two, err := gamedl.NewStore(db, rootTwo)
	if err != nil {
		t.Fatal(err)
	}
	defer two.Close()
	// The migration record is already complete for the shared database, yet the
	// second installation's documents were never imported and must still land.
	if _, detail, err := two.ImportLegacy(context.Background()); err != nil || detail != "1 tasks" {
		t.Fatalf("second install import: detail=%q err=%v", detail, err)
	}
	tasks, err := one.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("task count=%d, want both installations", len(tasks))
	}
	if _, err := one.Load(context.Background(), second.ID); err != nil {
		t.Fatalf("second installation task missing: %v", err)
	}
	// A state root whose documents are all present stays a no-op.
	if _, detail, err := one.ImportLegacy(context.Background()); err != nil || detail != "database already current" {
		t.Fatalf("repeat import: detail=%q err=%v", detail, err)
	}
}

// The launcher has one database but one task directory per installation, so the
// startup entry point has to walk them all and tolerate a state root that does
// not exist yet.
func TestImportLegacyScansEveryInstallationRoot(t *testing.T) {
	db := testDB(t)
	installs := t.TempDir()
	fixtures := importFixtures(t)
	first := fixtures[0]
	first.ID = "install-a-task"
	first.InstallKey = "1111111111111111"
	second := fixtures[1]
	second.ID = "install-b-task"
	second.InstallKey = "2222222222222222"
	writeTaskMirror(t, filepath.Join(installs, "AAAA"), first, false)
	writeTaskMirror(t, filepath.Join(installs, "BBBB"), second, false)
	if err := gamedl.ImportLegacy(context.Background(), db, installs); err != nil {
		t.Fatal(err)
	}
	store, err := gamedl.NewStore(db, filepath.Join(installs, "AAAA"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	tasks, err := store.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 {
		t.Fatalf("task count=%d, want both installations", len(tasks))
	}
	if err := gamedl.ImportLegacy(context.Background(), db, installs); err != nil {
		t.Fatalf("repeat startup import: %v", err)
	}
	if err := gamedl.ImportLegacy(context.Background(), db, filepath.Join(t.TempDir(), "missing")); err != nil {
		t.Fatalf("missing installs root: %v", err)
	}
}

func TestImportLegacyReconcilesMirrorRewrittenByOlderBuild(t *testing.T) {
	db := testDB(t)
	state := t.TempDir()
	store, err := gamedl.NewStore(db, state)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task := importFixtures(t)[0]
	writeTaskMirror(t, state, task, false)
	if _, _, err = store.ImportLegacy(context.Background()); err != nil {
		t.Fatal(err)
	}
	mirrors, err := db.Mirrors(context.Background(), gamedl.Domain)
	if err != nil || len(mirrors) != 1 {
		t.Fatalf("mirror records: %+v err=%v", mirrors, err)
	}
	if mirrors[0].Path != filepath.Join(state, "tasks", task.ID+".json") {
		t.Fatalf("mirror path=%q", mirrors[0].Path)
	}
	if stale, err := db.MirrorStale(context.Background(), gamedl.Domain); err != nil || stale {
		t.Fatalf("fresh mirror reported stale=%v err=%v", stale, err)
	}
	changed := task
	changed.Purpose = "older-build"
	changed.UpdatedAt = task.UpdatedAt.Add(time.Hour)
	writeTaskMirror(t, state, changed, false)
	if stale, err := db.MirrorStale(context.Background(), gamedl.Domain); err != nil || !stale {
		t.Fatalf("rewritten mirror reported stale=%v err=%v", stale, err)
	}
	if _, detail, err := store.ImportLegacy(context.Background()); err != nil {
		t.Fatal(err)
	} else if detail != "1 tasks" {
		t.Fatalf("reconcile detail=%q", detail)
	}
	got, err := store.Load(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	compareTask(t, changed, got)
}

func TestImportLegacyNamesCorruptFileAndKeepsState(t *testing.T) {
	db := testDB(t)
	state := t.TempDir()
	store, err := gamedl.NewStore(db, state)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	task := importFixtures(t)[0]
	writeTaskMirror(t, state, task, false)
	if _, _, err = store.ImportLegacy(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := dbSnapshot(t, db)
	broken := filepath.Join(state, "tasks", "broken.json")
	if err = os.WriteFile(broken, []byte("truncated"), 0600); err != nil {
		t.Fatal(err)
	}
	// Change the mirror digest without changing the valid task so the importer
	// actually scans and reaches the corrupt file.
	writeTaskMirror(t, state, task, true)
	_, _, err = store.ImportLegacy(context.Background())
	if err == nil {
		t.Fatal("corrupt mirror did not fail the import")
	}
	if !strings.Contains(err.Error(), broken) {
		t.Fatalf("import error %q does not name %s", err, broken)
	}
	data, err := os.ReadFile(broken)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "truncated" {
		t.Fatalf("corrupt file rewritten to %q", data)
	}
	if after := dbSnapshot(t, db); after != before {
		t.Fatalf("failed import changed rows:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	if _, err = store.Load(context.Background(), task.ID); err != nil {
		t.Fatalf("previous task lost: %v", err)
	}
}

func TestSaveLoadRoundTripPreservesZeroSizeAndNilSlices(t *testing.T) {
	db := testDB(t)
	state := t.TempDir()
	store, err := gamedl.NewStore(db, state)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	now := time.Date(2026, 6, 7, 8, 9, 10, 0, time.UTC)
	empty := identity(nil)
	zero := gamedl.Task{
		ID: "zero-size", InstallKey: "0123456789ABCDEF", Purpose: "preload", Branch: "main", TargetVersion: "1.0.0",
		ListHash: fixtureListHash, Kind: gamedl.TaskPreloadFiles, State: gamedl.StateStaged,
		CreatedAt: now, UpdatedAt: now,
		Transfers: []gamedl.Transfer{{Object: empty, Source: gameres.Source{Kind: gameres.SourceCAS, Object: empty}, State: gamedl.TransferPending}},
	}
	if err = store.Save(context.Background(), zero); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(context.Background(), zero.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := zero
	// Save stamps its own UpdatedAt; everything else must survive unchanged.
	want.UpdatedAt = got.UpdatedAt
	compareTask(t, want, got)
	if got.UpdatedAt.Before(now) {
		t.Fatalf("updated_at %v precedes the save", got.UpdatedAt)
	}
	var size int64
	var key string
	if err = db.QueryRow(context.Background(), `SELECT object_size, object_key FROM download_transfers WHERE task_id = ?`, zero.ID).Scan(&size, &key); err != nil {
		t.Fatal(err)
	}
	if size != 1 || key != empty.String() {
		t.Fatalf("stored zero-size object as size=%d key=%q", size, key)
	}
	emptyTask := gamedl.Task{
		ID: "no-transfers", InstallKey: "0123456789ABCDEF", Purpose: "preload", Branch: "main", TargetVersion: "1.0.0",
		ListHash: fixtureListHash, Kind: gamedl.TaskPreloadFiles, State: gamedl.StateDone,
		CreatedAt: now, UpdatedAt: now, Transfers: []gamedl.Transfer{},
	}
	if err = store.Save(context.Background(), emptyTask); err != nil {
		t.Fatal(err)
	}
	got, err = store.Load(context.Background(), emptyTask.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Transfers != nil {
		t.Fatalf("empty transfer slice round-tripped as %#v", got.Transfers)
	}
	if got.Rebuilds != nil || got.Targets != nil {
		t.Fatalf("empty rebuilds/targets round-tripped as %#v/%#v", got.Rebuilds, got.Targets)
	}
}

func TestTargetedUpdatesRejectInvalidProgressAndTransitions(t *testing.T) {
	db := testDB(t)
	store, err := gamedl.NewStore(db, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	obj := identity([]byte("12345678"))
	task := gamedl.Task{
		ID: "targeted", InstallKey: "0123456789ABCDEF", Purpose: "preload", Branch: "main", TargetVersion: "1.0.0",
		ListHash: fixtureListHash, Kind: gamedl.TaskPreloadFiles, State: gamedl.StateRunning,
		Transfers: []gamedl.Transfer{{Object: obj, Source: gameres.Source{Kind: gameres.SourceCAS, Object: obj}, State: gamedl.TransferPending}},
	}
	if err = store.Save(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	requireKind(t, store.UpdateTransferProgress(ctx, task.ID, 0, obj.Size+1), gamedl.ErrPathUnsafe)
	requireKind(t, store.UpdateTransferProgress(ctx, task.ID, 0, -1), gamedl.ErrPathUnsafe)
	requireKind(t, store.UpdateTransferProgress(ctx, task.ID, 4, 1), gamedl.ErrStaleTask)
	requireKind(t, store.UpdateTransfer(ctx, task.ID, 0, 0, gamedl.TransferVerified), gamedl.ErrRecoveryRequired)
	if err = store.UpdateTransfer(ctx, task.ID, 0, obj.Size, gamedl.TransferDownloading); err != nil {
		t.Fatal(err)
	}
	if err = store.UpdateTransfer(ctx, task.ID, 0, obj.Size, gamedl.TransferVerified); err != nil {
		t.Fatal(err)
	}
	requireKind(t, store.UpdateTransfer(ctx, task.ID, 0, obj.Size, gamedl.TransferDownloading), gamedl.ErrRecoveryRequired)
	requireKind(t, store.UpdateTransferProgress(ctx, task.ID, 0, obj.Size+1), gamedl.ErrPathUnsafe)
	got, err := store.Load(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Transfers[0].BytesDone != obj.Size || got.Transfers[0].State != gamedl.TransferVerified {
		t.Fatalf("targeted update lost: %+v", got.Transfers[0])
	}
}

func TestThrottledProgressUnderReportsAndRecoverUsesBitmap(t *testing.T) {
	data := bytes.Repeat([]byte("0123456789abcdef"), 3*(1<<16))
	obj := identity(data)
	var mu sync.Mutex
	requests := map[string]int{}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests[r.Header.Get("Range")]++
		mu.Unlock()
		http.ServeContent(w, r, "object", zeroTime, bytes.NewReader(data))
	}))
	defer server.Close()
	work, state := t.TempDir(), t.TempDir()
	db := testDB(t)
	opts := gamedl.Options{DB: db, WorkRoot: work, StateRoot: state, Client: server.Client(), Resolve: func(context.Context, gameres.Source) ([]string, error) { return []string{server.URL}, nil }, Parameters: gamedl.Parameters{ChunkSize: 1 << 20, ConnectionsPerFile: 1, FilesInFlight: 1}}
	task := gamedl.Task{ID: "crash", Kind: gamedl.TaskPreloadFiles, ListHash: fixtureListHash, Transfers: []gamedl.Transfer{{Object: obj, Source: gameres.Source{Kind: gameres.SourceCAS, Object: obj}}}}
	e, err := gamedl.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err = e.Run(ctx, task, func(p gamedl.Progress) {
		if p.Downloaded >= 1<<20 {
			cancel()
		}
	}); err == nil {
		t.Fatal("cancel did not stop the download")
	}
	cancel()
	if err = e.Close(); err != nil {
		t.Fatal(err)
	}
	// Simulate a crash that lost the last throttled sample: the row claims zero
	// bytes while the bitmap on disk already proves the first chunk.
	if _, err = db.Exec(context.Background(), `UPDATE download_transfers SET bytes_done = 0 WHERE task_id = ?`, task.ID); err != nil {
		t.Fatal(err)
	}
	stored, err := gamedl.NewStore(db, state)
	if err != nil {
		t.Fatal(err)
	}
	under, err := stored.Load(context.Background(), task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if under.Transfers[0].BytesDone != 0 {
		t.Fatalf("simulated crash progress=%d", under.Transfers[0].BytesDone)
	}
	stored.Close()
	reopened, err := gamedl.New(opts)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	recovered, err := reopened.Recover(context.Background(), fixtureListHash)
	if err != nil {
		t.Fatal(err)
	}
	if len(recovered) != 1 || recovered[0].Transfers[0].BytesDone != 1<<20 {
		t.Fatalf("recovered=%+v, want the bitmap's first chunk", recovered)
	}
	if err = reopened.Run(context.Background(), recovered[0], nil); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	first := requests["bytes=0-1048575"]
	mu.Unlock()
	if first != 1 {
		t.Fatalf("first chunk requested %d times after recovery, want 1", first)
	}
}
