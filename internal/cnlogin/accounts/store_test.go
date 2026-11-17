// SPDX-License-Identifier: GPL-3.0-or-later

package accounts

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

var hexID = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestSaveUnlockRoundTrip(t *testing.T) {
	s, dir, p := newTestStore(t)
	sec := sampleSecret(1)
	a := mustSave(t, s, "138****0000", sec)
	if !hexID.MatchString(a.ID) || a.Label != "138****0000" || a.SavedAt.IsZero() {
		t.Fatalf("saved account = %+v", a)
	}
	raw, err := os.ReadFile(recordPath(dir, a.ID))
	if err != nil {
		t.Fatalf("record file: %v", err)
	}
	for _, plain := range []string{sec.DeviceID, sec.ChannelUID, sec.ChannelToken} {
		if bytes.Contains(raw, []byte(plain)) {
			t.Fatalf("record stores %q in clear: %s", plain, raw)
		}
	}
	got, err := s.Unlock(a.ID)
	if err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if got != sec {
		t.Fatalf("Unlock = %#v, want the saved secret", got.DeviceID == sec.DeviceID)
	}

	// A fresh Store over the same directory sees the same account.
	s2 := openTestStore(t, dir, p)
	list, corrupt := mustList(t, s2)
	if corrupt != 0 || len(list) != 1 || list[0] != a {
		t.Fatalf("reopened List = %+v (corrupt %d), want [%+v]", list, corrupt, a)
	}
	if got, err := s2.Unlock(a.ID); err != nil || got != sec {
		t.Fatalf("reopened Unlock: %v", err)
	}
}

func TestListNewestFirst(t *testing.T) {
	s, _, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	b := mustSave(t, s, "B", sampleSecret(2))
	c := mustSave(t, s, "C", sampleSecret(3))
	list, _ := mustList(t, s)
	if len(list) != 3 || list[0].ID != c.ID || list[1].ID != b.ID || list[2].ID != a.ID {
		t.Fatalf("List order = %+v", list)
	}
}

func TestTamperedPublicFieldsFailUnlock(t *testing.T) {
	cases := map[string]func(t *testing.T, dir, id string) string{
		"label": func(t *testing.T, dir, id string) string {
			editRecord(t, dir, id, func(m map[string]any) string { m["label"] = "139****1111"; return "" })
			return id
		},
		"savedAt": func(t *testing.T, dir, id string) string {
			editRecord(t, dir, id, func(m map[string]any) string { m["savedAt"] = "2030-01-01T00:00:00Z"; return "" })
			return id
		},
		"id": func(t *testing.T, dir, id string) string {
			other := strings.Repeat("ab", 16)
			editRecord(t, dir, id, func(m map[string]any) string {
				m["id"] = other
				return recordPath(dir, other)
			})
			return other
		},
		"secret": func(t *testing.T, dir, id string) string {
			editRecord(t, dir, id, func(m map[string]any) string {
				b := []byte(m["secret"].(string))
				i := len(b) - 4
				if b[i] == 'A' {
					b[i] = 'B'
				} else {
					b[i] = 'A'
				}
				m["secret"] = string(b)
				return ""
			})
			return id
		},
	}
	for name, tamper := range cases {
		t.Run(name, func(t *testing.T) {
			s, dir, _ := newTestStore(t)
			a := mustSave(t, s, "138****0000", sampleSecret(1))
			id := tamper(t, dir, a.ID)
			if _, err := s.Unlock(id); !errors.Is(err, ErrUnlock) {
				t.Fatalf("Unlock after %s edit: err = %v, want ErrUnlock", name, err)
			}
		})
	}
}

func TestIDMismatchWithFileNameIsCorrupt(t *testing.T) {
	s, dir, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	editRecord(t, dir, a.ID, func(m map[string]any) string { m["id"] = strings.Repeat("cd", 16); return "" })
	list, corrupt := mustList(t, s)
	if len(list) != 0 || corrupt != 1 {
		t.Fatalf("List = %+v corrupt %d, want none and 1 corrupt", list, corrupt)
	}
	if _, err := s.Unlock(a.ID); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Unlock = %v, want ErrCorrupt", err)
	}
}

func TestWrongProtectorCannotUnlock(t *testing.T) {
	s, dir, _ := newTestStore(t)
	a := mustSave(t, s, "138****0000", sampleSecret(1))
	other := openTestStore(t, dir, newFakeProtector(t))
	list, _ := mustList(t, other)
	if len(list) != 1 || list[0].ID != a.ID {
		t.Fatalf("public listing must not need the key: %+v", list)
	}
	if _, err := other.Unlock(a.ID); !errors.Is(err, ErrUnlock) {
		t.Fatalf("Unlock with another protector: %v, want ErrUnlock", err)
	}
}

