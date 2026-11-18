// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"context"
	"crypto/md5"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/game"
)

// The large fixture is written and checked as a stream; the test never allocates
// a game-sized buffer and every file belongs to this test's temporary directory.
func txnScaleStream(t *testing.T, root, name string, size int64) InstallTarget {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(full)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := md5.New()
	writer := io.MultiWriter(f, h)
	chunk := make([]byte, 64<<10)
	for i := range chunk {
		chunk[i] = byte(i % 251)
	}
	for remaining := size; remaining > 0; {
		n := int64(len(chunk))
		if remaining < n {
			n = remaining
		}
		written, err := writer.Write(chunk[:n])
		if err != nil {
			t.Fatal(err)
		}
		if int64(written) != n {
			t.Fatal(io.ErrShortWrite)
		}
		remaining -= n
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	return InstallTarget{Path: name, MD5: fmt.Sprintf("%x", h.Sum(nil)), Size: size}
}
func txnScaleAssertHashes(t *testing.T, root string, targets []InstallTarget) {
	t.Helper()
	for _, target := range targets {
		f, err := os.Open(filepath.Join(root, filepath.FromSlash(target.Path)))
		if err != nil {
			t.Fatalf("open %s: %v", target.Path, err)
		}
		h := md5.New()
		size, err := io.Copy(h, f)
		closeErr := f.Close()
		if err != nil || closeErr != nil {
			t.Fatalf("read %s: %v, close: %v", target.Path, err, closeErr)
		}
		if size != target.Size || fmt.Sprintf("%x", h.Sum(nil)) != target.MD5 {
			t.Fatalf("identity mismatch for %s: size=%d MD5=%x, expected size=%d MD5=%s", target.Path, size, h.Sum(nil), target.Size, target.MD5)
		}
	}
}
func txnScaleAbsent(t *testing.T, root, path string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(path))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected absent %s: %v", path, err)
	}
}

