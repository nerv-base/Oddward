// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/database/dbtest"
)

const txnPrefix = "Client/WindowsNoEditor/"

func txnName(name string) string {
	if filepath.Base(name) == name {
		return txnPrefix + name
	}
	return name
}
func txnTarget(name, data string) InstallTarget {
	return InstallTarget{Path: txnName(name), MD5: fmt.Sprintf("%x", md5.Sum([]byte(data))), Size: int64(len(data))}
}
func txnPut(t *testing.T, root, name, data string) {
	t.Helper()
	p := filepath.Join(root, filepath.FromSlash(txnName(name)))
	if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(p, []byte(data), 0600); e != nil {
		t.Fatal(e)
	}
}
func txnRead(t *testing.T, root, name string) string {
	t.Helper()
	b, e := os.ReadFile(filepath.Join(root, filepath.FromSlash(txnName(name))))
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}
func txnFixture(t *testing.T) (*Store, TxnInput, InstallRecord) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "game")
	if e := os.MkdirAll(root, 0700); e != nil {
		t.Fatal(e)
	}
	s := newStore(t, dbtest.Open(t), t.TempDir())
	key, e := InstallKey(root)
	if e != nil {
		t.Fatal(e)
	}
	old := InstallRecord{Schema: 2, Root: root, InstallKey: key, Mode: "adopted", State: "ready", Branch: "PC_150", Version: "1.0", ListHash: fmt.Sprintf("%x", md5.Sum([]byte("old list"))), ManifestDigest: fmt.Sprintf("%x", md5.Sum([]byte("old list"))), TxnID: "old-transaction", VerifyCoverage: "full", VerifiedTargets: 4, CreatedAt: time.Now().UTC(), Targets: []InstallTarget{txnTarget("a.bin", "old-a"), txnTarget("b.bin", "old-b"), txnTarget("prune.bin", "old-prune"), txnTarget("keep.bin", "keep")}}
	for _, v := range []struct{ p, d string }{{"a.bin", "old-a"}, {"b.bin", "old-b"}, {"prune.bin", "old-prune"}, {"keep.bin", "keep"}, {"unknown.bin", "user-data"}} {
		txnPut(t, root, v.p, v.d)
	}
	if e = s.writeInstall(context.Background(), old); e != nil {
		t.Fatal(e)
	}
	old, e = s.ReadInstall(root)
	if e != nil {
		t.Fatal(e)
	}
	work := filepath.Join(root, ".oddward")
	for _, v := range []struct{ p, d string }{{"cache/blobs/a", "new-a"}, {"cache/blobs/b", "new-b"}, {"cache/blobs/c", "new-c"}} {
		txnPut(t, work, v.p, v.d)
	}
	candidate := old
	candidate.Version = "1.1"
	candidate.ListHash = fmt.Sprintf("%x", md5.Sum([]byte("new list")))
	candidate.ManifestDigest = candidate.ListHash
	candidate.Targets = []InstallTarget{txnTarget("a.bin", "new-a"), txnTarget("b.bin", "new-b"), txnTarget("c.bin", "new-c"), txnTarget("keep.bin", "keep")}
	in := TxnInput{Root: root, WorkRoot: work, TxnID: "new-transaction", Candidate: candidate, Guard: func(context.Context) error { return nil }, FreeBytes: func(context.Context) (int64, error) { return 1 << 30, nil }, RequiredFreeBytes: 100, BackupRetention: 0}
	for _, v := range candidate.Targets {
		source := "cache/blobs/" + filepath.Base(v.Path)[:1]
		if v.Path == txnName("keep.bin") {
			source = ""
		}
		in.Files = append(in.Files, TxnFile{Path: v.Path, MD5: v.MD5, Size: v.Size, Source: source, Origin: "full"})
	}
	return s, in, old
}
func txnAssertOld(t *testing.T, root string) {
	t.Helper()
	for _, v := range []struct{ p, d string }{{"a.bin", "old-a"}, {"b.bin", "old-b"}, {"prune.bin", "old-prune"}, {"keep.bin", "keep"}, {"unknown.bin", "user-data"}} {
		if g := txnRead(t, root, v.p); g != v.d {
			t.Fatalf("%s=%q want %q", v.p, g, v.d)
		}
	}
	if _, e := os.Stat(filepath.Join(root, filepath.FromSlash(txnName("c.bin")))); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("added file survived rollback: %v", e)
	}
}
func txnAssertNew(t *testing.T, root string) {
	t.Helper()
	for _, v := range []struct{ p, d string }{{"a.bin", "new-a"}, {"b.bin", "new-b"}, {"c.bin", "new-c"}, {"keep.bin", "keep"}, {"unknown.bin", "user-data"}} {
		if g := txnRead(t, root, v.p); g != v.d {
			t.Fatalf("%s=%q want %q", v.p, g, v.d)
		}
	}
	if _, e := os.Stat(filepath.Join(root, filepath.FromSlash(txnName("prune.bin")))); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("pruned file survived: %v", e)
	}
}
func TestTxnUpdatePublishesCompleteCandidateAndPrunes(t *testing.T) {
	s, in, _ := txnFixture(t)
	st, e := s.Commit(context.Background(), in)
	if e != nil {
		t.Fatal(e)
	}
	if st.LaunchBlocked {
		t.Fatal(st)
	}
	txnAssertNew(t, in.Root)
	r, e := s.ReadInstall(in.Root)
	if e != nil {
		t.Fatal(e)
	}
	if r.Version != "1.1" || r.TxnID != in.TxnID || r.BaseVersion != "1.0" || r.State != "ready" || r.VerifyCoverage != "full" {
		t.Fatalf("record %+v", r)
	}
	if !reflect.DeepEqual(r.Targets, in.Candidate.Targets) {
		t.Fatal("record lost candidate identity")
	}
}
func TestCrashPoints(t *testing.T) {
	points := []string{"after-backup-rename", "after-placement-rename", "after-prune-rename", "before-journal", "after-journal", "after-backup-directory", "after-first-backup", "after-all-backups", "after-first-placement", "after-all-placements", "before-install-rename", "after-install-rename", "after-committed", "after-first-backup-delete", "after-first-pruned-delete", "after-prune", "before-rebuild-cleanup"}
	for _, point := range points {
		t.Run(point, func(t *testing.T) {
			s, in, old := txnFixture(t)
			crash := errors.New("synthetic abrupt stop")
			hit := false
			in.Checkpoint = func(p string) error {
				if p == point {
					hit = true
					return crash
				}
				return nil
			}
			_, e := s.Commit(context.Background(), in)
			if !errors.Is(e, crash) || !hit {
				t.Fatalf("checkpoint missing: %s: %v", point, e)
			}
			status, e := s.TransactionStatus(context.Background(), in.Root, in.WorkRoot)
			if e != nil {
				t.Fatal(e)
			}
			if point != "before-journal" && !status.LaunchBlocked && point != "after-committed" && point != "after-first-backup-delete" && point != "after-first-pruned-delete" && point != "before-rebuild-cleanup" {
				t.Fatalf("pending transaction not launch blocked: %+v", status)
			}
			_, e = s.Recover(context.Background(), in.Root, in.WorkRoot, in.Guard)
			if e != nil {
				t.Fatal(e)
			}
			r, e := s.ReadInstall(in.Root)
			if e != nil {
				t.Fatal(e)
			}
			published := point == "after-install-rename" || point == "after-committed" || point == "after-first-backup-delete" || point == "after-first-pruned-delete" || point == "before-rebuild-cleanup"
			if published {
				txnAssertNew(t, in.Root)
				if r.TxnID != in.TxnID {
					t.Fatal("publication reversed")
				}
			} else {
				txnAssertOld(t, in.Root)
				if r.TxnID != old.TxnID || r.Version != old.Version {
					t.Fatal("unpublished transaction changed record")
				}
			}
			if _, e = s.Recover(context.Background(), in.Root, in.WorkRoot, in.Guard); e != nil {
				t.Fatalf("recovery not idempotent: %v", e)
			}
		})
	}
}
func TestTxnRejectsUnsafePathsChecksumsGuardsAndSpace(t *testing.T) {
	for _, kind := range []string{"candidate-hash", "prior-hash", "guard-missing", "guard-busy", "space", "source-traversal", "target-traversal", "case-collision", "parent-collision", "work-outside"} {
		t.Run(kind, func(t *testing.T) {
			s, in, _ := txnFixture(t)
			switch kind {
			case "candidate-hash":
				txnPut(t, in.WorkRoot, "cache/blobs/a", "bad-a")
			case "prior-hash":
				txnPut(t, in.Root, "a.bin", "bad-a")
			case "guard-missing":
				in.Guard = nil
			case "guard-busy":
				in.Guard = func(context.Context) error { return errors.New("official process active") }
			case "space":
				in.FreeBytes = func(context.Context) (int64, error) { return 1, nil }
			case "source-traversal":
				in.Files[0].Source = "../outside"
			case "target-traversal":
				in.Files[0].Path = "../outside"
			case "case-collision":
				in.Files[1].Path = "A.BIN"
			case "parent-collision":
				in.Files[1].Path = "a.bin/sub"
			case "work-outside":
				in.WorkRoot = t.TempDir()
			}
			if _, e := s.Commit(context.Background(), in); e == nil {
				t.Fatal("unsafe transaction accepted")
			}
			if kind != "prior-hash" {
				txnAssertOld(t, in.Root)
			}
		})
	}
}
func TestTxnPrunePreservesChangedAndUntrackedFiles(t *testing.T) {
	s, in, _ := txnFixture(t)
	txnPut(t, in.Root, "prune.bin", "user-edited")
	if _, e := s.Commit(context.Background(), in); e != nil {
		t.Fatal(e)
	}
	if txnRead(t, in.Root, "prune.bin") != "user-edited" {
		t.Fatal("modified prior file removed")
	}
	if txnRead(t, in.Root, "unknown.bin") != "user-data" {
		t.Fatal("untracked removed")
	}
}
func TestTxnSameVersionRequiresExactTransactionEvidence(t *testing.T) {
	for _, foreign := range []bool{false, true} {
		t.Run(fmt.Sprint(foreign), func(t *testing.T) {
			s, in, old := txnFixture(t)
			in.Candidate.Version = old.Version
			in.Checkpoint = func(p string) error {
				if p == "after-install-rename" {
					return errors.New("stop")
				}
				return nil
			}
			if _, e := s.Commit(context.Background(), in); e == nil {
				t.Fatal("did not stop")
			}
			if foreign {
				r, e := s.ReadInstall(in.Root)
				if e != nil {
					t.Fatal(e)
				}
				r.TxnID = "unrelated-transaction"
				if e = s.writeInstall(context.Background(), r); e != nil {
					t.Fatal(e)
				}
			}
			st, e := s.Recover(context.Background(), in.Root, in.WorkRoot, in.Guard)
			if foreign {
				if !errors.Is(e, ErrRecoveryRequired) || !st.LaunchBlocked {
					t.Fatalf("ambiguous repair allowed: %+v %v", st, e)
				}
			} else {
				if e != nil {
					t.Fatal(e)
				}
				txnAssertNew(t, in.Root)
			}
		})
	}
}
func TestTxnRecoveryRetainsEvidenceWhenCandidateOrBackupChanged(t *testing.T) {
	for _, kind := range []string{"published-hash", "missing-backup", "foreign-base", "broken-journal"} {
		t.Run(kind, func(t *testing.T) {
			s, in, old := txnFixture(t)
			point := "after-all-placements"
			if kind == "published-hash" {
				point = "after-install-rename"
			}
			in.Checkpoint = func(p string) error {
				if p == point {
					return errors.New("stop")
				}
				return nil
			}
			_, _ = s.Commit(context.Background(), in)
			switch kind {
			case "published-hash":
				txnPut(t, in.Root, "a.bin", "bad-a")
			case "missing-backup":
				p := filepath.Join(in.WorkRoot, "txn", in.TxnID, "backup", "0")
				if e := os.Remove(p); e != nil {
					t.Fatal(e)
				}
			case "foreign-base":
				old.TxnID = "foreign"
				if e := s.writeInstall(context.Background(), old); e != nil {
					t.Fatal(e)
				}
			case "broken-journal":
				key, _ := InstallKey(in.Root)
				p := filepath.Join(s.stateRoot, key, "txn", in.TxnID+".json")
				if e := os.WriteFile(p, []byte(`{"schema":`), 0600); e != nil {
					t.Fatal(e)
				}
			}
			st, e := s.Recover(context.Background(), in.Root, in.WorkRoot, in.Guard)
			if !errors.Is(e, ErrRecoveryRequired) || !st.LaunchBlocked {
				t.Fatalf("unsafe recovery %+v %v", st, e)
			}
		})
	}
}

