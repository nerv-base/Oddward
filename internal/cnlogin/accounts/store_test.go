// SPDX-License-Identifier: GPL-3.0-or-later

package accounts

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nerv-base/Oddward/internal/database/dbtest"
)

func TestSaveUnlockRoundTrip(t *testing.T) {
	s, dir, p := newTestStore(t)
	sec := sampleSecret(1)
	a := mustSave(t, s, "个人 账号", sec)
	if !isLowerHex(a.ID, 32) {
		t.Fatalf("account id = %q, want 32 lowercase hex chars", a.ID)
	}
	if a.Label != "个人 账号" || a.SavedAt.IsZero() {
		t.Fatalf("saved account = %+v", a)
	}
	stored := rowBytes(t, s, a.ID)
	for _, plain := range []string{sec.DeviceID, sec.ChannelUID, sec.ChannelToken, "SECRETVALUE"} {
		if bytes.Contains(stored, []byte(plain)) {
			t.Fatalf("stored record leaks %q", plain)
		}
	}
	// The mirror is the byte sequence the previous build reads, so it has to be
	// exactly the stored record and no clearer.
	mirror, err := os.ReadFile(recordPath(dir, a.ID))
	if err != nil {
		t.Fatalf("mirror file: %v", err)
	}
	if !bytes.Equal(mirror, stored) {
		t.Fatal("mirror differs from the stored record bytes")
	}
	got, err := s.Unlock(a.ID)
	if err != nil {
		t.Fatalf("Unlock: %v", err)
	}
	if got != sec {
		t.Fatalf("Unlock = %+v, want the saved secret", got)
	}
	// A second store over the same database and protector sees the same account.
	reopened := openTestStore(t, s.db, dir, p)
	list, corrupt := mustList(t, reopened)
	if corrupt != 0 || len(list) != 1 || list[0] != a {
		t.Fatalf("List = %+v (corrupt %d), want the saved account", list, corrupt)
	}
	if got, err := reopened.Unlock(a.ID); err != nil || got != sec {
		t.Fatalf("Unlock after reopen = %+v, %v", got, err)
	}
}

func TestListNewestFirst(t *testing.T) {
	s, _, _ := newTestStore(t)
	var ids []string
	for i := 1; i <= 3; i++ {
		ids = append(ids, mustSave(t, s, fmt.Sprintf("L%d", i), sampleSecret(i)).ID)
	}
	list, _ := mustList(t, s)
	if len(list) != len(ids) {
		t.Fatalf("List returned %d accounts, want %d", len(list), len(ids))
	}
	for i, a := range list {
		if want := ids[len(ids)-1-i]; a.ID != want {
			t.Fatalf("List[%d] = %s, want %s (newest first)", i, a.ID, want)
		}
	}
}

func TestTamperedPublicFieldsFailUnlock(t *testing.T) {
	cases := map[string]func(map[string]any){
		"label": func(m map[string]any) { m["label"] = "another label" },
		"savedAt": func(m map[string]any) {
			m["savedAt"] = "2020-01-01T00:00:00Z"
		},
		"identityKey": func(m map[string]any) { m["identityKey"] = strings.Repeat("A", 64) },
		"secret": func(m map[string]any) {
			// Flip a bit of the decoded ciphertext: flipping a base64 character
			// can leave the decoded bytes unchanged when it only touches padding
			// bits, which would test nothing.
			b, err := base64.StdEncoding.DecodeString(m["secret"].(string))
			if err != nil || len(b) < 8 {
				t.Fatalf("secret is not base64 ciphertext: %v", err)
			}
			b[len(b)/2] ^= 0x01
			m["secret"] = base64.StdEncoding.EncodeToString(b)
		},
	}
	for name, edit := range cases {
		t.Run(name, func(t *testing.T) {
			s, _, _ := newTestStore(t)
			a := mustSave(t, s, "A", sampleSecret(1))
			tamperRecord(t, s, a.ID, edit)
			if _, err := s.Unlock(a.ID); !errors.Is(err, ErrUnlock) {
				t.Fatalf("Unlock after %s tamper = %v, want ErrUnlock", name, err)
			}
		})
	}
}

