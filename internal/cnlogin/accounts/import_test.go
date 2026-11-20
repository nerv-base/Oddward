// SPDX-License-Identifier: GPL-3.0-or-later

package accounts

import (
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/database"
)

// rowSnapshot renders every account row so two imports can be compared without
// depending on column order.
func rowSnapshot(t *testing.T, s *Store) string {
	t.Helper()
	rows, err := s.db.Query(t.Context(), `SELECT id, label, identity_key, saved_at, hex(record) FROM accounts ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var b strings.Builder
	for rows.Next() {
		var id, label, identity, record string
		var saved int64
		if err := rows.Scan(&id, &label, &identity, &saved, &record); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "%s|%s|%s|%d|%s\n", id, label, identity, saved, record)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func TestImportLegacyMigrationParity(t *testing.T) {
	p := newFakeProtector(t)
	l := newLegacyDir(t, p)
	secA, secB := sampleSecret(1), sampleSecret(2)
	savedA := time.Date(2026, 3, 4, 5, 6, 7, 8, time.UTC)
	savedB := time.Date(2026, 3, 5, 5, 6, 7, 8, time.UTC)
	idA, idB := strings.Repeat("a", 32), strings.Repeat("b", 32)
	l.account(idA, "主号", savedA, secA)
	l.account(idB, "小号", savedB, secB)
	l.tajiduo(idA, sampleGrant(1))
	l.last(idB)
	l.notice(3)

	s := l.store()
	if err := s.ImportLegacy(t.Context()); err != nil {
		t.Fatalf("ImportLegacy: %v", err)
	}
	list, corrupt := mustList(t, s)
	if corrupt != 0 || len(list) != 2 {
		t.Fatalf("List = %+v (corrupt %d), want the two imported accounts", list, corrupt)
	}
	// The order is what the file store returned: newest saved-at first.
	if list[0].ID != idB || list[0].Label != "小号" || !list[0].SavedAt.Equal(savedB) {
		t.Fatalf("newest account = %+v", list[0])
	}
	if list[1].ID != idA || list[1].Label != "主号" || !list[1].SavedAt.Equal(savedA) {
		t.Fatalf("oldest account = %+v", list[1])
	}
	if !list[1].TajiduoLinked || list[1].TajiduoState != "linked" {
		t.Fatalf("linked account reported %+v", list[1])
	}
	if list[0].TajiduoLinked || list[0].TajiduoState != "" {
		t.Fatalf("unlinked account reported %+v", list[0])
	}
	// The imported ciphertext is the file's, so it still decrypts to the same
	// credentials: device id, channel uid and token are usable.
	if got, err := s.Unlock(idA); err != nil || got != secA {
		t.Fatalf("Unlock(%s) = %+v, %v", idA, got, err)
	}
	if got, err := s.Unlock(idB); err != nil || got != secB {
		t.Fatalf("Unlock(%s) = %+v, %v", idB, got, err)
	}
	grant, err := s.UnlockTajiduo(idA)
	if err != nil {
		t.Fatalf("UnlockTajiduo: %v", err)
	}
	if grant != sampleGrant(1) {
		t.Fatalf("UnlockTajiduo = %+v, want the linked grant", grant)
	}
	if grant.DeviceID == "" || grant.CenterUID != "center-1" || grant.RefreshToken != "refresh-1-SECRETVALUE" {
		t.Fatalf("imported grant is not usable: device %q center %q", grant.DeviceID, grant.CenterUID)
	}
	if id, err := s.Last(); err != nil || id != idB {
		t.Fatalf("Last = %q, %v; want the remembered %q", id, err, idB)
	}
	if ack, err := s.NoticeAcknowledged(3); err != nil || !ack {
		t.Fatalf("revision 3 acknowledged = %v, %v", ack, err)
	}
	if ack, _ := s.NoticeAcknowledged(2); ack {
		t.Fatal("an unacknowledged revision was reported as acknowledged")
	}
	if n := countRows(t, s, `SELECT count(*) FROM accounts`); n != 2 {
		t.Fatalf("imported %d accounts, want 2", n)
	}
	if n := countRows(t, s, `SELECT count(*) FROM account_tajiduo`); n != 1 {
		t.Fatalf("imported %d attachments, want 1", n)
	}
	// The identity key is the one the file carries, derived from the imported
	// subject key: a save in this build recognises the account without decrypting.
	var identity string
	if err := s.db.QueryRow(t.Context(), `SELECT identity_key FROM accounts WHERE id = ?`, idA).Scan(&identity); err != nil {
		t.Fatal(err)
	}
	if want := identityKeyFor(l.key, secA.ChannelUID); identity != want {
		t.Fatalf("identity key = %q, want the file's %q", identity, want)
	}
	// Importing publishes the files as mirrors of the rows: same bytes.
	mirror, err := os.ReadFile(recordPath(l.dir, idA))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(mirror, rowBytes(t, s, idA)) {
		t.Fatal("the imported record is not mirrored byte for byte")
	}
	record, found, err := s.db.Mirror(t.Context(), accountsDomain)
	if err != nil || !found {
		t.Fatalf("no mirror digest recorded: found=%v err=%v", found, err)
	}
	if record.Path != s.mirrorPath(mirrorIndexName) {
		t.Fatalf("mirror path = %q, want the index", record.Path)
	}
	state, found, err := s.db.MigrationState(t.Context(), accountsDomain)
	if err != nil || !found {
		t.Fatalf("no migration state: found=%v err=%v", found, err)
	}
	if state.Status != database.MigrationDone || state.Version != legacyVersion {
		t.Fatalf("migration state = %+v", state)
	}
}

func TestImportLegacyIsIdempotent(t *testing.T) {
	l := newLegacyDir(t, newFakeProtector(t))
	l.account(strings.Repeat("a", 32), "A", time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC), sampleSecret(1))
	l.account(strings.Repeat("b", 32), "B", time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC), sampleSecret(2))
	s := l.store()
	if err := s.ImportLegacy(t.Context()); err != nil {
		t.Fatal(err)
	}
	first := rowSnapshot(t, s)
	if err := s.ImportLegacy(t.Context()); err != nil {
		t.Fatalf("second ImportLegacy: %v", err)
	}
	if second := rowSnapshot(t, s); second != first {
		t.Fatalf("a second import changed the rows:\n%s\nwant:\n%s", second, first)
	}
	if n := countRows(t, s, `SELECT count(*) FROM accounts`); n != 2 {
		t.Fatalf("a second import left %d rows, want 2", n)
	}
	// A completed import with untouched files is skipped entirely, not replayed.
	if err := s.ImportLegacy(t.Context()); err != nil {
		t.Fatal(err)
	}
	if last := rowSnapshot(t, s); last != first {
		t.Fatal("a replayed import changed the rows")
	}
}

func TestImportLegacyReconcilesRewrite(t *testing.T) {
	p := newFakeProtector(t)
	l := newLegacyDir(t, p)
	sec := sampleSecret(1)
	id := strings.Repeat("a", 32)
	saved := time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC)
	l.account(id, "before rollback", saved, sec)
	s := l.store()
	if err := s.ImportLegacy(t.Context()); err != nil {
		t.Fatal(err)
	}
	// The launcher was rolled back, the older build renamed the account, and this
	// build starts again: the relationship the files describe is the new one.
	l.account(id, "after rollback", saved, sec)
	if err := s.ImportLegacy(t.Context()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	list, _ := mustList(t, s)
	if len(list) != 1 || list[0].Label != "after rollback" {
		t.Fatalf("List after reconcile = %+v", list)
	}
	if got, err := s.Unlock(id); err != nil || got != sec {
		t.Fatalf("the reconciled record does not decrypt: %+v, %v", got, err)
	}
}

func TestImportLegacyReconcilesRemoval(t *testing.T) {
	l := newLegacyDir(t, newFakeProtector(t))
	idA, idB := strings.Repeat("a", 32), strings.Repeat("b", 32)
	l.account(idA, "A", time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC), sampleSecret(1))
	l.account(idB, "B", time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC), sampleSecret(2))
	s := l.store()
	if err := s.ImportLegacy(t.Context()); err != nil {
		t.Fatal(err)
	}
	// The older build deleted the second account; the files are the whole set.
	if err := os.Remove(recordPath(l.dir, idB)); err != nil {
		t.Fatal(err)
	}
	if err := s.ImportLegacy(t.Context()); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	list, _ := mustList(t, s)
	if len(list) != 1 || list[0].ID != idA {
		t.Fatalf("List after reconcile = %+v, want only %s", list, idA)
	}
	if _, err := s.Unlock(idB); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Unlock of the removed account = %v, want ErrNotFound", err)
	}
}

func TestImportLegacyCorruptRecord(t *testing.T) {
	l := newLegacyDir(t, newFakeProtector(t))
	idA, idB := strings.Repeat("a", 32), strings.Repeat("b", 32)
	l.account(idA, "A", time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC), sampleSecret(1))
	s := l.store()
	if err := s.ImportLegacy(t.Context()); err != nil {
		t.Fatal(err)
	}
	before := rowSnapshot(t, s)
	corrupt := []byte(`{"version":1,"id":"broken"`)
	path := recordPath(l.dir, idB)
	if err := os.WriteFile(path, corrupt, 0o600); err != nil {
		t.Fatal(err)
	}
	err := s.ImportLegacy(t.Context())
	if err == nil {
		t.Fatal("an import with an unparsable record succeeded")
	}
	if !strings.Contains(err.Error(), idB+recordExt) {
		t.Fatalf("import error does not name the file: %v", err)
	}
	if !errors.Is(err, ErrCorrupt) {
		t.Fatalf("import error = %v, want ErrCorrupt", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, corrupt) {
		t.Fatal("a failed import rewrote the file")
	}
	if now := rowSnapshot(t, s); now != before {
		t.Fatalf("a failed import changed the rows:\n%s\nwant:\n%s", now, before)
	}
}

func TestImportLegacyCorruptRecordOnFirstImport(t *testing.T) {
	l := newLegacyDir(t, newFakeProtector(t))
	id := strings.Repeat("a", 32)
	l.write(id+recordExt, []byte("garbage record"))
	err := l.store().ImportLegacy(t.Context())
	if err == nil || !strings.Contains(err.Error(), id+recordExt) {
		t.Fatalf("import error = %v, want it to name %s", err, id+recordExt)
	}
}

func TestImportLegacyOrphanAttachment(t *testing.T) {
	l := newLegacyDir(t, newFakeProtector(t))
	orphan := strings.Repeat("c", 32)
	raw, err := l.s.sealTajiduo(orphan, sampleGrant(1))
	if err != nil {
		t.Fatal(err)
	}
	l.write(orphan+tajiduoExt, raw)
	err = l.store().ImportLegacy(t.Context())
	if err == nil {
		t.Fatal("an import with an attachment beside no record succeeded")
	}
	if !strings.Contains(err.Error(), orphan+tajiduoExt) || !errors.Is(err, ErrCorrupt) {
		t.Fatalf("import error = %v, want ErrCorrupt naming %s", err, orphan+tajiduoExt)
	}
	if n := countRows(t, l.store(), `SELECT count(*) FROM accounts`); n != 0 {
		t.Fatalf("a failed import wrote %d accounts", n)
	}
}

func TestImportLegacyKeepsDamagedAttachment(t *testing.T) {
	l := newLegacyDir(t, newFakeProtector(t))
	id := strings.Repeat("a", 32)
	l.account(id, "A", time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC), sampleSecret(1))
	l.write(id+tajiduoExt, []byte("garbage attachment"))
	s := l.store()
	if err := s.ImportLegacy(t.Context()); err != nil {
		t.Fatalf("ImportLegacy: %v", err)
	}
	// The link existed, so the row keeps it and reports that it needs a new one;
	// dropping the row would hide that the user had linked.
	list, _ := mustList(t, s)
	if len(list) != 1 || list[0].TajiduoLinked || list[0].TajiduoState != "needs-relink" {
		t.Fatalf("List = %+v, want needs-relink", list)
	}
	if _, err := s.UnlockTajiduo(id); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("UnlockTajiduo = %v, want ErrCorrupt", err)
	}
}

func TestImportLegacyMissingDirectory(t *testing.T) {
	l := newLegacyDir(t, newFakeProtector(t))
	l.account(strings.Repeat("a", 32), "A", time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC), sampleSecret(1))
	s := l.store()
	if err := os.RemoveAll(l.dir); err != nil {
		t.Fatal(err)
	}
	if err := s.ImportLegacy(t.Context()); err != nil {
		t.Fatalf("ImportLegacy without a directory = %v, want nil", err)
	}
	if n := countRows(t, s, `SELECT count(*) FROM accounts`); n != 0 {
		t.Fatalf("an import without a directory wrote %d accounts", n)
	}
}

func TestImportLegacyEmptyDirectoryKeepsRows(t *testing.T) {
	s, _, _ := newTestStore(t)
	mustSave(t, s, "A", sampleSecret(1))
	if err := s.ImportLegacy(t.Context()); err != nil {
		t.Fatalf("ImportLegacy of an empty directory = %v", err)
	}
	if n := countRows(t, s, `SELECT count(*) FROM accounts`); n != 1 {
		t.Fatalf("an import of nothing dropped rows: %d left", n)
	}
	list, _ := mustList(t, s)
	if _, err := s.Unlock(list[0].ID); err != nil {
		t.Fatalf("the surviving account does not decrypt: %v", err)
	}
}

func TestImportLegacyDuplicateIdentity(t *testing.T) {
	l := newLegacyDir(t, newFakeProtector(t))
	sec := sampleSecret(1)
	idA, idB := strings.Repeat("a", 32), strings.Repeat("b", 32)
	l.account(idA, "A", time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC), sec)
	// The same game login under a second id: older builds could produce this by
	// copying a file, and importing both would make dedupe ambiguous.
	l.account(idB, "B", time.Date(2026, 3, 5, 0, 0, 0, 0, time.UTC), sec)
	err := l.store().ImportLegacy(t.Context())
	if err == nil {
		t.Fatal("an import with two records for one identity succeeded")
	}
	if !errors.Is(err, ErrCorrupt) || !strings.Contains(err.Error(), "same channel identity") {
		t.Fatalf("import error = %v, want ErrCorrupt about the duplicate identity", err)
	}
}

func TestImportLegacyStateDefaults(t *testing.T) {
	l := newLegacyDir(t, newFakeProtector(t))
	id := strings.Repeat("a", 32)
	l.account(id, "A", time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC), sampleSecret(1))
	// A directory written before the state files existed has no memory of a
	// choice, so the row must not claim one.
	if err := os.Remove(filepath.Join(l.dir, subjectFileName)); err != nil {
		t.Fatal(err)
	}
	s := l.store()
	if err := s.ImportLegacy(t.Context()); err != nil {
		t.Fatal(err)
	}
	var last sql.NullString
	var notice int64
	if err := s.db.QueryRow(t.Context(), `SELECT last_account, notice_revision FROM account_state WHERE id = 1`).Scan(&last, &notice); err != nil {
		t.Fatal(err)
	}
	if last.Valid {
		t.Fatalf("last account = %q, want NULL (never chosen)", last.String)
	}
	if notice != 0 {
		t.Fatalf("notice revision = %d, want 0", notice)
	}
	// A lone account with no recorded choice is the file store's fallback.
	if got, err := s.Last(); err != nil || got != id {
		t.Fatalf("Last = %q, %v; want %q", got, err, id)
	}
}

func TestImportLegacyUnusableLastChoice(t *testing.T) {
	l := newLegacyDir(t, newFakeProtector(t))
	l.account(strings.Repeat("a", 32), "A", time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC), sampleSecret(1))
	l.write(lastFileName, []byte("not a protected document"))
	s := l.store()
	if err := s.ImportLegacy(t.Context()); err != nil {
		t.Fatal(err)
	}
	// The file records that a choice was made, so the account is not promoted:
	// "explicitly none" and "never chosen" are different states.
	if got, err := s.Last(); err != nil || got != "" {
		t.Fatalf("Last = %q, %v; want an explicit none", got, err)
	}
}

func TestImportLegacyKeepsStateForMissingFiles(t *testing.T) {
	l := newLegacyDir(t, newFakeProtector(t))
	id := strings.Repeat("a", 32)
	l.account(id, "A", time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC), sampleSecret(1))
	s := l.store()
	if err := s.ImportLegacy(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.AcceptNotice(4); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLast(id); err != nil {
		t.Fatal(err)
	}
	// A reconcile with the state files deleted must not forget what this build
	// recorded: a missing file means "nothing to import", not "reset".
	for _, name := range []string{noticeFileName, lastFileName} {
		if err := os.Remove(filepath.Join(l.dir, name)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	l.account(id, "renamed", time.Date(2026, 3, 4, 0, 0, 0, 0, time.UTC), sampleSecret(1))
	if err := s.ImportLegacy(t.Context()); err != nil {
		t.Fatal(err)
	}
	if ack, err := s.NoticeAcknowledged(4); err != nil || !ack {
		t.Fatalf("revision 4 acknowledged after reconcile = %v, %v", ack, err)
	}
	if got, err := s.Last(); err != nil || got != id {
		t.Fatalf("Last after reconcile = %q, %v; want %q", got, err, id)
	}
}

func TestIdentityKeyUniqueIndex(t *testing.T) {
	s, _, _ := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	var identity string
	if err := s.db.QueryRow(t.Context(), `SELECT identity_key FROM accounts WHERE id = ?`, a.ID).Scan(&identity); err != nil {
		t.Fatal(err)
	}
	// The dedupe of this store relies on the schema, so a second row for the same
	// identity is a constraint error no caller can bypass.
	_, err := s.db.Exec(t.Context(), `
		INSERT INTO accounts(id, label, identity_key, saved_at, protection, record, updated_at)
		VALUES(?, 'x', ?, ?, ?, x'00', ?)`,
		strings.Repeat("e", 32), identity, database.Stamp(time.Unix(0, 0)), protectionKind, database.Stamp(time.Unix(0, 0)))
	if err == nil {
		t.Fatal("the database accepted two rows with one identity key")
	}
	if !strings.Contains(err.Error(), "UNIQUE") {
		t.Fatalf("constraint error = %v, want a UNIQUE violation", err)
	}
}
