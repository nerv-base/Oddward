// SPDX-License-Identifier: GPL-3.0-or-later

package accounts

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/database"
	"github.com/nerv-base/Oddward/internal/database/dbtest"
)

// fakeProtector wraps data keys with AES-GCM under its own random key, so two
// fakes behave like two different Windows users: neither can unwrap the
// other's keys.
type fakeProtector struct {
	aead cipher.AEAD
	fail error
}

func newFakeProtector(t testing.TB) *fakeProtector {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatal(err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		t.Fatal(err)
	}
	return &fakeProtector{aead: aead}
}

func (p *fakeProtector) Protect(plain []byte) ([]byte, error) {
	if p.fail != nil {
		return nil, p.fail
	}
	nonce := make([]byte, p.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	return p.aead.Seal(nonce, nonce, plain, []byte("fake-protector")), nil
}

func (p *fakeProtector) Unprotect(wrapped []byte) ([]byte, error) {
	if p.fail != nil {
		return nil, p.fail
	}
	n := p.aead.NonceSize()
	if len(wrapped) < n {
		return nil, errors.New("fake protector: short input")
	}
	return p.aead.Open(nil, wrapped[:n], wrapped[n:], []byte("fake-protector"))
}

// testClock hands out strictly increasing times so ordering is deterministic.
type testClock struct{ t time.Time }

func (c *testClock) now() time.Time {
	c.t = c.t.Add(time.Second)
	return c.t
}

func openTestStore(t testing.TB, db *database.DB, dir string, p KeyProtector) *Store {
	t.Helper()
	s, err := Open(db, dir, p)
	if err != nil {
		t.Fatalf("Open(%q): %v", dir, err)
	}
	clock := &testClock{t: time.Date(2026, 11, 13, 8, 0, 0, 123456789, time.UTC)}
	s.now = clock.now
	return s
}

func newTestStore(t testing.TB) (*Store, string, *fakeProtector) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "accounts")
	p := newFakeProtector(t)
	return openTestStore(t, dbtest.Open(t), dir, p), dir, p
}

func sampleSecret(n int) Secret {
	return Secret{
		DeviceID:     fmt.Sprintf("%032x", 0xabc000+n),
		ChannelUID:   fmt.Sprintf("uid-%d-7788", n),
		ChannelToken: fmt.Sprintf("token-%d-SECRETVALUE", n),
	}
}

func mustSave(t testing.TB, s *Store, label string, sec Secret) Account {
	t.Helper()
	a, err := s.Save(Account{Label: label}, sec)
	if err != nil {
		t.Fatalf("Save(%q): %v", label, err)
	}
	return a
}

func mustList(t testing.TB, s *Store) ([]Account, int) {
	t.Helper()
	list, corrupt, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return list, corrupt
}

func recordPath(dir, id string) string { return filepath.Join(dir, id+".acct") }

// tamperRecord decodes the stored record blob, lets edit change it and writes it
// back to the row. It is what editing the file used to be: the bytes below the
// public fields no longer verify.
func tamperRecord(t *testing.T, s *Store, id string, edit func(map[string]any)) {
	t.Helper()
	raw := rowBytes(t, s, id)
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	edit(m)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.Exec(t.Context(), `UPDATE accounts SET record = ? WHERE id = ?`, out, id); err != nil {
		t.Fatal(err)
	}
}

// rowBytes reads one stored record blob.
func rowBytes(t *testing.T, s *Store, id string) []byte {
	t.Helper()
	var raw []byte
	if err := s.db.QueryRow(t.Context(), `SELECT record FROM accounts WHERE id = ?`, id).Scan(&raw); err != nil {
		t.Fatalf("read record row %s: %v", id, err)
	}
	return raw
}

// insertAccount writes a row directly, which is how a stored record can become
// something List cannot parse.
func insertAccount(t *testing.T, s *Store, id string, raw []byte) {
	t.Helper()
	_, err := s.db.Exec(t.Context(), `
		INSERT INTO accounts(id, label, identity_key, saved_at, protection, record, updated_at)
		VALUES(?, 'x', ?, ?, ?, ?, ?)`,
		id, keylessIdentity(id), database.Stamp(time.Unix(0, 0)), protectionKind, raw, database.Stamp(time.Unix(0, 0)))
	if err != nil {
		t.Fatal(err)
	}
}

func countRows(t *testing.T, s *Store, query string) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(t.Context(), query).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