func TestRecordIDMismatchIsCorrupt(t *testing.T) {
	s, _, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	tamperRecord(t, s, a.ID, func(m map[string]any) { m["id"] = strings.Repeat("b", 32) })
	list, corrupt := mustList(t, s)
	if len(list) != 0 || corrupt != 1 {
		t.Fatalf("List = %+v (corrupt %d), want one corrupt row", list, corrupt)
	}
	if _, err := s.Unlock(a.ID); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Unlock of a record with a foreign id = %v, want ErrCorrupt", err)
	}
	// A row that does not parse is still removable: the user can drop it.
	if err := s.Remove(a.ID); err != nil {
		t.Fatalf("Remove of a corrupt row: %v", err)
	}
}

func TestWrongProtectorCannotUnlock(t *testing.T) {
	s, dir, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	other := openTestStore(t, s.db, dir, newFakeProtector(t))
	list, corrupt := mustList(t, other)
	if corrupt != 0 || len(list) != 1 || list[0].ID != a.ID {
		t.Fatalf("List with a foreign protector = %+v (corrupt %d)", list, corrupt)
	}
	if _, err := other.Unlock(a.ID); !errors.Is(err, ErrUnlock) {
		t.Fatalf("Unlock with a foreign protector = %v, want ErrUnlock", err)
	}
}

func TestProtectorFailureSavesNothing(t *testing.T) {
	s, dir, p := newTestStore(t)
	p.fail = errors.New("dpapi unavailable")
	if _, err := s.Save(Account{Label: "A"}, sampleSecret(1)); err == nil {
		t.Fatal("Save succeeded without a working protector")
	}
	if n := countRows(t, s, `SELECT count(*) FROM accounts`); n != 0 {
		t.Fatalf("failed save stored %d rows", n)
	}
	if names := dirNames(t, dir); len(names) != 0 {
		t.Fatalf("failed save left files: %v", names)
	}
}

func TestAccountLimit(t *testing.T) {
	s, _, _ := newTestStore(t)
	for i := 0; i < MaxAccounts; i++ {
		mustSave(t, s, fmt.Sprintf("L%d", i), sampleSecret(i))
	}
	if _, err := s.Save(Account{Label: "one too many"}, sampleSecret(MaxAccounts)); !errors.Is(err, ErrTooManyAccounts) {
		t.Fatalf("Save beyond MaxAccounts = %v, want ErrTooManyAccounts", err)
	}
	// Refreshing an account at the limit is not a new account.
	if _, err := s.Save(Account{}, sampleSecret(0)); err != nil {
		t.Fatalf("refresh at MaxAccounts: %v", err)
	}
	if n := countRows(t, s, `SELECT count(*) FROM accounts`); n != MaxAccounts {
		t.Fatalf("account count = %d, want %d", n, MaxAccounts)
	}
}

func TestUnparsableRowIsSkipped(t *testing.T) {
	cases := map[string][]byte{
		"garbage":    []byte("not a record at all"),
		"bad json":   []byte(`{"version":"one"}`),
		"oversized":  bytes.Repeat([]byte(" "), MaxRecordSize+1),
		"empty blob": {},
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			s, _, _ := newTestStore(t)
			id := strings.Repeat("c", 32)
			insertAccount(t, s, id, raw)
			list, corrupt := mustList(t, s)
			if len(list) != 0 || corrupt != 1 {
				t.Fatalf("List = %+v (corrupt %d), want one skipped row", list, corrupt)
			}
			if _, err := s.Unlock(id); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("Unlock of an unparsable row = %v, want ErrCorrupt", err)
			}
		})
	}
}

func TestLabelSanitizing(t *testing.T) {
	s, _, _ := newTestStore(t)
	a := mustSave(t, s, " 主\u200b号 ", sampleSecret(1))
	if a.Label != "主号" {
		t.Fatalf("stored label = %q, want the invisible characters dropped", a.Label)
	}
	long := mustSave(t, s, strings.Repeat("标", MaxLabelRunes), sampleSecret(2))
	if n := utf8.RuneCountInString(long.Label); n != MaxLabelRunes {
		t.Fatalf("stored label has %d runes, want %d", n, MaxLabelRunes)
	}
	bad := map[string]string{
		"blank":        "   ",
		"too long":     strings.Repeat("标", MaxLabelRunes+1),
		"phone number": "13800000000",
	}
	for name, label := range bad {
		if _, err := s.Save(Account{Label: label}, sampleSecret(3)); !errors.Is(err, ErrInvalidLabel) {
			t.Fatalf("Save with a %s label = %v, want ErrInvalidLabel", name, err)
		}
	}
	// Control characters are dropped rather than refused, so a label with a line
	// break still stores the visible text.
	joined := mustSave(t, s, "a\nb", sampleSecret(3))
	if joined.Label != "ab" {
		t.Fatalf("stored label = %q, want the line break dropped", joined.Label)
	}
	if err := s.Rename(a.ID, "  "); !errors.Is(err, ErrInvalidLabel) {
		t.Fatalf("Rename to a blank label = %v, want ErrInvalidLabel", err)
	}
}

