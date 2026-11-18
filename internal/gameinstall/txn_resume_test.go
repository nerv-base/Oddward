// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTxnRecoveryWritesReceipt(t *testing.T) {
	s, in, _ := txnFixture(t)
	in.Checkpoint = func(p string) error {
		if p == "after-first-backup" {
			return errors.New("stop")
		}
		return nil
	}
	_, _ = s.Commit(context.Background(), in)
	if _, e := s.Recover(context.Background(), in.Root, in.WorkRoot, in.Guard); e != nil {
		t.Fatal(e)
	}
	key, _ := InstallKey(in.Root)
	data, e := os.ReadFile(filepath.Join(s.stateRoot, key, "recovery.json"))
	if e != nil {
		t.Fatal(e)
	}
	if len(data) == 0 {
		t.Fatal("empty receipt")
	}
}

func TestTxnRecordedRebuildCleanupAfterPublication(t *testing.T) {
	s, in, _ := txnFixture(t)
	txnPut(t, in.WorkRoot, "rebuild/task/intermediate", "step-data")
	txnPut(t, in.WorkRoot, "rebuild/task/unrelated", "preserve")
	identity := txnTarget("intermediate", "step-data")
	in.RebuildCleanup = []TxnFile{{Source: "rebuild/task/intermediate", MD5: identity.MD5, Size: identity.Size}}
	in.Checkpoint = func(p string) error {
		if p == "before-rebuild-cleanup" {
			return errors.New("stop")
		}
		return nil
	}
	_, _ = s.Commit(context.Background(), in)
	if _, e := os.Stat(filepath.Join(in.WorkRoot, "rebuild", "task", "intermediate")); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Recover(context.Background(), in.Root, in.WorkRoot, in.Guard); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(in.WorkRoot, "rebuild", "task", "intermediate")); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("intermediate survived cleanup: %v", e)
	}
	if txnRead(t, in.WorkRoot, "rebuild/task/unrelated") != "preserve" {
		t.Fatal("unrecorded rebuild file removed")
	}
	txnAssertNew(t, in.Root)
}
func TestTxnPublishedCleanupFailureKeepsLaunchAllowed(t *testing.T) {
	s, in, _ := txnFixture(t)
	in.Checkpoint = func(p string) error {
		if p == "after-committed" {
			return errors.New("stop")
		}
		return nil
	}
	_, _ = s.Commit(context.Background(), in)
	held, e := os.Open(filepath.Join(in.WorkRoot, "txn", in.TxnID, "backup", "0"))
	if e != nil {
		t.Fatal(e)
	}
	defer held.Close()
	st, e := s.Recover(context.Background(), in.Root, in.WorkRoot, in.Guard)
	if e == nil || st.LaunchBlocked || !st.Published || st.Action != "cleanup_pending" {
		t.Fatalf("cleanup failure misclassified %+v %v", st, e)
	}
	st, e = s.TransactionStatus(context.Background(), in.Root, in.WorkRoot)
	if e != nil || st.LaunchBlocked || !st.Published {
		t.Fatalf("published cleanup gates launch %+v %v", st, e)
	}
	held.Close()
	if _, e = s.Recover(context.Background(), in.Root, in.WorkRoot, in.Guard); e != nil {
		t.Fatal(e)
	}
	txnAssertNew(t, in.Root)
}

