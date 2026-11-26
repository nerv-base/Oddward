// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/database/dbtest"
)

func TestVerifyPromotesOnlyWholeMD5AndResumesAcrossStore(t *testing.T) {
	root, _ := adoptFixture(t)
	stateRoot := t.TempDir()
	db := dbtest.Open(t)
	s := newStore(t, db, stateRoot)
	if _, err := s.AdoptQuick(context.Background(), root, nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	reports := 0
	_, err := s.VerifyInstallation(ctx, root, func(r VerifyReport) {
		reports++
		if r.FilesDone == 1 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) || reports != 1 {
		t.Fatalf("file-granular pause: reports=%d err=%v", reports, err)
	}
	record, err := s.ReadInstall(root)
	if err != nil || record.State == Verified {
		t.Fatalf("canceled verify promoted: %+v %v", record, err)
	}
	s = newStore(t, db, stateRoot)
	got, err := s.VerifyInstallation(context.Background(), root, nil)
	if err != nil || got.Record.State != Verified || got.Verified != 2 || got.Reused != 1 {
		t.Fatalf("resumed verify: %+v %v", got, err)
	}
	trusted, err := s.TrustedTargets(context.Background(), root)
	if err != nil || len(trusted) != 2 {
		t.Fatalf("verified targets: %+v %v", trusted, err)
	}
}
func TestVerifyDetectsSameSizeCorruptionAndInvalidatesTrust(t *testing.T) {
	root, _ := adoptFixture(t)
	s := adoptStore(t)
	if _, err := s.AdoptQuick(context.Background(), root, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyInstallation(context.Background(), root, nil); err != nil {
		t.Fatal(err)
	}
	adoptPut(t, root, "Client/WindowsNoEditor/HT/Content/Paks/a.pak", []byte("corrupt first"))
	trusted, err := s.TrustedTargets(context.Background(), root)
	if err != nil || len(trusted) != 1 {
		t.Fatalf("changed file retained trust: %+v %v", trusted, err)
	}
	got, err := s.VerifyInstallation(context.Background(), root, nil)
	if err != nil || got.Record.State == Verified || len(got.Mismatches) != 1 || got.Verified != 1 {
		t.Fatalf("whole MD5 corruption: %+v %v", got, err)
	}
}
func TestVerifyReplacementWithSameSizeMtimeRequiresNewMD5(t *testing.T) {
	root, _ := adoptFixture(t)
	s := adoptStore(t)
	if _, err := s.AdoptQuick(context.Background(), root, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyInstallation(context.Background(), root, nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "Client/WindowsNoEditor/HT/Content/Paks/a.pak")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	// Rename the known synthetic path; replacement must have a distinct file ID.
	if err := os.Rename(path, path+".old"); err != nil {
		t.Fatal(err)
	}
	adoptPut(t, root, "Client/WindowsNoEditor/HT/Content/Paks/a.pak", []byte("corrupt first"))
	if err := os.Chtimes(path, time.Now(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	got, err := s.VerifyInstallation(context.Background(), root, nil)
	if err != nil || got.Reused != 1 || len(got.Mismatches) != 1 {
		t.Fatalf("file ID substitution: %+v %v", got, err)
	}
}
func TestVerifyRejectsChangedOfficialGeneration(t *testing.T) {
	root, manifest := adoptFixture(t)
	s := adoptStore(t)
	if _, err := s.AdoptQuick(context.Background(), root, nil); err != nil {
		t.Fatal(err)
	}
	changed := append([]byte("\n"), manifest...)
	adoptPut(t, root, "NTELauncher/UserData/Patcher/PatcherSDK/ResList.xml", changed)
	if _, err := s.VerifyInstallation(context.Background(), root, nil); !errors.Is(err, ErrStaleState) {
		t.Fatalf("official baseline changed: %v", err)
	}
}
func TestVerifyReportsHashedBytesExcludingReused(t *testing.T) {
	root, _ := adoptFixture(t)
	s := adoptStore(t)
	if _, err := s.AdoptQuick(context.Background(), root, nil); err != nil {
		t.Fatal(err)
	}
	var firstHashed, firstTotal int64
	if _, err := s.VerifyInstallation(context.Background(), root, func(r VerifyReport) {
		firstHashed, firstTotal = r.HashedBytes, r.BytesTotal
	}); err != nil {
		t.Fatal(err)
	}
	if firstTotal <= 0 || firstHashed != firstTotal {
		t.Fatalf("initial verify hashed=%d total=%d, want equal positive bytes", firstHashed, firstTotal)
	}
	var reusedHashed int64
	if _, err := s.VerifyInstallation(context.Background(), root, func(r VerifyReport) {
		if r.HashedBytes > reusedHashed {
			reusedHashed = r.HashedBytes
		}
	}); err != nil {
		t.Fatal(err)
	}
	if reusedHashed != 0 {
		t.Fatalf("reused verify counted %d hashed bytes, want 0", reusedHashed)
	}
}

func TestVerifyParallelMultiFilePreservesMismatchesOrderAndMonotonicProgress(t *testing.T) {
	root := filepath.Join(t.TempDir(), "parallel verify")
	s := adoptStore(t)
	key, err := InstallKey(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	record := InstallRecord{
		Schema: 2, InstallKey: key, Root: root, Mode: "oddward", State: "ready",
		Branch: "PC_150", Version: "1.5.8",
		ListHash: adoptDigest([]byte("parallel list")), ManifestDigest: adoptDigest([]byte("parallel manifest")),
		TxnID: "parallel-txn", VerifyCoverage: "full", VerifiedTargets: 12,
		CreatedAt: now, PublishedAt: now,
	}
	for i := 0; i < 12; i++ {
		path := filepath.ToSlash(filepath.Join("Client/WindowsNoEditor/HT/Content/Paks", "chunk-"+string(rune('a'+i))+".pak"))
		data := make([]byte, (i+1)*(16<<10))
		for j := range data {
			data[j] = byte((i*17 + j) & 0xff)
		}
		adoptPut(t, root, path, data)
		record.Targets = append(record.Targets, InstallTarget{Path: path, MD5: adoptDigest(data), Size: int64(len(data))})
	}
	if err := s.writeInstall(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	var last VerifyReport
	got, err := s.VerifyInstallation(context.Background(), root, func(r VerifyReport) {
		if r.FilesDone < last.FilesDone || r.BytesDone < last.BytesDone || r.HashedBytes < last.HashedBytes || r.BytesDone > r.BytesTotal {
			t.Fatalf("non-monotonic verify progress: prev=%+v cur=%+v", last, r)
		}
		last = r
	})
	if err != nil || got.Verified != 12 || got.Reused != 0 || len(got.Mismatches) != 0 {
		t.Fatalf("initial parallel verify: %+v %v", got, err)
	}
	if last.FilesDone != 12 || last.BytesDone != last.BytesTotal || last.HashedBytes != last.BytesTotal {
		t.Fatalf("final parallel report = %+v", last)
	}
	// Corrupt file 2 (same size) and delete file 7; verify reports exact mismatches
	// in manifest order while reusing all 10 untouched files.
	corrupt := make([]byte, record.Targets[2].Size)
	for i := range corrupt {
		corrupt[i] = 0x55
	}
	adoptPut(t, root, record.Targets[2].Path, corrupt)
	if err := os.Remove(filepath.Join(root, filepath.FromSlash(record.Targets[7].Path))); err != nil {
		t.Fatal(err)
	}
	got2, err := s.VerifyInstallation(context.Background(), root, nil)
	if err != nil || got2.Reused != 10 || got2.Verified != 10 || len(got2.Mismatches) != 2 {
		t.Fatalf("partial mismatch parallel verify: %+v %v", got2, err)
	}
	if got2.Mismatches[0].Path != record.Targets[2].Path || got2.Mismatches[0].Reason != "md5" ||
		got2.Mismatches[1].Path != record.Targets[7].Path || got2.Mismatches[1].Reason != "missing" {
		t.Fatalf("mismatches = %+v", got2.Mismatches)
	}
}

func TestTrustedTargetsParallelFallbackPreservesOrderWithoutWritingState(t *testing.T) {
	root := filepath.Join(t.TempDir(), "trusted parallel")
	s := adoptStore(t)
	key, err := InstallKey(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	record := InstallRecord{
		Schema: 2, InstallKey: key, Root: root, Mode: "oddward", State: "ready",
		Branch: "PC_150", Version: "1.5.8",
		ListHash: adoptDigest([]byte("trusted list")), ManifestDigest: adoptDigest([]byte("trusted manifest")),
		TxnID: "trusted-txn", VerifyCoverage: "full", VerifiedTargets: 10,
		CreatedAt: now, PublishedAt: now,
	}
	for i := 0; i < 10; i++ {
		path := filepath.ToSlash(filepath.Join("Client/WindowsNoEditor/HT/Content/Paks", "t-"+string(rune('a'+i))+".pak"))
		data := make([]byte, 32<<10)
		for j := range data {
			data[j] = byte((i*13 + j) & 0xff)
		}
		adoptPut(t, root, path, data)
		record.Targets = append(record.Targets, InstallTarget{Path: path, MD5: adoptDigest(data), Size: int64(len(data))})
	}
	if err := s.writeInstall(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	// Corrupt target 4 so only 9 targets are trusted, in exact manifest order.
	bad := make([]byte, 32<<10)
	adoptPut(t, root, record.Targets[4].Path, bad)
	trusted, err := s.TrustedTargets(context.Background(), root)
	if err != nil || len(trusted) != 9 {
		t.Fatalf("trusted=%d err=%v", len(trusted), err)
	}
	idx := 0
	for i, want := range record.Targets {
		if i == 4 {
			continue
		}
		if trusted[idx] != want {
			t.Fatalf("trusted[%d] = %+v, want %+v", idx, trusted[idx], want)
		}
		idx++
	}
	if _, err := os.Stat(filepath.Join(s.stateRoot, key, "verify.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("TrustedTargets wrote verify.json: %v", err)
	}
}
