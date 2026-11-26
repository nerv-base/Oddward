// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestTxnCommitHashesCandidateOnceReusesVerifyAndPersistsFingerprints(t *testing.T) {
	s, in, old := txnFixture(t)
	ctx := context.Background()
	// Mark baseline as Oddward-owned (like verifyOwnedFixture) so VerifyInstallation
	// runs directly without needing an official PE binary.
	old.Mode = "oddward"
	if err := s.writeInstall(ctx, old); err != nil {
		t.Fatal(err)
	}
	// First verify baseline so verify.json has valid fingerprints for all old files.
	vr, err := s.VerifyInstallation(ctx, in.Root, nil)
	if err != nil || vr.Verified != 4 {
		t.Fatalf("baseline verify: %+v %v", vr, err)
	}
	in.Candidate.Mode = "oddward"
	// Replace fixture payloads with multi-KiB data so hash byte counts are clear.
	newA := string(bytes.Repeat([]byte("A"), 64<<10))
	newB := string(bytes.Repeat([]byte("B"), 48<<10))
	newC := string(bytes.Repeat([]byte("C"), 32<<10))
	txnPut(t, in.WorkRoot, "cache/blobs/a", newA)
	txnPut(t, in.WorkRoot, "cache/blobs/b", newB)
	txnPut(t, in.WorkRoot, "cache/blobs/c", newC)
	in.Candidate.Targets[0] = txnTarget("a.bin", newA)
	in.Candidate.Targets[1] = txnTarget("b.bin", newB)
	in.Candidate.Targets[2] = txnTarget("c.bin", newC)
	for i := 0; i < 3; i++ {
		in.Files[i].MD5 = in.Candidate.Targets[i].MD5
		in.Files[i].Size = in.Candidate.Targets[i].Size
	}
	var hashedFiles atomic.Int64
	var hashedBytes atomic.Int64
	prevHook := txnHashObserver
	txnHashObserver = func(_ string, n int64) {
		hashedFiles.Add(1)
		hashedBytes.Add(n)
	}
	defer func() { txnHashObserver = prevHook }()

	status, err := s.Commit(ctx, in)
	if err != nil || !status.Published || status.LaunchBlocked {
		t.Fatalf("commit: %+v %v", status, err)
	}
	wantCandidateBytes := int64(len(newA) + len(newB) + len(newC))
	if hashedFiles.Load() != 3 || hashedBytes.Load() != wantCandidateBytes {
		t.Fatalf("commit hashed %d files / %d bytes; want 3 files / %d bytes (each candidate once, zero reused/old re-hashes)", hashedFiles.Load(), hashedBytes.Load(), wantCandidateBytes)
	}
	// Commit must persist verify.json for the newly published candidate so
	// immediate TrustedTargets and VerifyInstallation reuse 100% of files.
	trusted, err := s.TrustedTargets(ctx, in.Root)
	if err != nil || len(trusted) != len(in.Candidate.Targets) {
		t.Fatalf("TrustedTargets after commit: %d %v", len(trusted), err)
	}
	var verifyHashed int64
	vr2, err := s.VerifyInstallation(ctx, in.Root, func(r VerifyReport) {
		if r.HashedBytes > verifyHashed {
			verifyHashed = r.HashedBytes
		}
	})
	if err != nil || vr2.Reused != len(in.Candidate.Targets) || verifyHashed != 0 {
		t.Fatalf("post-commit verify did not reuse persisted fingerprints: %+v hashed=%d err=%v", vr2, verifyHashed, err)
	}
	if !s.TargetFingerprintMatches(in.Root, in.Candidate.Targets[0]) {
		t.Fatal("TargetFingerprintMatches returned false for freshly committed target")
	}
}

func TestTxnCommitReportsProgressAndDefersOnCommitStartUntilValidated(t *testing.T) {
	t.Run("corrupt-candidate-skips-start-and-keeps-blobs", func(t *testing.T) {
		s, in, _ := txnFixture(t)
		txnPut(t, in.WorkRoot, "cache/blobs/b", "corrupt")
		started := false
		in.OnCommitStart = func() error {
			started = true
			return nil
		}
		if _, err := s.Commit(context.Background(), in); err == nil {
			t.Fatal("accepted corrupt candidate")
		}
		if started {
			t.Fatal("OnCommitStart fired despite corrupt candidate")
		}
		// Earlier valid blob "a" must still sit untouched in cache/blobs/a.
		if got := txnRead(t, in.WorkRoot, "cache/blobs/a"); got != "new-a" {
			t.Fatalf("valid blob moved before all candidates verified: %q", got)
		}
		txnAssertOld(t, in.Root)
	})

	t.Run("valid-reports-monotonic-progress-and-starts-before-journal", func(t *testing.T) {
		s, in, _ := txnFixture(t)
		var order []string
		in.OnCommitStart = func() error {
			order = append(order, "start")
			return nil
		}
		in.Checkpoint = func(point string) error {
			if point == "before-journal" {
				order = append(order, point)
			}
			return nil
		}
		var last TxnProgress
		var count int
		in.Progress = func(p TxnProgress) {
			count++
			if p.BytesDone < last.BytesDone || p.FilesDone < last.FilesDone || p.BytesDone > p.BytesTotal || p.FilesDone > p.FilesTotal {
				t.Fatalf("non-monotonic commit progress: prev=%+v cur=%+v", last, p)
			}
			last = p
		}
		if _, err := s.Commit(context.Background(), in); err != nil {
			t.Fatal(err)
		}
		if count == 0 || last.BytesTotal == 0 || last.BytesDone != last.BytesTotal || last.FilesDone != last.FilesTotal {
			t.Fatalf("incomplete commit progress: count=%d last=%+v", count, last)
		}
		if len(order) != 2 || order[0] != "start" || order[1] != "before-journal" {
			t.Fatalf("hook order = %v, want [start before-journal]", order)
		}
	})
}

