// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package accounts

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func makeJunction(t *testing.T, link, target string) {
	t.Helper()
	if !filepath.IsAbs(link) || !filepath.IsAbs(target) {
		t.Fatal("junction fixture paths must be absolute temp paths")
	}
	out, err := exec.Command("cmd", "/d", "/c", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Fatalf("synthetic mklink /J failed: %v: %s", err, strings.TrimSpace(string(out)))
	}
}

func TestOpenRejectsJunctionDirectory(t *testing.T) {
	target := filepath.Join(t.TempDir(), "real")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "accounts")
	makeJunction(t, link, target)
	if _, err := Open(link, newFakeProtector(t)); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Open(junction) = %v, want ErrUnsafePath", err)
	}
}

func TestStoreRejectsDirectorySwappedForJunction(t *testing.T) {
	s, dir, _ := newTestStore(t)
	mustSave(t, s, "A", sampleSecret(1))
	moved := dir + "-moved"
	if err := os.Rename(dir, moved); err != nil {
		t.Fatal(err)
	}
	makeJunction(t, dir, moved)
	if _, _, err := s.List(); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("List through swapped junction = %v, want ErrUnsafePath", err)
	}
	if _, err := s.Save(Account{Label: "B"}, sampleSecret(2)); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Save through swapped junction = %v, want ErrUnsafePath", err)
	}
}

func TestSymlinkedRecordIsCorrupt(t *testing.T) {
	s, dir, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	outside := filepath.Join(t.TempDir(), "outside.acct")
	if err := os.Rename(recordPath(dir, a.ID), outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, recordPath(dir, a.ID)); err != nil {
		t.Skipf("file symlinks need Developer Mode or privilege here: %v", err)
	}
	list, corrupt := mustList(t, s)
	if len(list) != 0 || corrupt != 1 {
		t.Fatalf("List = %+v corrupt %d, want symlink counted as corrupt", list, corrupt)
	}
	if _, err := s.Unlock(a.ID); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Unlock(symlink) = %v, want ErrCorrupt", err)
	}
}
