// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"context"
	"crypto/md5"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/database/dbtest"
)

func txnSourceFixture(t *testing.T) (*Store, TxnInput) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "fresh")
	work := filepath.Join(root, ".oddward")
	if e := os.MkdirAll(work, 0700); e != nil {
		t.Fatal(e)
	}
	s := newStore(t, dbtest.Open(t), t.TempDir())
	key, e := InstallKey(root)
	if e != nil {
		t.Fatal(e)
	}
	source := t.TempDir()
	target := txnTarget("source.bin", "verified external source")
	txnPut(t, source, "cache/blob", "verified external source")
	candidate := InstallRecord{Schema: 2, Root: root, InstallKey: key, Mode: "fresh", State: "installing", Branch: "PC_150", Version: "1.0", ListHash: fmt.Sprintf("%x", md5.Sum([]byte("source list"))), ManifestDigest: fmt.Sprintf("%x", md5.Sum([]byte("source manifest"))), CreatedAt: time.Now().UTC(), VerifyCoverage: "none", Targets: []InstallTarget{target}}
	in := TxnInput{Root: root, WorkRoot: work, SourceRoot: source, TxnID: "external-copy", Candidate: candidate, Files: []TxnFile{{Path: target.Path, Source: "cache/blob", MD5: target.MD5, Size: target.Size}}, Guard: func(context.Context) error { return nil }, FreeBytes: func(context.Context) (int64, error) { return 1 << 30, nil }}
	bootstrap := txnTarget(txnPrefix+"PatcherConfig/Patch_Windows.json", `{"appId":"1289","branchName":"PC_150"}`)
	txnPut(t, work, "bootstrap/config", `{"appId":"1289","branchName":"PC_150"}`)
	in.Bootstrap = []TxnFile{{Path: bootstrap.Path, Source: "bootstrap/config", MD5: bootstrap.MD5, Size: bootstrap.Size}}
	return s, in
}
func TestTxnExternalFreshSourcesCopyPreserveAndResume(t *testing.T) {
	for _, point := range []string{"complete", "after-source-copy", "after-journal", "after-first-placement"} {
		t.Run(point, func(t *testing.T) {
			s, in := txnSourceFixture(t)
			stop := errors.New("external synthetic stop")
			hit := false
			if point != "complete" {
				in.Checkpoint = func(p string) error {
					if p == point {
						hit = true
						return stop
					}
					return nil
				}
			}
			_, e := s.Commit(context.Background(), in)
			if point == "complete" {
				if e != nil {
					t.Fatal(e)
				}
			} else {
				if !hit || !errors.Is(e, stop) {
					t.Fatalf("missing external checkpoint %s: hit=%v %v", point, hit, e)
				}
				restarted := newStore(t, s.db, s.stateRoot)
				if point == "after-source-copy" {
					in.Checkpoint = nil
					in.FreeBytes = func(context.Context) (int64, error) { return 1 << 20, nil }
					if _, e = restarted.Commit(context.Background(), in); e != nil {
						t.Fatalf("pre-journal validated copy not reusable: %v", e)
					}
				} else {
					if _, e = restarted.Recover(context.Background(), in.Root, in.WorkRoot, in.Guard); e != nil {
						t.Fatal(e)
					}
					// Durable destination journal must not depend on continued external access.
					unavailable := in.SourceRoot + "-temporarily-unavailable"
					if e = os.Rename(in.SourceRoot, unavailable); e != nil {
						t.Fatal(e)
					}
					defer os.Rename(unavailable, in.SourceRoot)
					if _, e = restarted.ResumeCommit(context.Background(), in.Root, in.WorkRoot, in.Guard, in.FreeBytes, 0); e != nil {
						t.Fatal(e)
					}
					if e = os.Rename(unavailable, in.SourceRoot); e != nil {
						t.Fatal(e)
					}
				}
			}
			txnScaleAssertHashes(t, in.Root, in.Candidate.Targets)
			if txnRead(t, in.SourceRoot, "cache/blob") != "verified external source" {
				t.Fatal("external source removed or modified")
			}
			if txnRead(t, in.Root, txnPrefix+"PatcherConfig/Patch_Windows.json") != `{"appId":"1289","branchName":"PC_150"}` {
				t.Fatal("local bootstrap source confused with external cache")
			}
		})
	}
}
func TestTxnExternalOverlapUsesOpenedVolumePaths(t *testing.T) {
	_, in := txnSourceFixture(t)
	roots, e := txnOpenRoots(in.Root, in.WorkRoot)
	if e != nil {
		t.Fatal(e)
	}
	defer roots.close()
	for _, overlap := range []string{in.Root, in.WorkRoot, filepath.Dir(in.Root)} {
		if e := txnOpenSource(roots, overlap); !errors.Is(e, ErrUnsafeTarget) {
			t.Fatalf("opened-handle overlap accepted: %v", e)
		}
	}
}
func TestTxnExternalInterruptedCopyTempDoesNotBecomeStage(t *testing.T) {
	s, in := txnSourceFixture(t)
	orphan := "cache/tmp/txn-copy-" + in.TxnID + "/copy-interrupted.part"
	txnPut(t, in.WorkRoot, orphan, "unfinished copy")
	if _, e := s.Commit(context.Background(), in); e != nil {
		t.Fatal(e)
	}
	txnScaleAssertHashes(t, in.Root, in.Candidate.Targets)
	if txnRead(t, in.WorkRoot, orphan) != "unfinished copy" {
		t.Fatal("unreferenced partial copy inferred or deleted")
	}
	if txnRead(t, in.SourceRoot, "cache/blob") != "verified external source" {
		t.Fatal("external source changed")
	}
}