func TestTxnCommitCatchesMidTransactionTamperDespiteLedger(t *testing.T) {
	s, in, _ := txnFixture(t)
	in.Checkpoint = func(point string) error {
		if point == "after-staged" {
			// Overwrite staged file 0 with different size/content after staging.
			p := filepath.Join(in.WorkRoot, "txn", in.TxnID, "staged", "0")
			if err := os.WriteFile(p, []byte("tampered-stage-bytes"), 0600); err != nil {
				return err
			}
		}
		return nil
	}
	if _, err := s.Commit(context.Background(), in); err == nil {
		t.Fatal("mid-transaction tamper bypassed ledger")
	}
	txnAssertOld(t, in.Root)
}

func TestTxnExpiredArchiveCleanupAvoidsRedundantHashing(t *testing.T) {
	s, in, _ := txnFixture(t)
	ctx := context.Background()
	in.BackupRetention = 24 * time.Hour
	if _, err := s.Commit(ctx, in); err != nil {
		t.Fatal(err)
	}
	key, _ := InstallKey(in.Root)
	name := key + "/retained/" + in.TxnID + ".json"
	r, err := os.OpenRoot(s.stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	data, err := stateReadBounded(r, name)
	r.Close()
	if err != nil {
		t.Fatal(err)
	}
	var j txnJournal
	if err := stateDecodeJSON(data, &j); err != nil {
		t.Fatal(err)
	}
	j.RetainUntil = time.Now().Add(-time.Hour)
	if err := s.stateWrite(name, j, nil); err != nil {
		t.Fatal(err)
	}
	var hashedFiles atomic.Int64
	prevHook := txnHashObserver
	txnHashObserver = func(_ string, _ int64) { hashedFiles.Add(1) }
	defer func() { txnHashObserver = prevHook }()

	if _, err := s.Recover(ctx, in.Root, in.WorkRoot, in.Guard); err != nil {
		t.Fatal(err)
	}
	// 3 backup/pruned files (a.bin, b.bin, prune.bin) hashed once each; 0 re-hashes
	// for installed targets (covered by persisted verify.json) or duplicate delete loop.
	if hashedFiles.Load() != 3 {
		t.Fatalf("expired archive cleanup hashed %d files, want 3 (each backup once, 0 install re-hashes)", hashedFiles.Load())
	}
	if _, err := os.Stat(filepath.Join(in.WorkRoot, "txn", in.TxnID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expired archive survived: %v", err)
	}
	txnAssertNew(t, in.Root)
}

func TestTxnCommitMultipleCandidatesParallelVerification(t *testing.T) {
	s, in, _ := txnFixture(t)
	ctx := context.Background()
	// Add 12 more candidate files to exercise the parallel 4-worker pool.
	for i := 0; i < 12; i++ {
		name := fmt.Sprintf("extra-%02d.bin", i)
		payload := string(bytes.Repeat([]byte{byte('a' + i)}, 32<<10))
		src := fmt.Sprintf("cache/blobs/extra-%02d", i)
		txnPut(t, in.WorkRoot, src, payload)
		target := txnTarget(name, payload)
		in.Candidate.Targets = append(in.Candidate.Targets, target)
		in.Files = append(in.Files, TxnFile{Path: target.Path, MD5: target.MD5, Size: target.Size, Source: src, Origin: "full"})
	}
	status, err := s.Commit(ctx, in)
	if err != nil || !status.Published || status.LaunchBlocked {
		t.Fatalf("parallel commit: %+v %v", status, err)
	}
	for _, target := range in.Candidate.Targets {
		if !s.TargetFingerprintMatches(in.Root, target) {
			t.Fatalf("missing persisted fingerprint for %s", target.Path)
		}
	}
}
