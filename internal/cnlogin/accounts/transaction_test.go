// SPDX-License-Identifier: GPL-3.0-or-later

package accounts

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The staged save is the security property the launcher relies on: the account
// is encrypted and durable before the credential is delivered, but nothing is
// stored until it actually was.

func TestPrepareRollbackLeavesNothing(t *testing.T) {
	s, dir, _ := newTestStore(t)
	tx, err := s.Prepare(Account{Label: "A"}, sampleSecret(1))
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if n := countRows(t, s, `SELECT count(*) FROM accounts`); n != 0 {
		t.Fatalf("rolled back save left %d rows", n)
	}
	if n := countRows(t, s, `SELECT count(*) FROM account_state`); n != 0 {
		t.Fatalf("rolled back save left %d state rows", n)
	}
	if names := dirNames(t, dir); len(names) != 0 {
		t.Fatalf("rolled back save left files: %v", names)
	}
	if records, err := s.db.Mirrors(t.Context(), accountsDomain); err != nil || len(records) != 0 {
		t.Fatalf("rolled back save recorded a mirror digest: records=%v err=%v", records, err)
	}
	if err := tx.Commit(); err == nil {
		t.Fatal("a rolled back save committed")
	}
}

func TestPrepareCommitPersistsRowAndMirror(t *testing.T) {
	s, dir, _ := newTestStore(t)
	sec := sampleSecret(1)
	tx, err := s.Prepare(Account{Label: "A"}, sec)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	id := tx.Account().ID
	if list, _ := mustList(t, s); len(list) != 0 {
		t.Fatal("a staged account is already visible")
	}
	if !hasTempFile(t, dir) {
		t.Fatal("Prepare returned before writing the durable encrypted candidate")
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got, err := s.Unlock(id); err != nil || got != sec {
		t.Fatalf("Unlock after Commit = %+v, %v", got, err)
	}
	mirror, err := os.ReadFile(recordPath(dir, id))
	if err != nil {
		t.Fatalf("mirror file: %v", err)
	}
	if !bytes.Equal(mirror, rowBytes(t, s, id)) {
		t.Fatal("the mirror is not the committed record bytes")
	}
	// The mirrored bytes are the canonical record, so they verify and decrypt
	// exactly like the row does.
	r, err := parseRecord(id, mirror)
	if err != nil {
		t.Fatalf("mirror does not parse as a record: %v", err)
	}
	_, plain, err := s.open(r)
	if err != nil {
		t.Fatalf("mirror does not decrypt: %v", err)
	}
	back, err := decodePayload(plain)
	wipeBytes(plain)
	if err != nil || back != sec {
		t.Fatalf("mirror decrypts to %+v, %v", back, err)
	}
	assertNoTempFiles(t, dir)
}

func TestPreparedCommitIsIdempotent(t *testing.T) {
	s, dir, _ := newTestStore(t)
	tx, err := s.Prepare(Account{Label: "A"}, sampleSecret(1))
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("second Commit = %v, want nil", err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback after Commit = %v, want nil", err)
	}
	if n := countRows(t, s, `SELECT count(*) FROM accounts`); n != 1 {
		t.Fatalf("account count = %d, want 1", n)
	}
	rolled, err := s.Prepare(Account{Label: "B"}, sampleSecret(2))
	if err != nil {
		t.Fatal(err)
	}
	if err := rolled.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := rolled.Commit(); err == nil {
		t.Fatal("a rolled back save committed")
	}
	if n := countRows(t, s, `SELECT count(*) FROM accounts`); n != 1 {
		t.Fatalf("account count = %d, want 1", n)
	}
	assertNoTempFiles(t, dir)
}

func TestPreparedRefreshRollbackPreservesCommittedBytes(t *testing.T) {
	s, dir, _ := newTestStore(t)
	original := sampleSecret(1)
	a := mustSave(t, s, "personal label", original)
	before := rowBytes(t, s, a.ID)
	mirrorBefore, err := os.ReadFile(recordPath(dir, a.ID))
	if err != nil {
		t.Fatal(err)
	}
	next := original
	next.ChannelToken = "replacement-token"
	tx, err := s.Prepare(Account{Label: "138****0000"}, next)
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if tx.Account().ID != a.ID || tx.Account().Label != a.Label {
		t.Fatal("a refresh lost the account identity or its custom label")
	}
	if !bytes.Equal(before, rowBytes(t, s, a.ID)) {
		t.Fatal("Prepare published the replacement before delivery")
	}
	if mirror, err := os.ReadFile(recordPath(dir, a.ID)); err != nil || !bytes.Equal(mirror, mirrorBefore) {
		t.Fatalf("Prepare rewrote the mirror before delivery: %v", err)
	}
	if got, err := s.Unlock(a.ID); err != nil || got != original {
		t.Fatal("the old account is unavailable before delivery")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatalf("Rollback: %v", err)
	}
	if !bytes.Equal(before, rowBytes(t, s, a.ID)) {
		t.Fatal("a failed delivery changed the stored account")
	}
	if mirror, err := os.ReadFile(recordPath(dir, a.ID)); err != nil || !bytes.Equal(mirror, mirrorBefore) {
		t.Fatalf("a failed delivery changed the mirror: %v", err)
	}
	assertNoTempFiles(t, dir)
}

func TestPreparedRefreshCommitReplacesSecret(t *testing.T) {
	s, dir, _ := newTestStore(t)
	original := sampleSecret(1)
	a := mustSave(t, s, "label", original)
	next := original
	next.ChannelToken = "rotated-token"
	tx, err := s.Prepare(Account{}, next)
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if got, err := s.Unlock(a.ID); err != nil || got != next {
		t.Fatalf("Unlock after a committed refresh = %+v, %v", got, err)
	}
	list, _ := mustList(t, s)
	if len(list) != 1 || list[0].ID != a.ID {
		t.Fatalf("a refresh created a second account: %+v", list)
	}
	mirror, err := os.ReadFile(recordPath(dir, a.ID))
	if err != nil || !bytes.Equal(mirror, rowBytes(t, s, a.ID)) {
		t.Fatalf("the mirror was not replaced with the rotated record: %v", err)
	}
}

func hasTempFile(t *testing.T, dir string) bool {
	t.Helper()
	for _, name := range dirNames(t, dir) {
		if strings.HasSuffix(name, tempSuffix) {
			return true
		}
	}
	return false
}

// The subject key only exists to derive identity keys for the second and later
// saves. Staging such a save must not create it: the save may never be delivered.
func TestStagedSaveDoesNotStoreSubjectKey(t *testing.T) {
	s, dir, _ := newTestStore(t)
	first := mustSave(t, s, "first", sampleSecret(1))
	if _, err := os.Lstat(filepath.Join(dir, subjectFileName)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the first save created a subject key: %v", err)
	}
	for attempt := 1; attempt <= 2; attempt++ {
		tx, err := s.Prepare(Account{Label: "second"}, sampleSecret(2))
		if err != nil {
			t.Fatalf("Prepare: %v", err)
		}
		if attempt == 1 {
			if err := tx.Rollback(); err != nil {
				t.Fatalf("Rollback: %v", err)
			}
			if n := countRows(t, s, `SELECT count(*) FROM account_state WHERE subject_key IS NOT NULL`); n != 0 {
				t.Fatalf("a rolled back save stored a subject key")
			}
			if _, err := os.Lstat(filepath.Join(dir, subjectFileName)); !errors.Is(err, fs.ErrNotExist) {
				t.Fatalf("a rolled back save wrote the subject key mirror: %v", err)
			}
			continue
		}
		if err := tx.Commit(); err != nil {
			t.Fatalf("Commit: %v", err)
		}
		key := subjectKeyOf(t, s)
		if got, want := identityOf(t, s, tx.Account().ID), identityKeyFor(key, sampleSecret(2).ChannelUID); got != want {
			t.Fatalf("stored identity key = %q, want %q", got, want)
		}
	}
	// The account written before the key existed stays readable: it never had an
	// identity key, and its ciphertext does not depend on one.
	if got, err := s.Unlock(first.ID); err != nil || got != sampleSecret(1) {
		t.Fatalf("the keyless account no longer unlocks: %+v, %v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, subjectFileName)); err != nil {
		t.Fatalf("the committed save did not mirror the subject key: %v", err)
	}
	assertNoTempFiles(t, dir)
}