func TestTxnExternalFreshBudgetCountsOnlyUnstagedCopies(t *testing.T) {
	s, in := txnSourceFixture(t)
	in.Files = nil
	in.Candidate.Targets = nil
	for i := 0; i < 3; i++ {
		source := fmt.Sprintf("cache/blob-%d", i)
		target := txnScaleStream(t, in.SourceRoot, source, 2<<20)
		target.Path = txnName(fmt.Sprintf("budget-%d.bin", i))
		in.Candidate.Targets = append(in.Candidate.Targets, target)
		in.Files = append(in.Files, TxnFile{Path: target.Path, Source: source, MD5: target.MD5, Size: target.Size})
	}
	stop := errors.New("one external source staged")
	in.RequiredFreeBytes = 1 << 20
	in.Checkpoint = func(point string) error {
		if point == "after-source-copy" {
			return stop
		}
		return nil
	}
	if _, e := s.Commit(context.Background(), in); !errors.Is(e, stop) {
		t.Fatal(e)
	}
	// Restart with exactly enough bytes for the two remaining copies. The final
	// space check then has only the existing 1MiB margin: verified stages are not
	// charged again and rename from stage into the install allocates no file copy.
	in.Checkpoint = nil
	checks := 0
	in.FreeBytes = func(context.Context) (int64, error) {
		checks++
		if checks == 1 {
			return 5 << 20, nil
		}
		return 1 << 20, nil
	}
	restarted := newStore(t, s.db, s.stateRoot)
	if _, e := restarted.Commit(context.Background(), in); e != nil {
		t.Fatalf("already staged bytes charged twice: %v", e)
	}
	if checks != 2 {
		t.Fatalf("space checks=%d, want2", checks)
	}
	txnScaleAssertHashes(t, in.Root, in.Candidate.Targets)
	for _, file := range in.Files {
		target := InstallTarget{Path: file.Source, MD5: file.MD5, Size: file.Size}
		txnScaleAssertHashes(t, in.SourceRoot, []InstallTarget{target})
	}
}
func txnSourceJunction(t *testing.T, link, target string) {
	t.Helper()
	out, err := exec.Command("cmd", "/d", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Fatalf("synthetic junction failed: %v %s", err, out)
	}
}
func TestTxnExternalSourcesRejectReparsePaths(t *testing.T) {
	for _, kind := range []string{"source-root", "source-file"} {
		t.Run(kind, func(t *testing.T) {
			s, in := txnSourceFixture(t)
			if kind == "source-root" {
				link := filepath.Join(t.TempDir(), "linked-cache")
				if e := os.Symlink(in.SourceRoot, link); e != nil {
					txnSourceJunction(t, link, in.SourceRoot)
				}
				in.SourceRoot = link
			} else {
				target := filepath.Join(in.SourceRoot, "cache", "blob")
				link := filepath.Join(in.SourceRoot, "cache", "linked")
				if e := os.Symlink(target, link); e != nil {
					parent := filepath.Join(in.SourceRoot, "linked-parent")
					txnSourceJunction(t, parent, filepath.Join(in.SourceRoot, "cache"))
					in.Files[0].Source = "linked-parent/blob"
				} else {
					in.Files[0].Source = "cache/linked"
				}
			}
			if _, e := s.Commit(context.Background(), in); !errors.Is(e, ErrUnsafeTarget) {
				t.Fatalf("source reparse accepted or misclassified: %v", e)
			}
			txnScaleAbsent(t, in.Root, in.Candidate.Targets[0].Path)
		})
	}
}
func TestTxnExternalFreshCopyOnDistinctRealVolume(t *testing.T) {
	confirmed := os.Getenv("ODDWARD_TXN_SECOND_VOLUME_TEMP")
	if confirmed == "" {
		t.Skip("distinct-volume synthetic test requires an explicit writable temp directory")
	}
	s, in := txnSourceFixture(t)
	if e := txnSourceRootValid(confirmed, in.Root); e != nil {
		t.Fatal(e)
	}
	source, e := os.MkdirTemp(confirmed, "oddward-txn-source-")
	if e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(source)
	in.SourceRoot = source
	roots, e := txnOpenRoots(in.Root, in.WorkRoot)
	if e != nil {
		t.Fatal(e)
	}
	if e = txnOpenSource(roots, in.SourceRoot); e != nil {
		roots.close()
		t.Fatal(e)
	}
	different := roots.sourceDifferentVolume
	roots.close()
	if !different {
		t.Skip("selected temp directories have identical handle-derived volume GUID/serial")
	}
	target := txnScaleStream(t, source, "cache/blob", 64<<20)
	target.Path = in.Files[0].Path
	in.Candidate.Targets = []InstallTarget{target}
	in.Files[0].MD5 = target.MD5
	in.Files[0].Size = target.Size
	if _, e = s.Commit(context.Background(), in); e != nil {
		t.Fatal(e)
	}
	txnScaleAssertHashes(t, in.Root, in.Candidate.Targets)
	target.Path = "cache/blob"
	txnScaleAssertHashes(t, source, []InstallTarget{target})
	t.Log("64MiB source preserved and exact destination MD5 verified across distinct handle-derived volume GUID/serial identities; synthetic temp files only")
}

