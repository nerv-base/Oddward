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

func TestSyncTrustedTargetsReusesPriorManifestFingerprintsAndPersists(t *testing.T) {
	root := filepath.Join(t.TempDir(), "legacy sync")
	s := adoptStore(t)
	key, err := InstallKey(root)
	if err != nil {
		t.Fatal(err)
	}
	unchangedA := []byte("unchanged-file-alpha")
	unchangedB := []byte("unchanged-file-beta")
	oldC := []byte("old-version-gamma")
	newC := []byte("new-version-gamma-updated")
	pathA := "Client/WindowsNoEditor/HT/Content/Paks/a.pak"
	pathB := "Client/WindowsNoEditor/HT/Content/Paks/b.pak"
	pathC := "Client/WindowsNoEditor/HT/Content/Paks/c.pak"
	adoptPut(t, root, pathA, unchangedA)
	adoptPut(t, root, pathB, unchangedB)
	adoptPut(t, root, pathC, oldC)
	now := time.Now().UTC()
	v1 := InstallRecord{
		Schema: 2, InstallKey: key, Root: root, Mode: "oddward", State: "ready",
		Branch: "PC_150", Version: "1.5.6",
		ListHash: adoptDigest([]byte("v1-list")), ManifestDigest: adoptDigest([]byte("v1-manifest")),
		TxnID: "txn-v1", VerifyCoverage: "full", VerifiedTargets: 3,
		CreatedAt: now, PublishedAt: now,
		Targets: []InstallTarget{
			{Path: pathA, MD5: adoptDigest(unchangedA), Size: int64(len(unchangedA))},
			{Path: pathB, MD5: adoptDigest(unchangedB), Size: int64(len(unchangedB))},
			{Path: pathC, MD5: adoptDigest(oldC), Size: int64(len(oldC))},
		},
	}
	if err := s.writeInstall(context.Background(), v1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.VerifyInstallation(context.Background(), root, nil); err != nil {
		t.Fatal(err)
	}
	oldDoc, err := s.verifyRead(v1)
	if err != nil || len(oldDoc.Entries) != 3 {
		t.Fatalf("v1 verifyRead = %+v %v", oldDoc, err)
	}
	// Simulate a legacy v0.3.0 update to 1.5.8: c.pak updated and install.json
	// moved to v2's ManifestDigest, while verify.json still has v1's digest.
	adoptPut(t, root, pathC, newC)
	v2 := v1
	v2.Version = "1.5.8"
	v2.ListHash = adoptDigest([]byte("v2-list"))
	v2.ManifestDigest = adoptDigest([]byte("v2-manifest"))
	v2.TxnID = "txn-v2"
	v2.Targets = []InstallTarget{
		{Path: pathA, MD5: adoptDigest(unchangedA), Size: int64(len(unchangedA))},
		{Path: pathB, MD5: adoptDigest(unchangedB), Size: int64(len(unchangedB))},
		{Path: pathC, MD5: adoptDigest(newC), Size: int64(len(newC))},
	}
	if err := s.writeInstall(context.Background(), v2); err != nil {
		t.Fatal(err)
	}

	// VerifyInstallation must reuse the 2 unchanged files across ManifestDigest
	// instead of rehashing all 3.
	vRes, err := s.VerifyInstallation(context.Background(), root, nil)
	if err != nil || vRes.Verified != 3 || vRes.Reused != 2 {
		t.Fatalf("VerifyInstallation across manifest digest: %+v %v", vRes, err)
	}

	// Rewind verify.json back to v1 so we can test PlanTargets directly.
	if err := s.stateWrite(key+"/verify.json", oldDoc, nil); err != nil {
		t.Fatal(err)
	}
	planned, err := s.PlanTargets(context.Background(), root)
	if err != nil || len(planned) != 3 {
		t.Fatalf("PlanTargets = %d err=%v", len(planned), err)
	}
	// PlanTargets must not fabricate or rewrite verify.json.
	stillOld, err := s.verifyRead(v2)
	if err != nil || len(stillOld.Entries) != 0 {
		t.Fatalf("PlanTargets wrote verify.json: %+v %v", stillOld, err)
	}
	// Corrupting a file that has a verified fingerprint in verify.json (same
	// size) must still be detected and excluded from PlanTargets.
	corruptA := make([]byte, len(unchangedA))
	copy(corruptA, unchangedA)
	corruptA[0] ^= 0xff
	adoptPut(t, root, pathA, corruptA)
	planned2, err := s.PlanTargets(context.Background(), root)
	if err != nil || len(planned2) != 2 {
		t.Fatalf("PlanTargets trusted corrupted verified file: %+v %v", planned2, err)
	}
	// Missing or wrong-size unverified files must be excluded from PlanTargets.
	adoptPut(t, root, pathC, []byte("short"))
	planned3, err := s.PlanTargets(context.Background(), root)
	if err != nil || len(planned3) != 1 || planned3[0].Path != pathB {
		t.Fatalf("PlanTargets trusted wrong-size file: %+v %v", planned3, err)
	}
}

func TestPlanTargetsMetadataAdoptedWithoutVerifyJSONSkipsHashing(t *testing.T) {
	root, _ := adoptFixture(t)
	s := adoptStore(t)
	if _, err := s.AdoptQuick(context.Background(), root, nil); err != nil {
		t.Fatal(err)
	}
	// AdoptQuick has VerifyCoverage="metadata" and no verify.json; PlanTargets
	// must accept matching regular files by stat(size) without hashing.
	planned, err := s.PlanTargets(context.Background(), root)
	if err != nil || len(planned) != 2 {
		t.Fatalf("PlanTargets on adopted baseline = %+v %v", planned, err)
	}
	// Strict TrustedTargets must still refuse unverified metadata baselines.
	trusted, err := s.TrustedTargets(context.Background(), root)
	if err != nil || len(trusted) != 0 {
		t.Fatalf("TrustedTargets trusted metadata baseline: %+v %v", trusted, err)
	}
}
