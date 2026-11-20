// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package accounts

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"

	"github.com/nerv-base/Oddward/internal/database/dbtest"
)

func assertPrivateDACL(t *testing.T, path string) {
	t.Helper()
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
	if err != nil {
		t.Fatal(err)
	}
	control, _, err := sd.Control()
	if err != nil {
		t.Fatal(err)
	}
	if control&windows.SE_DACL_PROTECTED == 0 {
		t.Fatalf("%s inherits its DACL", filepath.Base(path))
	}
	acl, _, err := sd.DACL()
	if err != nil || acl == nil {
		t.Fatalf("missing DACL: %v", err)
	}
	if acl.AceCount != 2 {
		t.Fatalf("%s has %d ACEs, want current user + SYSTEM only", filepath.Base(path), acl.AceCount)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{user.User.Sid.String(): true, "S-1-5-18": true}
	for i := uint32(0); i < uint32(acl.AceCount); i++ {
		var ace *windows.ACCESS_ALLOWED_ACE
		if err := windows.GetAce(acl, i, &ace); err != nil {
			t.Fatal(err)
		}
		sid := (*windows.SID)(unsafe.Pointer(&ace.SidStart)).String()
		if ace.Header.AceType != windows.ACCESS_ALLOWED_ACE_TYPE || ace.Header.AceFlags&windows.INHERITED_ACE != 0 || !want[sid] || ace.Mask&0x1f01ff != 0x1f01ff {
			t.Fatalf("unexpected ACL entry for %s", filepath.Base(path))
		}
		delete(want, sid)
	}
	if len(want) != 0 {
		t.Fatal("missing expected private ACL principal")
	}
}

func TestWindowsStoreAndPersistedFilesHaveProtectedPrivateDACL(t *testing.T) {
	s, dir, _ := newTestStore(t)
	assertPrivateDACL(t, dir)
	a := mustSave(t, s, "A", sampleSecret(1))
	if err := s.SetLast(a.ID); err != nil {
		t.Fatal(err)
	}
	mustLink(t, s, a.ID, sampleGrant(1))
	for _, name := range dirNames(t, dir) {
		assertPrivateDACL(t, filepath.Join(dir, name))
	}
	tx, err := s.Prepare(Account{Label: "B"}, sampleSecret(2))
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for _, name := range dirNames(t, dir) {
		assertPrivateDACL(t, filepath.Join(dir, name))
	}
}

func TestOpenRejectsJunctionAncestor(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	makeJunction(t, link, target)
	if _, err := Open(dbtest.Open(t), filepath.Join(link, "accounts"), newFakeProtector(t)); err == nil {
		t.Fatal("Open followed junction ancestor")
	}
	if _, err := os.Stat(filepath.Join(target, "accounts")); !os.IsNotExist(err) {
		t.Fatal("Open wrote through junction ancestor")
	}
}

func TestRemovePreservesJunctionRecord(t *testing.T) {
	s, dir, _ := newTestStore(t)
	target := t.TempDir()
	id := "abcdefabcdefabcdefabcdefabcdefab"
	link := recordPath(dir, id)
	makeJunction(t, link, target)
	if err := s.Remove(id); err == nil {
		t.Fatal("Remove accepted junction record")
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatal("Remove deleted junction")
	}
}