func TestTxnExternalSourcesRejectUnsafeAndNonfresh(t *testing.T) {
	for _, kind := range []string{"nonfresh", "overlap-root", "overlap-child", "overlap-parent", "source-traversal", "source-checksum", "source-hardlink", "space", "space-reserve", "corrupt-stage", "rebuild"} {
		t.Run(kind, func(t *testing.T) {
			s, in := txnSourceFixture(t)
			switch kind {
			case "nonfresh":
				old := in.Candidate
				old.State = "ready"
				old.Mode = "oddward"
				old.TxnID = "old"
				old.VerifyCoverage = "full"
				old.VerifiedTargets = 1
				txnPut(t, in.Root, "source.bin", "verified external source")
				if e := s.writeInstall(context.Background(), old); e != nil {
					t.Fatal(e)
				}
				in.Candidate = old
				in.Candidate.Version = "2.0"
				in.Bootstrap = nil
			case "overlap-root":
				in.SourceRoot = in.Root
			case "overlap-child":
				in.SourceRoot = in.WorkRoot
			case "overlap-parent":
				in.SourceRoot = filepath.Dir(in.Root)
			case "source-traversal":
				in.Files[0].Source = "../outside"
			case "source-checksum":
				txnPut(t, in.SourceRoot, "cache/blob", "corrupted source")
			case "source-hardlink":
				if e := os.Link(filepath.Join(in.SourceRoot, "cache", "blob"), filepath.Join(t.TempDir(), "hardlink")); e != nil {
					t.Fatal(e)
				}
			case "space", "space-reserve":
				target := txnScaleStream(t, in.SourceRoot, "cache/blob", 2<<20)
				target.Path = in.Files[0].Path
				in.Candidate.Targets = []InstallTarget{target}
				in.Files[0].Size = target.Size
				in.Files[0].MD5 = target.MD5
				in.FreeBytes = func(context.Context) (int64, error) { return 1 << 20, nil }
				if kind == "space-reserve" {
					in.RequiredFreeBytes = 2 << 20
					in.FreeBytes = func(context.Context) (int64, error) { return 3 << 20, nil }
				}
			case "corrupt-stage":
				txnPut(t, in.WorkRoot, "txn/"+in.TxnID+"/staged/0", "ambiguous preexisting stage")
			case "rebuild":
				in.RebuildCleanup = []TxnFile{{Source: "rebuild/task/out", MD5: in.Files[0].MD5, Size: in.Files[0].Size}}
			}
			if _, e := s.Commit(context.Background(), in); e == nil {
				t.Fatalf("unsafe external source accepted: %s", kind)
			}
			if kind != "nonfresh" {
				txnScaleAbsent(t, in.Root, in.Candidate.Targets[0].Path)
			}
		})
	}
}
