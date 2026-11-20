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