func TestProtectorFailureSavesNothing(t *testing.T) {
	s, dir, p := newTestStore(t)
	p.fail = errors.New("dpapi unavailable")
	if _, err := s.Save(Account{Label: "A"}, sampleSecret(1)); err == nil {
		t.Fatal("Save succeeded without a working protector")
	}
	if names := dirNames(t, dir); len(names) != 0 {
		t.Fatalf("dir after failed save = %v", names)
	}
}

func TestAccountLimit(t *testing.T) {
	s, _, _ := newTestStore(t)
	for i := range MaxAccounts {
		mustSave(t, s, "A", sampleSecret(i))
	}
	if _, err := s.Save(Account{Label: "A"}, sampleSecret(100)); !errors.Is(err, ErrTooManyAccounts) {
		t.Fatalf("17th account: err = %v, want ErrTooManyAccounts", err)
	}
	// Re-saving an existing identity replaces it and is allowed at the limit.
	if _, err := s.Save(Account{}, sampleSecret(3)); err != nil {
		t.Fatalf("re-save at limit: %v", err)
	}
	if list, _ := mustList(t, s); len(list) != MaxAccounts {
		t.Fatalf("accounts = %d", len(list))
	}
}

func TestOversizedRecordSkipped(t *testing.T) {
	s, dir, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	big := strings.Repeat("ef", 16)
	if err := os.WriteFile(recordPath(dir, big), bytes.Repeat([]byte(" "), MaxRecordSize+1), 0o600); err != nil {
		t.Fatal(err)
	}
	list, corrupt := mustList(t, s)
	if len(list) != 1 || list[0].ID != a.ID || corrupt != 1 {
		t.Fatalf("List = %+v corrupt %d", list, corrupt)
	}
	if _, err := s.Unlock(big); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Unlock oversized = %v, want ErrCorrupt", err)
	}
}

func TestLabelSanitizing(t *testing.T) {
	s, _, _ := newTestStore(t)
	a := mustSave(t, s, "  主号\x00\t\u202e138****0000\r\n ", sampleSecret(1))
	if a.Label != "主号138****0000" {
		t.Fatalf("sanitized label = %q", a.Label)
	}
	thirtyTwo := strings.Repeat("异", 32)
	if b := mustSave(t, s, thirtyTwo, sampleSecret(2)); b.Label != thirtyTwo {
		t.Fatalf("32-rune label = %q", b.Label)
	}
	for _, bad := range []string{"", " \x01 ", strings.Repeat("异", 33), "\xff\xfe", "13800138000", "备注 13912345678"} {
		if _, err := s.Save(Account{Label: bad}, sampleSecret(9)); !errors.Is(err, ErrInvalidLabel) {
			t.Fatalf("Save(label %q) = %v, want ErrInvalidLabel", bad, err)
		}
		if err := s.Rename(a.ID, bad); !errors.Is(err, ErrInvalidLabel) {
			t.Fatalf("Rename(%q) = %v, want ErrInvalidLabel", bad, err)
		}
	}
}

func TestSecretValidation(t *testing.T) {
	s, _, _ := newTestStore(t)
	mutate := map[string]func(*Secret){
		"short device":     func(x *Secret) { x.DeviceID = "abc" },
		"upper device":     func(x *Secret) { x.DeviceID = strings.ToUpper(x.DeviceID) },
		"empty uid":        func(x *Secret) { x.ChannelUID = "" },
		"empty token":      func(x *Secret) { x.ChannelToken = "" },
		"control in token": func(x *Secret) { x.ChannelToken = "a\nb" },
		"huge token":       func(x *Secret) { x.ChannelToken = strings.Repeat("t", 4096) },
	}
	for name, m := range mutate {
		sec := sampleSecret(1)
		m(&sec)
		if _, err := s.Save(Account{Label: "A"}, sec); !errors.Is(err, ErrInvalidSecret) {
			t.Fatalf("%s: Save = %v, want ErrInvalidSecret", name, err)
		}
	}
}

