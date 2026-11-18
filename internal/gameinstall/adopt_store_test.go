// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAdoptStoreRejectsMalformedRecordAndCheckpointPreservesPublished(t *testing.T) {
	root, _ := adoptFixture(t)
	stateRoot := t.TempDir()
	s, err := NewStore(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.AdoptQuick(context.Background(), root, nil)
	if err != nil {
		t.Fatal(err)
	}
	sentinel := errors.New("checkpoint interrupted")
	next := got.Record
	next.Version = "next"
	if err := s.writeInstallCheckpoint(next, func() error { return sentinel }); !errors.Is(err, sentinel) {
		t.Fatalf("checkpoint: %v", err)
	}
	current, err := s.ReadInstall(root)
	if err != nil || current.Version != got.Record.Version {
		t.Fatalf("interrupted write published: %+v %v", current, err)
	}
	path := filepath.Join(stateRoot, got.Record.InstallKey, "install.json")
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{"schema":2,"schema":2}`, string(original) + ` {}`, strings.Replace(string(original), `"schema":2`, `"schema":99`, 1), strings.Replace(string(original), `"root":`, `"unknown":1,"root":`, 1)} {
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.ReadInstall(root); !errors.Is(err, ErrInvalidState) {
			t.Fatalf("malformed record accepted: %v", err)
		}
	}
}
func TestAdoptReadInstallRejectsChangedOfficialState(t *testing.T) {
	root, manifest := adoptFixture(t)
	s := adoptStore(t)
	if _, err := s.AdoptQuick(context.Background(), root, nil); err != nil {
		t.Fatal(err)
	}
	adoptPut(t, root, adoptOfficialDir+"ResList.xml", append([]byte("\n"), manifest...))
	if _, err := s.ReadInstall(root); !errors.Is(err, ErrStaleState) {
		t.Fatalf("stale saved metadata accepted: %v", err)
	}
}

func TestAdoptMissingRootDoesNotMasqueradeAsMissingRecord(t *testing.T) {
	root, _ := adoptFixture(t)
	s := adoptStore(t)
	if _, err := s.AdoptQuick(context.Background(), root, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, root+"-moved"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ReadInstall(root); !errors.Is(err, ErrStaleState) || errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale root must remain distinct from absent install record: %v", err)
	}
}
func TestAdoptInstallKeyUsesWindowsOrdinalCase(t *testing.T) {
	root := filepath.Join(t.TempDir(), "σ")
	a, err := InstallKey(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := InstallKey(filepath.Join(filepath.Dir(root), "ς"))
	if err != nil || a != b {
		t.Fatalf("Windows alias has different keys: %q %q %v", a, b, err)
	}
}
