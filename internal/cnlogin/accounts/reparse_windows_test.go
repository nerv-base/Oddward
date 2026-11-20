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

	"github.com/nerv-base/Oddward/internal/database/dbtest"
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
	if _, err := Open(dbtest.Open(t), link, newFakeProtector(t)); !errors.Is(err, ErrUnsafePath) {
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

// A mirror that was swapped for a link is never deleted through: the link itself
// is not a file this store wrote, and following it would remove whatever it
// points at.
func TestRemoveRejectsSymlinkedMirror(t *testing.T) {
	s, dir, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	outside := filepath.Join(t.TempDir(), "outside.acct")
	if err := os.Rename(recordPath(dir, a.ID), outside); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, recordPath(dir, a.ID)); err != nil {
		t.Skipf("file symlinks need Developer Mode or privilege here: %v", err)
	}
	if err := s.Remove(a.ID); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Remove(symlink) = %v, want ErrUnsafePath", err)
	}
	if _, err := os.Stat(outside); err != nil {
		t.Fatalf("Remove deleted the link target: %v", err)
	}
	if info, err := os.Lstat(recordPath(dir, a.ID)); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("Remove replaced the link: %v", err)
	}
	if _, err := s.Unlock(a.ID); err != nil {
		t.Fatalf("the row was deleted despite the refused mirror: %v", err)
	}
}
