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

// An Oddward transaction owns these synthetic resources, without official
// launcher metadata or a runnable PE. Its publication record is the baseline.
func verifyOwnedFixture(t *testing.T) (*Store, InstallRecord) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "owned install")
	adoptPut(t, root, "Client/WindowsNoEditor/HT/Content/Paks/a.pak", []byte("correct first"))
	adoptPut(t, root, "Client/WindowsNoEditor/HT/Content/Paks/b.pak", []byte("correct second"))
	s := adoptStore(t)
	key, err := InstallKey(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	record := InstallRecord{Schema: 2, InstallKey: key, Root: root, Mode: "oddward", State: "ready", Branch: "PC_150", Version: "1.2.3", ListHash: adoptDigest([]byte("owned manifest")), ManifestDigest: adoptDigest([]byte("owned manifest")), TxnID: "published-native-transaction", BaseVersion: "1.2.2", VerifyCoverage: "full", VerifiedTargets: 2, CreatedAt: now.Add(-time.Hour), PublishedAt: now,
		Targets: []InstallTarget{{Path: "Client/WindowsNoEditor/HT/Content/Paks/a.pak", MD5: adoptDigest([]byte("correct first")), Size: 13}, {Path: "Client/WindowsNoEditor/HT/Content/Paks/b.pak", MD5: adoptDigest([]byte("correct second")), Size: 14}}}
	if err := s.writeInstall(record); err != nil {
		t.Fatal(err)
	}
	record, err = s.ReadInstall(root)
	if err != nil {
		t.Fatal(err)
	}
	return s, record
}
func TestVerifyTrustedReadyWithoutFingerprintsRehashesWithoutWriting(t *testing.T) {
	s, record := verifyOwnedFixture(t)
	before := adoptSnapshot(t, s.stateRoot)
	targets, err := s.TrustedTargets(context.Background(), record.Root)
	if err != nil || len(targets) != 2 {
		t.Fatalf("published targets without cache must be hashed for planning: %+v %v", targets, err)
	}
	after := adoptSnapshot(t, s.stateRoot)
	if len(before) != len(after) {
		t.Fatal("read-side trust check created state")
	}
	for path, value := range before {
		if after[path] != value {
			t.Fatal("read-side trust check changed state")
		}
	}
	adoptPut(t, record.Root, record.Targets[0].Path, []byte("corrupt first"))
	targets, err = s.TrustedTargets(context.Background(), record.Root)
	if err != nil || len(targets) != 1 || targets[0].Path != record.Targets[1].Path {
		t.Fatalf("full record metadata must not trust corrupted contents: %+v %v", targets, err)
	}
	if _, err := os.Stat(filepath.Join(s.stateRoot, record.InstallKey, "verify.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-side fallback wrote verification cache: %v", err)
	}
}
func TestVerifyOwnedReadyWithoutOfficialSetupPreservesPublication(t *testing.T) {
	s, record := verifyOwnedFixture(t)
	got, err := s.VerifyInstallation(context.Background(), record.Root, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Verified != 2 || got.Record.State != "ready" || got.Record.Mode != record.Mode || got.Record.TxnID != record.TxnID || got.Record.BaseVersion != record.BaseVersion || !got.Record.PublishedAt.Equal(record.PublishedAt) || !got.Record.CreatedAt.Equal(record.CreatedAt) || got.Record.VerifyCoverage != "full" {
		t.Fatalf("verification changed publication provenance: %+v", got)
	}
	trusted, err := s.TrustedTargets(context.Background(), record.Root)
	if err != nil || len(trusted) != 2 {
		t.Fatalf("native fingerprints unavailable: %+v %v", trusted, err)
	}
	second, err := s.VerifyInstallation(context.Background(), record.Root, nil)
	if err != nil || second.Reused != 2 || second.Record.State != "ready" {
		t.Fatalf("native verify did not reuse fingerprints: %+v %v", second, err)
	}
}
func TestVerifyOwnedReadyCancellationPreservesPublication(t *testing.T) {
	s, record := verifyOwnedFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	_, err := s.VerifyInstallation(ctx, record.Root, func(r VerifyReport) {
		if r.FilesDone == 1 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel native verify: %v", err)
	}
	current, err := s.ReadInstall(record.Root)
	if err != nil || current.State != "ready" || current.VerifyCoverage != "full" || current.VerifiedTargets != 2 || current.TxnID != record.TxnID || !current.PublishedAt.Equal(record.PublishedAt) {
		t.Fatalf("cancellation lost publication evidence: %+v %v", current, err)
	}
}