func TestTxnBranchSwitchUpdatesAndRollsBackBootstrap(t *testing.T) {
	for _, interrupt := range []bool{false, true} {
		t.Run(fmt.Sprintf("interrupt=%t", interrupt), func(t *testing.T) {
			s, in, old := txnFixture(t)
			oldCfg := `{"appId":"1289","branchName":"PC_150","gameGetServerListUrl":["https://cdn.example.invalid/s.json"]}`
			newCfg := `{"appId":"1289","branchName":"PC_160","gameGetServerListUrl":["https://cdn.example.invalid/s.json"]}`
			txnPut(t, in.Root, txnPrefix+"PatcherConfig/Patch_Windows.json", oldCfg)
			txnPut(t, in.WorkRoot, "bootstrap/config", newCfg)
			cfgTarget := txnTarget(txnPrefix+"PatcherConfig/Patch_Windows.json", newCfg)
			in.Bootstrap = []TxnFile{{Path: cfgTarget.Path, MD5: cfgTarget.MD5, Size: cfgTarget.Size, Source: "bootstrap/config", Origin: "bootstrap"}}

			// Same-branch update must still reject bootstrap files.
			if _, e := s.Commit(context.Background(), in); !errors.Is(e, ErrTargetExists) {
				t.Fatalf("same-branch bootstrap = %v, want ErrTargetExists", e)
			}

			in.Candidate.Branch = "PC_160"
			if interrupt {
				in.Checkpoint = func(p string) error {
					if p == "after-all-placements" {
						return errors.New("stop")
					}
					return nil
				}
			}
			_, e := s.Commit(context.Background(), in)
			if interrupt {
				if e == nil {
					t.Fatal("expected checkpoint stop")
				}
				if _, e := s.Recover(context.Background(), in.Root, in.WorkRoot, in.Guard); e != nil {
					t.Fatal(e)
				}
				if got := txnRead(t, in.Root, txnPrefix+"PatcherConfig/Patch_Windows.json"); got != oldCfg {
					t.Fatalf("rollback did not restore Patch_Windows.json: got %q", got)
				}
				r, err := s.ReadInstall(in.Root)
				if err != nil || r.Branch != old.Branch {
					t.Fatalf("rollback changed branch: %+v %v", r, err)
				}
				return
			}
			if e != nil {
				t.Fatal(e)
			}
			if got := txnRead(t, in.Root, txnPrefix+"PatcherConfig/Patch_Windows.json"); got != newCfg {
				t.Fatalf("branch switch did not update Patch_Windows.json: got %q", got)
			}
			r, err := s.ReadInstall(in.Root)
			if err != nil || r.Branch != "PC_160" {
				t.Fatalf("record branch = %+v %v", r, err)
			}
		})
	}
}