func TestSecretValidation(t *testing.T) {
	s, _, _ := newTestStore(t)
	blank := sampleSecret(1)
	blank.ChannelUID = ""
	if _, err := s.Save(Account{Label: "A"}, blank); !errors.Is(err, ErrInvalidSecret) {
		t.Fatalf("Save with a blank channel uid = %v, want ErrInvalidSecret", err)
	}
	longToken := sampleSecret(1)
	longToken.ChannelToken = strings.Repeat("t", 128)
	if _, err := s.Save(Account{Label: "A"}, longToken); !errors.Is(err, ErrInvalidSecret) {
		t.Fatalf("Save with an oversized token = %v, want ErrInvalidSecret", err)
	}
	upperDevice := sampleSecret(1)
	upperDevice.DeviceID = strings.ToUpper(upperDevice.DeviceID)
	if _, err := s.Save(Account{Label: "A"}, upperDevice); !errors.Is(err, ErrInvalidSecret) {
		t.Fatalf("Save with an uppercase device id = %v, want ErrInvalidSecret", err)
	}
}

func TestRenameReencrypts(t *testing.T) {
	s, dir, _ := newTestStore(t)
	a := mustSave(t, s, "old label", sampleSecret(1))
	before := rowBytes(t, s, a.ID)
	if err := s.Rename(a.ID, "new label"); err != nil {
		t.Fatalf("Rename: %v", err)
	}
	after := rowBytes(t, s, a.ID)
	if bytes.Equal(before, after) {
		t.Fatal("Rename did not reseal the record; the label is part of the AAD")
	}
	mirror, err := os.ReadFile(recordPath(dir, a.ID))
	if err != nil {
		t.Fatalf("mirror file: %v", err)
	}
	if !bytes.Equal(mirror, after) {
		t.Fatal("Rename did not mirror the new bytes")
	}
	list, _ := mustList(t, s)
	if len(list) != 1 || list[0].Label != "new label" {
		t.Fatalf("List after Rename = %+v", list)
	}
	if !list[0].SavedAt.Equal(a.SavedAt) {
		t.Fatalf("Rename changed saved-at: %v, want %v", list[0].SavedAt, a.SavedAt)
	}
	if got, err := s.Unlock(a.ID); err != nil || got != sampleSecret(1) {
		t.Fatalf("Unlock after Rename = %+v, %v", got, err)
	}
	if err := s.Rename(strings.Repeat("0", 32), "label"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Rename of an unknown account = %v, want ErrNotFound", err)
	}
}

func TestDedupeByChannelUID(t *testing.T) {
	s, _, _ := newTestStore(t)
	first := mustSave(t, s, "A", sampleSecret(1))
	second := mustSave(t, s, "B", sampleSecret(2))
	refreshed := sampleSecret(1)
	refreshed.ChannelToken = "replacement-token"
	again, err := s.Save(Account{}, refreshed)
	if err != nil {
		t.Fatalf("refresh of a known login: %v", err)
	}
	if again.ID != first.ID {
		t.Fatalf("refresh created account %s; it must update %s", again.ID, first.ID)
	}
	if again.Label != first.Label {
		t.Fatalf("labelless refresh replaced label %q with %q", first.Label, again.Label)
	}
	if !again.SavedAt.After(first.SavedAt) {
		t.Fatalf("refresh kept saved-at %v; it must record the new save", again.SavedAt)
	}
	if got, err := s.Unlock(first.ID); err != nil || got != refreshed {
		t.Fatalf("Unlock after refresh = %+v, %v", got, err)
	}
	list, _ := mustList(t, s)
	if len(list) != 2 || list[0].ID != first.ID || list[1].ID != second.ID {
		t.Fatalf("List after refresh = %+v, want the refreshed account first", list)
	}
	// An explicit label replaces the stored one.
	relabelled, err := s.Save(Account{Label: "renamed"}, sampleSecret(1))
	if err != nil || relabelled.ID != first.ID || relabelled.Label != "renamed" {
		t.Fatalf("labelled refresh = %+v, %v", relabelled, err)
	}
	// A login that does not match anything is a new account and needs a label.
	if _, err := s.Save(Account{}, sampleSecret(3)); !errors.Is(err, ErrInvalidLabel) {
		t.Fatalf("Save of an unknown login without a label = %v, want ErrInvalidLabel", err)
	}
}