func TestTxnScalePhase4FreshResumesAfterOneHundredPlacements(t *testing.T) {
	ctx := context.Background()
	root := filepath.Join(t.TempDir(), "fresh scale")
	work := filepath.Join(root, ".oddward")
	stateRoot := t.TempDir()
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	key, err := InstallKey(root)
	if err != nil {
		t.Fatal(err)
	}
	candidate := InstallRecord{Schema: 2, Root: root, InstallKey: key, Mode: "fresh", State: "installing", Branch: "PC_150", Version: "scale-1", ListHash: fmt.Sprintf("%x", md5.Sum([]byte("synthetic scale list"))), ManifestDigest: fmt.Sprintf("%x", md5.Sum([]byte("synthetic scale manifest"))), CreatedAt: time.Now().UTC(), VerifyCoverage: "none"}
	guard := func(context.Context) error { return nil }
	free := func(context.Context) (int64, error) { return 1 << 30, nil }
	in := TxnInput{Root: root, WorkRoot: work, TxnID: "scale-fresh-201", Candidate: candidate, Guard: guard, FreeBytes: free}
	// Exactly 200 small target files, including a header-only synthetic PE64 that
	// Inspect recognizes structurally. The bytes are never executed.
	for i := 0; i < 200; i++ {
		path := fmt.Sprintf(txnPrefix+"HT/Content/Paks/small-%03d.bin", i)
		data := []byte(fmt.Sprintf("synthetic small target %03d\n", i))
		if i == 0 {
			path = txnPrefix + "HT/Binaries/Win64/HTGame.exe"
			data = make([]byte, 90)
			copy(data, "MZ")
			binary.LittleEndian.PutUint32(data[60:64], 64)
			copy(data[64:68], "PE\x00\x00")
			binary.LittleEndian.PutUint16(data[68:70], 0x8664)
			binary.LittleEndian.PutUint16(data[84:86], 2)
			binary.LittleEndian.PutUint16(data[86:88], 2)
			binary.LittleEndian.PutUint16(data[88:90], 0x20b)
		}
		source := fmt.Sprintf("cache/scale/small-%03d", i)
		adoptPut(t, work, source, data)
		target := txnTarget(path, string(data))
		in.Candidate.Targets = append(in.Candidate.Targets, target)
		in.Files = append(in.Files, TxnFile{Path: target.Path, Source: source, MD5: target.MD5, Size: target.Size, Origin: "full"})
	}
	largeSource := "cache/scale/large"
	large := txnScaleStream(t, work, largeSource, 64<<20)
	large.Path = txnPrefix + "HT/Content/Paks/large.pak"
	in.Candidate.Targets = append(in.Candidate.Targets, large)
	in.Files = append(in.Files, TxnFile{Path: large.Path, Source: largeSource, MD5: large.MD5, Size: large.Size, Origin: "full"})
	var bootstrap []InstallTarget
	for i, v := range []struct{ path, data string }{{txnPrefix + "PatcherConfig/Patch_Windows.json", `{"appId":"1289","branchName":"PC_150"}`}, {txnPrefix + "PatcherConfig/Patch_Windows.source.json", `{"source":"synthetic scale fixture"}`}} {
		source := fmt.Sprintf("bootstrap/scale-%d", i)
		adoptPut(t, work, source, []byte(v.data))
		target := txnTarget(v.path, v.data)
		bootstrap = append(bootstrap, target)
		in.Bootstrap = append(in.Bootstrap, TxnFile{Path: target.Path, Source: source, MD5: target.MD5, Size: target.Size})
	}
	stopped := errors.New("synthetic stop after placement 100")
	placements := 0
	in.Checkpoint = func(point string) error {
		if point == "after-placement-rename" {
			placements++
			if placements == 100 {
				return stopped
			}
		}
		return nil
	}
	if _, err = store.Commit(ctx, in); !errors.Is(err, stopped) {
		t.Fatalf("missing scale interruption: placements=%d err=%v", placements, err)
	}
	if placements != 100 {
		t.Fatalf("placed %d files, expected 100", placements)
	}
	txnScaleAssertHashes(t, root, in.Candidate.Targets[:100])
	for _, target := range in.Candidate.Targets[100:] {
		txnScaleAbsent(t, root, target.Path)
	}
	// All cache sources were moved into durable staging before placement started.
	// Recovery therefore cannot accidentally pass by downloading/copying them again.
	for _, file := range append(append([]TxnFile{}, in.Files...), in.Bootstrap...) {
		txnScaleAbsent(t, work, file.Source)
	}
	targets := append([]InstallTarget{}, in.Candidate.Targets...)
	txnID := in.TxnID
	in = TxnInput{} // Resume uses only persisted evidence, not the original source plan.
	restarted, err := NewStore(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	status, err := restarted.Recover(ctx, root, work, guard)
	if err != nil || status.State != "incomplete" || !status.LaunchBlocked || !status.Resume || status.Published {
		t.Fatalf("interrupted fresh recovery: %+v %v", status, err)
	}
	status, err = restarted.ResumeCommit(ctx, root, work, guard, free, 0)
	if err != nil || !status.Published || status.LaunchBlocked {
		t.Fatalf("durable scale resume: %+v %v", status, err)
	}
	txnScaleAssertHashes(t, root, targets)
	txnScaleAssertHashes(t, root, bootstrap)
	record, err := restarted.ReadInstall(root)
	if err != nil || record.State != "ready" || record.TxnID != txnID || record.VerifiedTargets != 201 || record.VerifyCoverage != "full" || !txnSameTargets(record.Targets, targets) {
		t.Fatalf("scale publication record: %+v %v", record, err)
	}
	installation, err := game.Inspect(ctx, root)
	if err != nil || installation.Branch != "PC_150" || installation.AppID != "1289" || !strings.EqualFold(filepath.Clean(installation.Executable), filepath.Join(root, filepath.FromSlash(txnPrefix+"HT/Binaries/Win64/HTGame.exe"))) {
		t.Fatalf("synthetic installation not recognized: %+v %v", installation, err)
	}
	if _, err = restarted.Recover(ctx, root, work, guard); err != nil {
		t.Fatal(err)
	}
	txnScaleAssertHashes(t, root, targets)
	t.Log("201 target MD5s + 2 bootstrap MD5s verified after interruption at placement 100 and durable restart; large target streamed at 64 MiB")
}

func TestTxnScalePhase5ATwentyBaselineFiveModifiedThreeAddedTwoPruned(t *testing.T) {
	for _, point := range []string{"complete", "fourth-placement", "before-install-rename", "after-install-rename"} {
		t.Run(point, func(t *testing.T) {
			ctx := context.Background()
			root := filepath.Join(t.TempDir(), "update scale")
			work := filepath.Join(root, ".oddward")
			stateRoot := t.TempDir()
			if err := os.MkdirAll(work, 0700); err != nil {
				t.Fatal(err)
			}
			store, err := NewStore(stateRoot)
			if err != nil {
				t.Fatal(err)
			}
			key, err := InstallKey(root)
			if err != nil {
				t.Fatal(err)
			}
			old := InstallRecord{Schema: 2, Root: root, InstallKey: key, Mode: "oddward", State: "ready", Branch: "PC_150", Version: "scale-old", ListHash: fmt.Sprintf("%x", md5.Sum([]byte("old scale list"))), ManifestDigest: fmt.Sprintf("%x", md5.Sum([]byte("old scale manifest"))), TxnID: "scale-base-transaction", VerifyCoverage: "full", VerifiedTargets: 20, CreatedAt: time.Now().UTC()}
			for i := 0; i < 20; i++ {
				path := fmt.Sprintf(txnPrefix+"HT/Content/Paks/base-%02d.bin", i)
				data := fmt.Sprintf("old synthetic target %02d", i)
				txnPut(t, root, path, data)
				old.Targets = append(old.Targets, txnTarget(path, data))
			}
			txnPut(t, root, "unknown.bin", "untracked synthetic user file")
			if err = store.writeInstall(old); err != nil {
				t.Fatal(err)
			}
			old, err = store.ReadInstall(root)
			if err != nil {
				t.Fatal(err)
			}
			candidate := old
			candidate.Version = "scale-new"
			candidate.ListHash = fmt.Sprintf("%x", md5.Sum([]byte("new scale list")))
			candidate.ManifestDigest = fmt.Sprintf("%x", md5.Sum([]byte("new scale manifest")))
			candidate.Targets = nil
			in := TxnInput{Root: root, WorkRoot: work, TxnID: "scale-update-transaction", Candidate: candidate, Guard: func(context.Context) error { return nil }, FreeBytes: func(context.Context) (int64, error) { return 1 << 30, nil }}
			for i := 0; i < 23; i++ {
				if i == 18 || i == 19 {
					continue
				} // Exactly two previously owned targets are pruned.
				path := fmt.Sprintf(txnPrefix+"HT/Content/Paks/base-%02d.bin", i)
				data := fmt.Sprintf("old synthetic target %02d", i)
				source := ""
				if i < 5 || i >= 20 {
					data = fmt.Sprintf("new synthetic target %02d", i)
					source = fmt.Sprintf("cache/scale/change-%02d", i)
					txnPut(t, work, source, data)
				}
				target := txnTarget(path, data)
				in.Candidate.Targets = append(in.Candidate.Targets, target)
				in.Files = append(in.Files, TxnFile{Path: path, Source: source, MD5: target.MD5, Size: target.Size, Origin: "full"})
			}
			if len(old.Targets) != 20 || len(in.Candidate.Targets) != 21 {
				t.Fatal("incorrect scale fixture cardinality")
			}
			stopped := errors.New("synthetic update scale interruption")
			placements := 0
			hit := false
			if point != "complete" {
				in.Checkpoint = func(checkpoint string) error {
					if checkpoint == "after-placement-rename" {
						placements++
					}
					if point == "fourth-placement" && checkpoint == "after-placement-rename" && placements == 4 || checkpoint == point {
						hit = true
						return stopped
					}
					return nil
				}
			}
			status, err := store.Commit(ctx, in)
			if point == "complete" {
				if err != nil || !status.Published || status.LaunchBlocked {
					t.Fatalf("scale commit: %+v %v", status, err)
				}
			} else if !hit || !errors.Is(err, stopped) {
				t.Fatalf("missing %s interruption: hit=%v err=%v", point, hit, err)
			}
			restarted, err := NewStore(stateRoot)
			if err != nil {
				t.Fatal(err)
			}
			status, err = restarted.Recover(ctx, root, work, in.Guard)
			if err != nil || status.LaunchBlocked {
				t.Fatalf("scale recovery: %+v %v", status, err)
			}
			published := point == "complete" || point == "after-install-rename"
			record, err := restarted.ReadInstall(root)
			if err != nil {
				t.Fatal(err)
			}
			if published {
				txnScaleAssertHashes(t, root, in.Candidate.Targets)
				for _, target := range old.Targets[18:] {
					txnScaleAbsent(t, root, target.Path)
				}
				if record.TxnID != in.TxnID || record.Version != in.Candidate.Version || record.VerifiedTargets != 21 || record.State != "ready" || !txnSameTargets(record.Targets, in.Candidate.Targets) {
					t.Fatalf("candidate publication %+v", record)
				}
			} else {
				txnScaleAssertHashes(t, root, old.Targets)
				for _, target := range in.Candidate.Targets[18:] {
					txnScaleAbsent(t, root, target.Path)
				}
				if record.TxnID != old.TxnID || record.Version != old.Version || record.ListHash != old.ListHash || record.ManifestDigest != old.ManifestDigest || !txnSameTargets(record.Targets, old.Targets) {
					t.Fatalf("rollback lost exact baseline %+v", record)
				}
			}
			if txnRead(t, root, "unknown.bin") != "untracked synthetic user file" {
				t.Fatal("untracked file changed")
			}
			if _, err = restarted.Recover(ctx, root, work, in.Guard); err != nil {
				t.Fatalf("repeated recovery: %v", err)
			}
			if published {
				txnScaleAssertHashes(t, root, in.Candidate.Targets)
			} else {
				txnScaleAssertHashes(t, root, old.Targets)
			}
			t.Logf("20 baseline: modify 5, add 3, prune 2; published=%v; complete target MD5 set checked", published)
		})
	}
}