// subjectKeyOf unwraps the stored subject key.
func subjectKeyOf(t *testing.T, s *Store) []byte {
	t.Helper()
	var wrapped []byte
	if err := s.db.QueryRow(t.Context(), `SELECT subject_key FROM account_state WHERE id = 1`).Scan(&wrapped); err != nil {
		t.Fatalf("read subject key: %v", err)
	}
	plain, err := s.p.Unprotect(wrapped)
	if err != nil {
		t.Fatalf("unwrap subject key: %v", err)
	}
	defer wipeBytes(plain)
	var doc subjectState
	if err := decodeState(plain, subjectPurpose, &doc); err != nil {
		t.Fatalf("decode subject key: %v", err)
	}
	return doc.Key
}

// identityOf reads one row's identity_key column.
func identityOf(t *testing.T, s *Store, id string) string {
	t.Helper()
	var identity string
	if err := s.db.QueryRow(t.Context(), `SELECT identity_key FROM accounts WHERE id = ?`, id).Scan(&identity); err != nil {
		t.Fatalf("read identity key of %s: %v", id, err)
	}
	return identity
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func assertNoTempFiles(t *testing.T, dir string) {
	t.Helper()
	for _, name := range dirNames(t, dir) {
		if strings.HasSuffix(name, ".tmp") {
			t.Fatalf("temp file left behind: %s (dir: %v)", name, dirNames(t, dir))
		}
	}
}

// legacyDir is a directory the previous file-store build left behind. The files
// are produced by the same sealing functions that build called, so they are the
// layout the importer has to read instead of a re-encoding of it.
type legacyDir struct {
	t   *testing.T
	s   *Store
	dir string
	p   KeyProtector
	key []byte
}

func newLegacyDir(t *testing.T, p KeyProtector) *legacyDir {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "accounts")
	key := make([]byte, subjectKeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	l := &legacyDir{t: t, s: openTestStore(t, dbtest.Open(t), dir, p), dir: dir, p: p, key: key}
	l.write(subjectFileName, l.seal(subjectFileName, maxStateSize, subjectState{
		Version: stateVersion,
		Purpose: subjectPurpose,
		Key:     key,
	}))
	return l
}

// account writes the record file of one saved login, with the identity key the
// file store derived from its subject key.
func (l *legacyDir) account(id, label string, savedAt time.Time, sec Secret) {
	l.t.Helper()
	raw, err := l.s.sealNew(Account{ID: id, Label: label, SavedAt: savedAt},
		identityKeyFor(l.key, sec.ChannelUID), sec)
	if err != nil {
		l.t.Fatal(err)
	}
	l.write(id+recordExt, raw)
}

// tajiduo writes the attachment of one linked account.
func (l *legacyDir) tajiduo(id string, g TajiduoGrant) {
	l.t.Helper()
	raw, err := l.s.sealTajiduo(id, g)
	if err != nil {
		l.t.Fatal(err)
	}
	l.write(id+tajiduoExt, raw)
}

// last writes the remembered account, or the "none" marker for an empty id.
func (l *legacyDir) last(id string) {
	l.t.Helper()
	l.write(lastFileName, l.seal(lastFileName, maxStateSize, lastState{
		Version: stateVersion,
		Purpose: lastPurpose,
		ID:      id,
	}))
}

// notice writes the acknowledged notice revision.
func (l *legacyDir) notice(revision int) {
	l.t.Helper()
	l.write(noticeFileName, l.seal(noticeFileName, maxNoticeSize, noticeState{
		Version:         stateVersion,
		Purpose:         noticePurpose,
		Revision:        revision,
		AcknowledgedUtc: formatTime(time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)),
	}))
}

// seal protects one state document the way the file store wrote it.
func (l *legacyDir) seal(name string, limit int, doc any) []byte {
	l.t.Helper()
	raw, err := l.s.sealState(name, limit, doc)
	if err != nil {
		l.t.Fatal(err)
	}
	return raw
}

func (l *legacyDir) write(name string, raw []byte) {
	l.t.Helper()
	if err := os.WriteFile(filepath.Join(l.dir, name), raw, 0o600); err != nil {
		l.t.Fatal(err)
	}
}

// store opens a fresh database over the directory: the import has to rebuild
// every row from the files.
func (l *legacyDir) store() *Store {
	l.t.Helper()
	return openTestStore(l.t, dbtest.Open(l.t), l.dir, l.p)
}