func TestDedupeMatchesKeylessRecord(t *testing.T) {
	s, _, _ := newTestStore(t)
	// A record written before a subject key existed: the AAD carries no identity
	// key, so only decryption can recognise it.
	id := strings.Repeat("d", 32)
	raw, err := s.sealNew(Account{ID: id, Label: "masked", SavedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)}, "", sampleSecret(1))
	if err != nil {
		t.Fatal(err)
	}
	insertAccount(t, s, id, raw)
	got, err := s.Save(Account{}, sampleSecret(1))
	if err != nil {
		t.Fatalf("Save of a keyless login: %v", err)
	}
	if got.ID != id {
		t.Fatalf("Save created %s; it must update the keyless record %s", got.ID, id)
	}
	if n := countRows(t, s, `SELECT count(*) FROM accounts`); n != 1 {
		t.Fatalf("account count = %d, want 1", n)
	}
	// The record now carries the identity key the subject key derives.
	var key string
	if err := s.db.QueryRow(t.Context(), `SELECT identity_key FROM accounts WHERE id = ?`, id).Scan(&key); err != nil {
		t.Fatal(err)
	}
	if !isUpperHex(key, identityKeyLen) {
		t.Fatalf("identity key column = %q, want 64 uppercase hex chars", key)
	}
}

func TestSetLastOntoDirectoryFailsCleanly(t *testing.T) {
	s, dir, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	// A directory where the mirror belongs: the row still commits, so the failure
	// is reported by the operation instead of being swallowed.
	if err := os.Mkdir(filepath.Join(dir, lastFileName), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLast(a.ID); err == nil {
		t.Fatal("SetLast succeeded although its mirror could not be written")
	}
	assertNoTempFiles(t, dir)
}

func TestRemove(t *testing.T) {
	s, dir, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	mustLink(t, s, a.ID, sampleGrant(1))
	if err := s.Remove(a.ID); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if n := countRows(t, s, `SELECT count(*) FROM accounts`); n != 0 {
		t.Fatalf("Remove left %d rows", n)
	}
	for _, name := range []string{recordPath(dir, a.ID), tajiduoPath(dir, a.ID)} {
		if _, err := os.Stat(name); !os.IsNotExist(err) {
			t.Fatalf("Remove left %s: %v", filepath.Base(name), err)
		}
	}
	if _, err := s.Unlock(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Unlock after Remove = %v, want ErrNotFound", err)
	}
	if err := s.Remove(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Remove = %v, want ErrNotFound", err)
	}
	for _, bad := range []string{"", strings.ToUpper(a.ID), a.ID + recordExt, "../../" + a.ID, "nope"} {
		if err := s.Remove(bad); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Remove(%q) = %v, want ErrNotFound", bad, err)
		}
	}
	assertNoTempFiles(t, dir)
}