func TestTxnStagedExplicitResumeRevalidatesTransferredFiles(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "resume", true: "changed"}[changed], func(t *testing.T) {
			s, in, _ := txnFixture(t)
			in.Checkpoint = func(p string) error {
				if p == "after-staged" {
					return errors.New("stop")
				}
				return nil
			}
			if _, e := s.Commit(context.Background(), in); e == nil {
				t.Fatal("missing stop")
			}
			txnAssertOld(t, in.Root)
			st, e := s.Recover(context.Background(), in.Root, in.WorkRoot, in.Guard)
			if e != nil || !st.Resume || !st.LaunchBlocked {
				t.Fatalf("%+v %v", st, e)
			}
			if changed {
				if e := os.WriteFile(filepath.Join(in.WorkRoot, "txn", in.TxnID, "staged", "0"), []byte("bad-a"), 0600); e != nil {
					t.Fatal(e)
				}
			}
			in.Checkpoint = nil
			st, e = s.ResumeCommit(context.Background(), in.Root, in.WorkRoot, in.Guard, in.FreeBytes, in.RequiredFreeBytes)
			if changed {
				if !errors.Is(e, ErrRecoveryRequired) || !st.LaunchBlocked {
					t.Fatalf("stage corruption %+v %v", st, e)
				}
				txnAssertOld(t, in.Root)
			} else {
				if e != nil {
					t.Fatal(e)
				}
				txnAssertNew(t, in.Root)
			}
		})
	}
}
func TestTxnPreparingRecoveryDiscardsOnlyRecordedStage(t *testing.T) {
	s, in, _ := txnFixture(t)
	in.Checkpoint = func(p string) error {
		if p == "after-journal" {
			return errors.New("stop")
		}
		return nil
	}
	_, _ = s.Commit(context.Background(), in)
	st, e := s.Recover(context.Background(), in.Root, in.WorkRoot, in.Guard)
	if e != nil || st.LaunchBlocked || st.Resume {
		t.Fatalf("%+v %v", st, e)
	}
	txnAssertOld(t, in.Root)
	if _, e := os.Stat(filepath.Join(in.WorkRoot, "txn", in.TxnID)); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("preparing stage retained: %v", e)
	}
}
func TestTxnRetainedBackupsExpireAfterValidatedPublication(t *testing.T) {
	s, in, _ := txnFixture(t)
	in.BackupRetention = 24 * time.Hour
	if _, e := s.Commit(context.Background(), in); e != nil {
		t.Fatal(e)
	}
	key, _ := InstallKey(in.Root)
	name := key + "/retained/" + in.TxnID + ".json"
	r, e := os.OpenRoot(s.stateRoot)
	if e != nil {
		t.Fatal(e)
	}
	data, e := stateReadBounded(r, name)
	r.Close()
	if e != nil {
		t.Fatal(e)
	}
	var j txnJournal
	if e := stateDecodeJSON(data, &j); e != nil {
		t.Fatal(e)
	}
	j.RetainUntil = time.Now().Add(-time.Hour)
	if e := s.stateWrite(name, j, nil); e != nil {
		t.Fatal(e)
	}
	held, e := os.Open(filepath.Join(in.WorkRoot, "txn", in.TxnID, "backup", "0"))
	if e != nil {
		t.Fatal(e)
	}
	defer held.Close()
	st, e := s.Recover(context.Background(), in.Root, in.WorkRoot, in.Guard)
	if !errors.Is(e, ErrCleanupPending) || st.LaunchBlocked || !st.Published {
		t.Fatalf("archive removal blocked published install: %+v %v", st, e)
	}
	held.Close()
	if _, e := s.Recover(context.Background(), in.Root, in.WorkRoot, in.Guard); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(in.WorkRoot, "txn", in.TxnID)); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("expired backup retained: %v", e)
	}
	txnAssertNew(t, in.Root)
}
func TestTxnRejectsSharedHardlinks(t *testing.T) {
	for _, where := range []string{"source", "target"} {
		t.Run(where, func(t *testing.T) {
			s, in, _ := txnFixture(t)
			p := filepath.Join(in.WorkRoot, "cache", "blobs", "a")
			if where == "target" {
				p = filepath.Join(in.Root, filepath.FromSlash(txnName("a.bin")))
			}
			if e := os.Link(p, filepath.Join(t.TempDir(), "shared")); e != nil {
				t.Fatal(e)
			}
			if _, e := s.Commit(context.Background(), in); e == nil {
				t.Fatal("shared file accepted")
			}
			txnAssertOld(t, in.Root)
		})
	}
}
func TestTxnRetainCopiesCacheWithoutHardlinks(t *testing.T) {
	s, in, _ := txnFixture(t)
	for i := range in.Files {
		in.Files[i].Retain = true
	}
	if _, e := s.Commit(context.Background(), in); e != nil {
		t.Fatal(e)
	}
	txnPut(t, in.WorkRoot, "cache/blobs/a", "cache-mutated")
	txnAssertNew(t, in.Root)
}
