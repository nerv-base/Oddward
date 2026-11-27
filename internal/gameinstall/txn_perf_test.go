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

func TestTxnCommitAllowMetadataBaselineSkipsUntouchedAndOldHashesAndStaysHonest(t *testing.T) {
	ctx := context.Background()
	root, _ := adoptFixture(t)
	s := adoptStore(t)
	adopted, err := s.AdoptQuick(ctx, root, nil)
	if err != nil || adopted.State != Adopted {
		t.Fatalf("adopt: %+v %v", adopted, err)
	}
	// Update a.pak, keep b.pak untouched without ever running VerifyInstallation.
	work := filepath.Join(root, ".oddward")
	newA := string(bytes.Repeat([]byte("N"), 32<<10))
	txnPut(t, work, "cache/blobs/a", newA)
	candidate := adopted.Record
	candidate.Mode = "oddward"
	candidate.State = "ready"
	candidate.Version = "1.2.4"
	candidate.ListHash = adoptDigest([]byte("1.2.4-list"))
	candidate.ManifestDigest = candidate.ListHash
	candidate.VerifyCoverage = "none"
	candidate.Targets = []InstallTarget{
		txnTarget("Client/WindowsNoEditor/HT/Content/Paks/a.pak", newA),
		adopted.Record.Targets[1], // b.pak untouched
	}
	in := TxnInput{
		Root: root, WorkRoot: work, TxnID: "meta-baseline-update",
		Candidate:             candidate,
		AllowMetadataBaseline: true,
		Guard:                 func(context.Context) error { return nil },
		FreeBytes:             func(context.Context) (int64, error) { return 1 << 30, nil },
		RequiredFreeBytes:     100,
		BackupRetention:       0,
		Files: []TxnFile{
			{Path: candidate.Targets[0].Path, MD5: candidate.Targets[0].MD5, Size: candidate.Targets[0].Size, Source: "cache/blobs/a", Origin: "blob"},
			{Path: candidate.Targets[1].Path, MD5: candidate.Targets[1].MD5, Size: candidate.Targets[1].Size, Source: "", Origin: "installed"},
		},
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
		t.Fatalf("metadata baseline commit failed: %+v %v", status, err)
	}
	// Only the 1 newly staged candidate blob (a.pak) should be hashed; untouched
	// b.pak and replaced old a.pak must not trigger full MD5 reads.
	if hashedFiles.Load() != 1 || hashedBytes.Load() != int64(len(newA)) {
		t.Fatalf("hashed %d files / %d bytes; want 1 file / %d bytes", hashedFiles.Load(), hashedBytes.Load(), len(newA))
	}
	rec, err := s.ReadInstall(root)
	if err != nil || rec.State != "ready" || rec.Version != "1.2.4" || rec.VerifyCoverage != "metadata" || rec.VerifiedTargets != 1 {
		t.Fatalf("honest metadata coverage expected (1 of 2 verified): %+v %v", rec, err)
	}
	if !s.TargetFingerprintMatches(root, candidate.Targets[0]) {
		t.Fatal("newly committed a.pak must have a persisted verify.json fingerprint")
	}
	if s.TargetFingerprintMatches(root, candidate.Targets[1]) {
		t.Fatal("unhashed untouched b.pak must not be fabricated in verify.json")
	}
	// Explicit VerifyInstallation must reuse a.pak (1 reused) and hash only b.pak,
	// then promote VerifyCoverage to "full".
	vr, err := s.VerifyInstallation(ctx, root, nil)
	if err != nil || vr.Reused != 1 || vr.Verified != 2 || vr.Record.VerifyCoverage != "full" {
		t.Fatalf("post-update manual verify = %+v %v", vr, err)
	}
}

func TestTxnCommitAllowMetadataBaselineRollbackOnCrash(t *testing.T) {
	ctx := context.Background()
	root, _ := adoptFixture(t)
	s := adoptStore(t)
	adopted, err := s.AdoptQuick(ctx, root, nil)
	if err != nil || adopted.State != Adopted {
		t.Fatalf("adopt: %+v %v", adopted, err)
	}
	work := filepath.Join(root, ".oddward")
	txnPut(t, work, "cache/blobs/a", "new-first-payload")
	candidate := adopted.Record
	candidate.Mode = "oddward"
	candidate.State = "ready"
	candidate.Version = "1.2.4"
	candidate.ListHash = adoptDigest([]byte("1.2.4-crash-list"))
	candidate.ManifestDigest = candidate.ListHash
	candidate.VerifyCoverage = "none"
	candidate.Targets = []InstallTarget{
		txnTarget("Client/WindowsNoEditor/HT/Content/Paks/a.pak", "new-first-payload"),
		adopted.Record.Targets[1],
	}
	crash := errors.New("synthetic crash")
	in := TxnInput{
		Root: root, WorkRoot: work, TxnID: "meta-baseline-crash",
		Candidate:             candidate,
		AllowMetadataBaseline: true,
		Guard:                 func(context.Context) error { return nil },
		FreeBytes:             func(context.Context) (int64, error) { return 1 << 30, nil },
		Checkpoint: func(point string) error {
			if point == "after-all-placements" {
				return crash
			}
			return nil
		},
		Files: []TxnFile{
			{Path: candidate.Targets[0].Path, MD5: candidate.Targets[0].MD5, Size: candidate.Targets[0].Size, Source: "cache/blobs/a", Origin: "blob"},
			{Path: candidate.Targets[1].Path, MD5: candidate.Targets[1].MD5, Size: candidate.Targets[1].Size, Source: "", Origin: "installed"},
		},
	}
	if _, err := s.Commit(ctx, in); !errors.Is(err, crash) {
		t.Fatalf("expected crash error, got %v", err)
	}
	st, err := s.Recover(ctx, root, work, in.Guard)
	if err != nil || st.LaunchBlocked || st.Action != "rolled_back" {
		t.Fatalf("metadata baseline rollback = %+v %v", st, err)
	}
	if got := txnRead(t, root, "Client/WindowsNoEditor/HT/Content/Paks/a.pak"); got != "correct first" {
		t.Fatalf("rollback did not restore old a.pak: %q", got)
	}
}
