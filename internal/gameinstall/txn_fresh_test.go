// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTxnFreshOnlyCreatesVerifiedTargetsAndExplicitBootstrap(t *testing.T) {
	for _, point := range []string{"", "before-journal", "after-journal", "after-staged", "after-backup-directory", "after-first-placement", "before-install-rename", "after-install-rename"} {
		interrupt := point != ""
		t.Run(point, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "fresh")
			work := filepath.Join(root, ".oddward")
			if e := os.MkdirAll(work, 0700); e != nil {
				t.Fatal(e)
			}
			s, e := NewStore(t.TempDir())
			if e != nil {
				t.Fatal(e)
			}
			key, _ := InstallKey(root)
			target := txnTarget("data.bin", "new-data")
			candidate := InstallRecord{Schema: 2, Root: root, InstallKey: key, Mode: "fresh", State: "installing", Branch: "PC_150", Version: "1.0", ListHash: fmt.Sprintf("%x", md5.Sum([]byte("list"))), VerifyCoverage: "none", CreatedAt: time.Now().UTC(), Targets: []InstallTarget{target}}
			txnPut(t, work, "cache/data", "new-data")
			txnPut(t, work, "bootstrap/config", "config")
			txnPut(t, work, "bootstrap/source", "audit")
			in := TxnInput{Root: root, WorkRoot: work, TxnID: "fresh-transaction", Candidate: candidate, Files: []TxnFile{{Path: target.Path, MD5: target.MD5, Size: target.Size, Source: "cache/data"}}, Guard: func(context.Context) error { return nil }, FreeBytes: func(context.Context) (int64, error) { return 1 << 20, nil }}
			for _, v := range []struct{ path, source, data string }{{txnPrefix + "PatcherConfig/Patch_Windows.json", "bootstrap/config", "config"}, {txnPrefix + "PatcherConfig/Patch_Windows.source.json", "bootstrap/source", "audit"}} {
				vtarget := txnTarget(v.path, v.data)
				in.Bootstrap = append(in.Bootstrap, TxnFile{Path: vtarget.Path, MD5: vtarget.MD5, Size: vtarget.Size, Source: v.source})
			}
			if interrupt {
				in.Checkpoint = func(p string) error {
					if p == point {
						return errors.New("stop")
					}
					return nil
				}
			}
			_, e = s.Commit(context.Background(), in)
			if interrupt {
				if e == nil {
					t.Fatal("missing stop")
				}
				st, recoveryErr := s.Recover(context.Background(), root, work, in.Guard)
				if recoveryErr != nil {
					t.Fatal(recoveryErr)
				}
				if point == "after-install-rename" {
					if !st.Published || st.LaunchBlocked {
						t.Fatalf("fresh publish recovery: %+v", st)
					}
					e = nil
				} else {
					if !st.LaunchBlocked || !st.Resume || st.State != "incomplete" {
						t.Fatalf("fresh interrupted status: %+v", st)
					}
					in.Checkpoint = nil
					if point == "before-journal" {
						_, e = s.Commit(context.Background(), in)
					} else {
						_, e = s.ResumeCommit(context.Background(), root, work, in.Guard, in.FreeBytes, 0)
					}
				}
			}
			if e != nil {
				t.Fatal(e)
			}
			if txnRead(t, root, "data.bin") != "new-data" {
				t.Fatal("wrong data")
			}
			r, e := s.ReadInstall(root)
			if e != nil || r.State != "ready" || r.TxnID != in.TxnID {
				t.Fatalf("%+v %v", r, e)
			}
		})
	}
}
func TestTxnFreshRejectsNonemptyAndArbitraryBootstrap(t *testing.T) {
	s, in, _ := txnFixture(t)
	in.Candidate.Mode = "fresh"
	in.Bootstrap = []TxnFile{{Path: txnPrefix + "config.xml", Source: "cache/blobs/a", MD5: in.Files[0].MD5, Size: in.Files[0].Size}}
	if _, e := s.Commit(context.Background(), in); e == nil {
		t.Fatal("arbitrary bootstrap accepted")
	}
	txnAssertOld(t, in.Root)
}
func TestTxnRepairCapturesActualOldAndMissingState(t *testing.T) {
	for _, point := range []string{"", "after-all-placements"} {
		t.Run(point, func(t *testing.T) {
			s, in, old := txnFixture(t)
			in.Repair = true
			in.Candidate.Version = old.Version
			txnPut(t, in.Root, "a.bin", "bad-a")
			if e := os.Remove(filepath.Join(in.Root, filepath.FromSlash(txnName("b.bin")))); e != nil {
				t.Fatal(e)
			}
			if point != "" {
				in.Checkpoint = func(p string) error {
					if p == point {
						return errors.New("stop")
					}
					return nil
				}
			}
			_, e := s.Commit(context.Background(), in)
			if point == "" {
				if e != nil {
					t.Fatal(e)
				}
				txnAssertNew(t, in.Root)
			} else {
				if e == nil {
					t.Fatal("no crash")
				}
				if _, e = s.Recover(context.Background(), in.Root, in.WorkRoot, in.Guard); e != nil {
					t.Fatal(e)
				}
				if txnRead(t, in.Root, "a.bin") != "bad-a" {
					t.Fatal("actual corrupt old not restored")
				}
				if _, e = os.Stat(filepath.Join(in.Root, filepath.FromSlash(txnName("b.bin")))); !errors.Is(e, os.ErrNotExist) {
					t.Fatal("missing old not restored")
				}
				r, e := s.ReadInstall(in.Root)
				if e != nil || r.TxnID != old.TxnID {
					t.Fatal("repair rollback lost exact old transaction")
				}
			}
		})
	}
}
func TestTxnRetainedBackupsDoNotBlockNextUpdate(t *testing.T) {
	s, in, _ := txnFixture(t)
	in.BackupRetention = 24 * time.Hour
	if _, e := s.Commit(context.Background(), in); e != nil {
		t.Fatal(e)
	}
	st, e := s.TransactionStatus(context.Background(), in.Root, in.WorkRoot)
	if e != nil || st.LaunchBlocked || !st.Published {
		t.Fatalf("retained transaction blocks launch: %+v %v", st, e)
	}
	next, e := s.ReadInstall(in.Root)
	if e != nil {
		t.Fatal(e)
	}
	in.Candidate = next
	in.Candidate.Version = "1.2"
	in.TxnID = "following-transaction"
	for i := range in.Files {
		in.Files[i].Source = ""
	}
	if _, e = s.Commit(context.Background(), in); e != nil {
		t.Fatalf("retained predecessor blocked update: %v", e)
	}
}