func TestRenameReencrypts(t *testing.T) {
	s, _, _ := newTestStore(t)
	sec := sampleSecret(1)
	a := mustSave(t, s, "138****0000", sec)
	if err := s.Rename(a.ID, " 大号 "); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	list, _ := mustList(t, s)
	if len(list) != 1 || list[0].Label != "大号" || !list[0].SavedAt.Equal(a.SavedAt) {
		t.Fatalf("after rename: %+v", list)
	}
	if got, err := s.Unlock(a.ID); err != nil || got != sec {
		t.Fatalf("Unlock after rename: %v", err)
	}
	if err := s.Rename(strings.Repeat("0", 32), "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Rename unknown = %v", err)
	}
}

func TestDedupeByChannelUID(t *testing.T) {
	s, _, _ := newTestStore(t)
	first := sampleSecret(1)
	a := mustSave(t, s, "138****0000", first)
	other := mustSave(t, s, "B", sampleSecret(2))

	refreshed := first
	refreshed.ChannelToken = "token-refreshed"
	refreshed.DeviceID = strings.Repeat("f", 32)
	b := mustSave(t, s, "", refreshed)
	if b.ID != a.ID || b.Label != "138****0000" || !b.SavedAt.After(a.SavedAt) {
		t.Fatalf("dedupe save = %+v, want same id/label as %+v and newer time", b, a)
	}
	list, _ := mustList(t, s)
	if len(list) != 2 || list[0].ID != a.ID || list[1].ID != other.ID {
		t.Fatalf("List after dedupe = %+v", list)
	}
	if got, err := s.Unlock(a.ID); err != nil || got != refreshed {
		t.Fatalf("Unlock after dedupe: %v", err)
	}

	c := mustSave(t, s, "新备注", refreshed)
	if c.ID != a.ID || c.Label != "新备注" {
		t.Fatalf("dedupe with label = %+v", c)
	}
	if _, err := s.Save(Account{}, sampleSecret(7)); !errors.Is(err, ErrInvalidLabel) {
		t.Fatalf("new account without label = %v, want ErrInvalidLabel", err)
	}
}

func TestAtomicSaveFailureLeavesNoTempFiles(t *testing.T) {
	s, dir, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	before, err := os.ReadFile(recordPath(dir, a.ID))
	if err != nil {
		t.Fatal(err)
	}
	injected := errors.New("injected rename failure")
	s.rename = func(string, string) error { return injected }

	if _, err := s.Save(Account{Label: "B"}, sampleSecret(2)); !errors.Is(err, injected) {
		t.Fatalf("Save = %v, want injected failure", err)
	}
	if err := s.Rename(a.ID, "renamed"); !errors.Is(err, injected) {
		t.Fatalf("Rename = %v, want injected failure", err)
	}
	if _, err := s.Save(Account{}, sampleSecret(1)); !errors.Is(err, injected) {
		t.Fatalf("dedupe Save = %v, want injected failure", err)
	}
	if err := s.SetLast(a.ID); !errors.Is(err, injected) {
		t.Fatalf("SetLast = %v, want injected failure", err)
	}
	assertNoTempFiles(t, dir)
	if names := dirNames(t, dir); len(names) != 1 {
		t.Fatalf("dir = %v, want only the original record", names)
	}
	after, err := os.ReadFile(recordPath(dir, a.ID))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("original record changed by failed writes: %v", err)
	}
}

