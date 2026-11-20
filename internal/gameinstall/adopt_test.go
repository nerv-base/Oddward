// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestAdoptQuickMetadataOnlyAndOfficialUnchanged(t *testing.T) {
	root, _ := adoptFixture(t)
	s := adoptStore(t)
	// Same size corrupt bytes must remain metadata-only, never claimed verified.
	adoptPut(t, root, "Client/WindowsNoEditor/HT/Content/Paks/a.pak", []byte("corrupt first"))
	before := adoptSnapshot(t, root)
	got, err := s.AdoptQuick(context.Background(), filepath.Join(root, "Client"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != Adopted || got.Record.VerifyCoverage != "metadata" || got.Record.VerifiedTargets != 0 || got.Files != 2 || got.Record.Root != root {
		t.Fatalf("metadata adoption: %+v", got)
	}
	if len(got.TagDrift) != 1 {
		t.Fatalf("tag drift must be advisory: %+v", got)
	}
	if !reflect.DeepEqual(before, adoptSnapshot(t, root)) {
		t.Fatal("adoption changed installation")
	}
	record, err := s.ReadInstall(root)
	if err != nil || record.State != Adopted {
		t.Fatalf("durable record: %+v %v", record, err)
	}
	trusted, err := s.TrustedTargets(context.Background(), root)
	if err != nil || len(trusted) != 0 {
		t.Fatalf("metadata must never become trusted: %+v %v", trusted, err)
	}
}
func TestAdoptMissingOrWrongSizeDetected(t *testing.T) {
	for _, mode := range []string{"missing", "size"} {
		t.Run(mode, func(t *testing.T) {
			root, _ := adoptFixture(t)
			s := adoptStore(t)
			p := filepath.Join(root, "Client/WindowsNoEditor/HT/Content/Paks/a.pak")
			if mode == "missing" {
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
			} else {
				adoptPut(t, root, "Client/WindowsNoEditor/HT/Content/Paks/a.pak", []byte("short"))
			}
			got, err := s.AdoptQuick(context.Background(), root, nil)
			if err != nil || got.State != Detected || len(got.Mismatches) != 1 {
				t.Fatalf("bad file must stay detected: %+v %v", got, err)
			}
		})
	}
}
func TestAdoptWithoutMetadataAndRemoteFallback(t *testing.T) {
	root, manifest := adoptFixture(t)
	s := adoptStore(t)
	for _, name := range []string{"config.xml", "ResList.xml"} {
		if err := os.Remove(filepath.Join(root, "NTELauncher/UserData/Patcher/PatcherSDK", name)); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.AdoptQuick(context.Background(), root, nil)
	if err != nil || got.State != Detected {
		t.Fatalf("structure alone: %+v %v", got, err)
	}
	got, err = s.AdoptQuick(context.Background(), root, &AdoptMetadata{ConfigXML: adoptConfig(manifest), Manifest: manifest, Branch: "PC_150"})
	if err != nil || got.State != Adopted {
		t.Fatalf("supplied metadata: %+v %v", got, err)
	}
}
func TestAdoptRejectsUnsafeManifestAndCanceledContext(t *testing.T) {
	root, manifest := adoptFixture(t)
	s := adoptStore(t)
	bad := []byte(strings.ReplaceAll(string(manifest), "Client/WindowsNoEditor/HT/Content/Paks/a.pak", "../escape"))
	adoptPut(t, root, "NTELauncher/UserData/Patcher/PatcherSDK/ResList.xml", bad)
	if _, err := s.AdoptQuick(context.Background(), root, nil); err == nil {
		t.Fatal("unsafe local manifest accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.AdoptQuick(ctx, root, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}
func TestAdoptRejectsDuplicateLocalTargets(t *testing.T) {
	root, manifest := adoptFixture(t)
	s := adoptStore(t)
	duplicate := strings.Replace(string(manifest), "Client/WindowsNoEditor/HT/Content/Paks/b.pak", "Client/WindowsNoEditor/HT/Content/Paks/a.pak", 1)
	adoptPut(t, root, adoptOfficialDir+"ResList.xml", []byte(duplicate))
	if _, err := s.AdoptQuick(context.Background(), root, nil); err == nil {
		t.Fatal("duplicate local targets silently downgraded")
	}
}
func TestAdoptDifferentOfficialGenerationCannotBecomeVerified(t *testing.T) {
	root, manifest := adoptFixture(t)
	s := adoptStore(t)
	config := strings.Replace(string(adoptConfig(manifest)), "<ResVersion>1.2.3</ResVersion>", "<ResVersion>1.2.4</ResVersion>", 1)
	adoptPut(t, root, adoptOfficialDir+"config.xml", []byte(config))
	got, err := s.AdoptQuick(context.Background(), root, nil)
	if err != nil || got.State != Adopted || len(got.Warnings) == 0 {
		t.Fatalf("mismatched official generation: %+v %v", got, err)
	}
	if _, err := s.TrustedTargets(context.Background(), root); !errors.Is(err, ErrStaleState) {
		t.Fatalf("inconsistent baseline trusted: %v", err)
	}
	if _, err := s.VerifyInstallation(context.Background(), root, nil); !errors.Is(err, ErrStaleState) {
		t.Fatalf("inconsistent version claim became verified: %v", err)
	}
}
func TestInstallStoreMissingAtomicValidatedAndRootKey(t *testing.T) {
	root, _ := adoptFixture(t)
	s := adoptStore(t)
	if _, err := s.ReadInstall(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing distinct: %v", err)
	}
	key, err := InstallKey(root)
	if err != nil {
		t.Fatal(err)
	}
	other, err := InstallKey(strings.ToUpper(root) + string(filepath.Separator))
	if err != nil || key != other || len(key) != 16 {
		t.Fatalf("canonical key: %q %q %v", key, other, err)
	}
	got, err := s.AdoptQuick(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	bad := got.Record
	bad.InstallKey = "wrong"
	if err := s.writeInstall(context.Background(), bad); err == nil {
		t.Fatal("invalid record overwrote good state")
	}
	persisted, err := s.ReadInstall(root)
	if err != nil || persisted.InstallKey != key {
		t.Fatalf("valid record lost: %+v %v", persisted, err)
	}
}
