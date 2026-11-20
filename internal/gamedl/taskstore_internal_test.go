// SPDX-License-Identifier: GPL-3.0-or-later
package gamedl

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/database"
	"github.com/nerv-base/Oddward/internal/database/dbtest"
	"github.com/nerv-base/Oddward/internal/gameres"
)

func throttleTask() Task {
	data := make([]byte, 1000)
	for i := range data {
		data[i] = byte(i)
	}
	obj := gameres.Object{MD5: fmt.Sprintf("%x", md5.Sum(data)), Size: int64(len(data))}
	return Task{
		ID: "throttle", InstallKey: "0123456789ABCDEF", Purpose: "preload", Branch: "main", TargetVersion: "1.0.0",
		ListHash: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Kind: TaskPreloadFiles, State: StateRunning,
		Transfers: []Transfer{{Object: obj, Source: gameres.Source{Kind: gameres.SourceCAS, Object: obj}, ChunkSize: 1 << 20, State: TransferPending}},
	}
}

func TestProgressThrottleBoundsWritesAndFlushesOnStateChange(t *testing.T) {
	db := dbtest.Open(t)
	state := t.TempDir()
	store, err := NewStore(db, state)
	if err != nil {
		t.Fatal(err)
	}
	task := throttleTask()
	base := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	store.now = func() time.Time { return base }
	if err = store.Save(context.Background(), task); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	sample := func(bytesDone int64) {
		t.Helper()
		if err = store.UpdateTransferProgress(ctx, task.ID, 0, bytesDone); err != nil {
			t.Fatal(err)
		}
	}
	sample(1)
	for i := int64(2); i <= 10; i++ {
		sample(i)
	}
	// Ten samples inside one window reach the database once; the rest stay in
	// memory until a later window, a state change or a full save.
	if store.flushes != 1 {
		t.Fatalf("throttled samples wrote %d times, want 1", store.flushes)
	}
	if bytesDone := storedBytes(t, db, task.ID); bytesDone != 1 {
		t.Fatalf("stored bytes=%d after window, want 1", bytesDone)
	}
	store.now = func() time.Time { return base.Add(300 * time.Millisecond) }
	sample(20)
	if store.flushes != 2 {
		t.Fatalf("second window wrote %d times, want 2", store.flushes)
	}
	if bytesDone := storedBytes(t, db, task.ID); bytesDone != 20 {
		t.Fatalf("stored bytes=%d after second window, want 20", bytesDone)
	}
	// A transfer state update forces the pending value out immediately, even
	// though it falls inside the window.
	store.now = func() time.Time { return base.Add(350 * time.Millisecond) }
	sample(30)
	if err = store.UpdateTransfer(ctx, task.ID, 0, 31, TransferDownloading); err != nil {
		t.Fatal(err)
	}
	if bytesDone := storedBytes(t, db, task.ID); bytesDone != 31 {
		t.Fatalf("stored bytes=%d after forced flush, want 31", bytesDone)
	}
	requireTaskKind(t, store.UpdateTransferProgress(ctx, task.ID, 0, task.Transfers[0].Object.Size+1), ErrPathUnsafe)
	if err = store.UpdateTransfer(ctx, task.ID, 0, 31, TransferVerified); err != nil {
		t.Fatal(err)
	}
	requireTaskKind(t, store.UpdateTransfer(ctx, task.ID, 0, 31, TransferDownloading), ErrRecoveryRequired)
	requireTaskKind(t, store.UpdateTransfer(ctx, task.ID, 3, 0, TransferPending), ErrStaleTask)
	// A task-level stop flushes progress of every transfer, so a paused task does
	// not lose the bytes its bitmaps already prove.
	store.now = func() time.Time { return base.Add(400 * time.Millisecond) }
	sample(40)
	store.now = func() time.Time { return base.Add(410 * time.Millisecond) }
	sample(50)
	if err = store.UpdateTaskState(ctx, task.ID, StatePaused, "paused"); err != nil {
		t.Fatal(err)
	}
	if bytesDone := storedBytes(t, db, task.ID); bytesDone != 50 {
		t.Fatalf("stored bytes=%d after task state change, want 50", bytesDone)
	}
	if store.flushes > 4 {
		t.Fatalf("progress wrote %d times, want a bounded number", store.flushes)
	}
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	// Reopening the database is the crash case: whatever reached the row is an
	// under-report at worst, never more than the bitmap can prove.
	reopened, err := NewStore(db, state)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	loaded, err := reopened.Load(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Transfers[0].BytesDone != 50 || loaded.State != StatePaused {
		t.Fatalf("reopened progress=%+v task state=%v", loaded.Transfers[0], loaded.State)
	}
	if loaded.Transfers[0].BytesDone > task.Transfers[0].Object.Size {
		t.Fatalf("progress %d exceeds object size", loaded.Transfers[0].BytesDone)
	}
}

func storedBytes(t *testing.T, db *database.DB, taskID string) int64 {
	t.Helper()
	var bytesDone int64
	if err := db.QueryRow(context.Background(), `SELECT bytes_done FROM download_transfers WHERE task_id = ? AND ordinal = 0`, taskID).Scan(&bytesDone); err != nil {
		t.Fatal(err)
	}
	return bytesDone
}

func requireTaskKind(t *testing.T, err error, want ErrorKind) {
	t.Helper()
	var taskErr *TaskError
	if !errors.As(err, &taskErr) || taskErr.Kind != want {
		t.Fatalf("error %v, want kind %v", err, want)
	}
}