func TestSetLastOntoDirectoryFailsCleanly(t *testing.T) {
	s, dir, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	if err := os.MkdirAll(filepath.Join(dir, lastFileName, "x"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLast(a.ID); err == nil {
		t.Fatal("SetLast onto a directory succeeded")
	}
	assertNoTempFiles(t, dir)
}

func TestCorruptFilesSkipped(t *testing.T) {
	s, dir, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(strings.Repeat("1", 32)+".acct", "not json")
	write(strings.Repeat("2", 32)+".acct", "")
	write(strings.Repeat("3", 32)+".acct", `{"version":99,"id":"`+strings.Repeat("3", 32)+`","label":"x","savedAt":"2026-01-01T00:00:00Z","secret":"AAAA"}`)
	write("notes.txt", "ignored")
	write("XYZ.acct", "ignored: not an id")
	if err := os.Mkdir(filepath.Join(dir, strings.Repeat("4", 32)+".acct"), 0o700); err != nil {
		t.Fatal(err)
	}
	list, corrupt := mustList(t, s)
	if len(list) != 1 || list[0].ID != a.ID || corrupt != 4 {
		t.Fatalf("List = %+v corrupt %d, want 1 account and 4 corrupt", list, corrupt)
	}
	// Corrupt records can still be removed by id.
	if err := s.Remove(strings.Repeat("1", 32)); err != nil {
		t.Fatalf("Remove corrupt: %v", err)
	}
}

func TestRemove(t *testing.T) {
	s, dir, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	if err := s.Remove(a.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if _, err := os.Stat(recordPath(dir, a.ID)); !os.IsNotExist(err) {
		t.Fatalf("record still present: %v", err)
	}
	if _, err := s.Unlock(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Unlock removed = %v", err)
	}
	for _, id := range []string{a.ID, "../" + a.ID, "", strings.ToUpper(a.ID), a.ID + ".acct"} {
		if err := s.Remove(id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Remove(%q) = %v, want ErrNotFound", id, err)
		}
		if _, err := s.Unlock(id); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Unlock(%q) = %v, want ErrNotFound", id, err)
		}
	}
}

func TestLastAccount(t *testing.T) {
	s, dir, _ := newTestStore(t)
	if id, err := s.Last(); err != nil || id != "" {
		t.Fatalf("empty store Last = %q %v", id, err)
	}
	a := mustSave(t, s, "A", sampleSecret(1))
	if id, _ := s.Last(); id != a.ID {
		t.Fatalf("single account with no state file: Last = %q, want %q", id, a.ID)
	}
	b := mustSave(t, s, "B", sampleSecret(2))
	if id, _ := s.Last(); id != "" {
		t.Fatalf("two accounts, nothing remembered: Last = %q", id)
	}
	if err := s.SetLast(b.ID); err != nil {
		t.Fatalf("SetLast: %v", err)
	}
	if id, _ := s.Last(); id != b.ID {
		t.Fatalf("Last = %q, want %q", id, b.ID)
	}
	if err := s.SetLast(strings.Repeat("9", 32)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetLast unknown = %v", err)
	}
	if err := s.SetLast("../evil"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SetLast invalid = %v", err)
	}
	// Removing the remembered account clears it explicitly; the survivor is
	// not silently promoted.
	if err := s.Remove(b.ID); err != nil {
		t.Fatal(err)
	}
	if id, _ := s.Last(); id != "" {
		t.Fatalf("Last after removing remembered account = %q", id)
	}
	if err := s.SetLast(a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLast(""); err != nil {
		t.Fatalf("SetLast clear: %v", err)
	}
	if id, _ := s.Last(); id != "" {
		t.Fatalf("Last after clear = %q", id)
	}
	// A garbage state file means "nothing remembered".
	if err := os.WriteFile(filepath.Join(dir, lastFileName), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if id, err := s.Last(); err != nil || id != "" {
		t.Fatalf("Last with garbage = %q %v", id, err)
	}
	// A remembered id whose record became corrupt resolves to nothing.
	if err := s.SetLast(a.ID); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recordPath(dir, a.ID), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if id, _ := s.Last(); id != "" {
		t.Fatalf("Last for corrupt record = %q", id)
	}
}

func TestOpenValidatesDirectory(t *testing.T) {
	p := newFakeProtector(t)
	if _, err := Open("relative\\accounts", p); err == nil {
		t.Fatal("Open accepted a relative path")
	}
	if _, err := Open(filepath.Join(t.TempDir(), "x"), nil); err == nil {
		t.Fatal("Open accepted a nil protector")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(file, p); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Open(file) = %v, want ErrUnsafePath", err)
	}
	dir := filepath.Join(t.TempDir(), "nested", "accounts")
	if _, err := Open(dir, p); err != nil {
		t.Fatalf("Open creates missing dir: %v", err)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		t.Fatalf("dir not created: %v", err)
	}
}

func TestOpenRemovesStaleTempFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "accounts")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, tempPrefix+"0123456789abcdef.tmp")
	keep := filepath.Join(dir, "unrelated.tmp")
	for _, f := range []string{stale, keep} {
		if err := os.WriteFile(f, []byte("partial"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	openTestStore(t, dir, newFakeProtector(t))
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("stale temp file kept: %v", err)
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("unrelated file removed: %v", err)
	}
}

func TestMaskPhone(t *testing.T) {
	if got, err := MaskPhone("13800000000"); err != nil || got != "138****0000" {
		t.Fatalf("MaskPhone = %q %v", got, err)
	}
	for _, bad := range []string{"", "1380000000", "23800000000", "138000000001", "1380000000a"} {
		if _, err := MaskPhone(bad); err == nil {
			t.Fatalf("MaskPhone(%q) accepted", bad)
		}
	}
}