func TestLastAccount(t *testing.T) {
	s, dir, p := newTestStore(t)
	if id, err := s.Last(); err != nil || id != "" {
		t.Fatalf("Last of an empty store = %q, %v", id, err)
	}
	a := mustSave(t, s, "A", sampleSecret(1))
	if id, err := s.Last(); err != nil || id != a.ID {
		t.Fatalf("Last with one account = %q, %v; a lone account needs no picker", id, err)
	}
	b := mustSave(t, s, "B", sampleSecret(2))
	if id, err := s.Last(); err != nil || id != "" {
		t.Fatalf("Last with two accounts and no choice = %q, %v", id, err)
	}
	if err := s.SetLast(b.ID); err != nil {
		t.Fatalf("SetLast: %v", err)
	}
	if id, _ := s.Last(); id != b.ID {
		t.Fatalf("Last = %q, want the remembered %q", id, b.ID)
	}
	// An explicit "none" survives a reopen: it is not the same as never choosing.
	reopened := openTestStore(t, s.db, dir, p)
	if id, _ := reopened.Last(); id != b.ID {
		t.Fatalf("Last after reopen = %q, want %q", id, b.ID)
	}
	if err := s.SetLast(""); err != nil {
		t.Fatalf("SetLast(\"\"): %v", err)
	}
	if id, _ := s.Last(); id != "" {
		t.Fatalf("Last after an explicit none = %q, want \"\"", id)
	}
	for _, bad := range []string{strings.Repeat("0", 32), "nope", strings.ToUpper(b.ID)} {
		if err := s.SetLast(bad); !errors.Is(err, ErrNotFound) {
			t.Fatalf("SetLast(%q) = %v, want ErrNotFound", bad, err)
		}
	}
	// Removing the remembered account forgets it instead of promoting the other.
	if err := s.SetLast(b.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.Remove(b.ID); err != nil {
		t.Fatal(err)
	}
	if id, _ := s.Last(); id != "" {
		t.Fatalf("Last after removing the remembered account = %q, want \"\"", id)
	}
	// A stored state value that names no account reads as "explicitly none".
	if _, err := s.db.Exec(t.Context(), `UPDATE account_state SET last_account = ?`, lastNone); err != nil {
		t.Fatal(err)
	}
	if id, _ := s.Last(); id != "" {
		t.Fatalf("Last with an unreadable remembered id = %q, want \"\"", id)
	}
	// A remembered record that no longer parses is not a choice any more.
	if err := s.SetLast(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(t.Context(), `UPDATE accounts SET record = ? WHERE id = ?`, []byte("{"), a.ID); err != nil {
		t.Fatal(err)
	}
	if id, _ := s.Last(); id != "" {
		t.Fatalf("Last with a corrupt remembered record = %q, want \"\"", id)
	}
}

func TestOpenValidatesDirectory(t *testing.T) {
	db := dbtest.Open(t)
	dir := filepath.Join(t.TempDir(), "accounts")
	p := newFakeProtector(t)
	if _, err := Open(nil, dir, p); err == nil {
		t.Fatal("Open accepted a nil database")
	}
	if _, err := Open(db, dir, nil); err == nil {
		t.Fatal("Open accepted a nil protector")
	}
	if _, err := Open(db, "relative/accounts", p); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Open of a relative directory = %v, want ErrUnsafePath", err)
	}
	file := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(db, file, p); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("Open of a plain file = %v, want ErrUnsafePath", err)
	}
	// A nested directory that does not exist yet is created.
	nested := filepath.Join(t.TempDir(), "a", "b", "accounts")
	if _, err := Open(db, nested, p); err != nil {
		t.Fatalf("Open of a missing nested directory: %v", err)
	}
	if info, err := os.Stat(nested); err != nil || !info.IsDir() {
		t.Fatalf("Open did not create %s: %v", nested, err)
	}
	// Opening the same directory again is the normal restart path.
	if _, err := Open(db, nested, p); err != nil {
		t.Fatalf("second Open of the same directory: %v", err)
	}
}

func TestOpenRemovesStaleTempFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "accounts")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(dir, tempPrefix+"0123456789abcdef"+tempSuffix)
	if err := os.WriteFile(stale, []byte("short"), 0o600); err != nil {
		t.Fatal(err)
	}
	unrelated := filepath.Join(dir, "unrelated.tmp")
	if err := os.WriteFile(unrelated, []byte("not a candidate name"), 0o600); err != nil {
		t.Fatal(err)
	}
	openTestStore(t, dbtest.Open(t), dir, newFakeProtector(t))
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Fatalf("Open kept a torn candidate: %v", err)
	}
	if _, err := os.Stat(unrelated); err != nil {
		t.Fatalf("Open removed a file it does not own: %v", err)
	}
}

func TestMaskPhone(t *testing.T) {
	got, err := MaskPhone("13800000000")
	if err != nil {
		t.Fatalf("MaskPhone: %v", err)
	}
	if got != "138****0000" {
		t.Fatalf("MaskPhone = %q", got)
	}
	if _, err := MaskPhone("1380000"); err == nil {
		t.Fatal("MaskPhone accepted a number that is too short")
	}
	if _, err := MaskPhone("23800000000"); err == nil {
		t.Fatal("MaskPhone accepted a number that is not a mobile number")
	}
}
