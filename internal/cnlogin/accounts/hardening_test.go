// SPDX-License-Identifier: GPL-3.0-or-later

package accounts

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nerv-base/Oddward/internal/database/dbtest"
)

func TestLastMirrorIsProtectedAndPurposeBound(t *testing.T) {
	s, dir, p := newTestStore(t)
	a := mustSave(t, s, "A", sampleSecret(1))
	if err := s.SetLast(a.ID); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, lastFileName))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(a.ID)) {
		t.Fatal("the last-account mirror stores the account id in plaintext")
	}
	plain, err := p.Unprotect(raw)
	if err != nil {
		t.Fatal("the last-account mirror is not protector-wrapped")
	}
	var doc lastState
	decodeErr := decodeState(plain, lastPurpose, &doc)
	wipeBytes(plain)
	if decodeErr != nil {
		t.Fatalf("last-account mirror does not decode as last-login state: %v", decodeErr)
	}
	if doc.ID != a.ID {
		t.Fatalf("mirror names %q, want %q", doc.ID, a.ID)
	}
	// The purpose field is what keeps one protected document from being read as
	// another, even under the same user's protector.
	if err := decodeState([]byte(fmt.Sprintf(`{"version":%d,"purpose":%q,"id":""}`, stateVersion, lastPurpose)), subjectPurpose, &lastState{}); err == nil {
		t.Fatal("a last-login document decoded as the subject key")
	}
	// Another Windows user cannot unwrap the mirror.
	other := openTestStore(t, s.db, dir, newFakeProtector(t))
	if _, err := other.p.Unprotect(raw); err == nil {
		t.Fatal("another user could unwrap the last-account mirror")
	}
	// Forgetting is recorded as protected state too, never inferred from absence.
	if err := s.SetLast(""); err != nil {
		t.Fatal(err)
	}
	if raw, err := os.ReadFile(filepath.Join(dir, lastFileName)); err != nil || len(raw) == 0 {
		t.Fatalf("an explicit forget must also write protected state: %v", err)
	}
}

type countingProtector struct {
	KeyProtector
	opens int
}

func (p *countingProtector) Unprotect(b []byte) ([]byte, error) {
	p.opens++
	return p.KeyProtector.Unprotect(b)
}

func TestDedupeDoesNotUnlockUnrelatedAccounts(t *testing.T) {
	p := &countingProtector{KeyProtector: newFakeProtector(t)}
	dir := filepath.Join(t.TempDir(), "accounts")
	db := dbtest.Open(t)
	s := openTestStore(t, db, dir, p)
	var a Account
	for i := 0; i < 12; i++ {
		a = mustSave(t, s, "A", sampleSecret(i))
	}
	p.opens = 0
	s2 := openTestStore(t, db, dir, p)
	saved := mustSave(t, s2, "", sampleSecret(11))
	if saved.ID != a.ID {
		t.Fatal("the subject key was not stable across a reopen")
	}
	// The identity key is an HMAC, so recognising a repeat costs one unwrap for
	// the subject key and one round-trip check of the new data key, not one
	// unlock per stored account.
	if p.opens > 4 {
		t.Fatalf("dedupe unwrapped %d keys; it must not decrypt every account", p.opens)
	}
	if _, err := os.Stat(filepath.Join(dir, subjectFileName)); err != nil {
		t.Fatal("no protected stable subject key")
	}
}

func TestSecretProtocolByteLimits(t *testing.T) {
	for _, test := range []struct{ uid, token int }{{64, 1}, {1, 128}} {
		sec := sampleSecret(1)
		sec.ChannelUID = strings.Repeat("u", test.uid)
		sec.ChannelToken = strings.Repeat("t", test.token)
		if err := sec.validate(); !errors.Is(err, ErrInvalidSecret) {
			t.Fatal("accepted a credential exceeding the SDK field bounds")
		}
	}
	sec := sampleSecret(1)
	sec.ChannelUID = "   "
	if err := sec.validate(); !errors.Is(err, ErrInvalidSecret) {
		t.Fatal("accepted a blank channel uid")
	}
}

func TestSecretAllFormattingVerbsRedact(t *testing.T) {
	sec := sampleSecret(1)
	for _, verb := range []string{"%x", "%X", "%q", "%#v", "%+v"} {
		out := fmt.Sprintf(verb, sec)
		if strings.Contains(out, sec.ChannelToken) || strings.Contains(out, fmt.Sprintf("%x", sec.ChannelToken)) {
			t.Fatalf("secret formatting %s leaks the credential", verb)
		}
	}
}

func TestOpenPreservesUnknownPendingFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "accounts")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, tempPrefix+"0123456789abcdef"+tempSuffix)
	if err := os.WriteFile(path, []byte("not owned by this transaction"), 0o600); err != nil {
		t.Fatal(err)
	}
	openTestStore(t, dbtest.Open(t), dir, newFakeProtector(t))
	if _, err := os.Stat(path); err != nil {
		t.Fatal("Open deleted an unrecognized file based only on its name")
	}
}

func TestNoticeRevisionIsProtected(t *testing.T) {
	s, dir, p := newTestStore(t)
	if ack, err := s.NoticeAcknowledged(2); err != nil || ack {
		t.Fatalf("fresh store acknowledged revision 2: %v, %v", ack, err)
	}
	if _, err := s.NoticeAcknowledged(0); !errors.Is(err, ErrInvalidNotice) {
		t.Fatalf("NoticeAcknowledged(0) = %v, want ErrInvalidNotice", err)
	}
	if err := s.AcceptNotice(0); !errors.Is(err, ErrInvalidNotice) {
		t.Fatalf("AcceptNotice(0) = %v, want ErrInvalidNotice", err)
	}
	if err := s.AcceptNotice(2); err != nil {
		t.Fatalf("AcceptNotice: %v", err)
	}
	if ack, err := s.NoticeAcknowledged(2); err != nil || !ack {
		t.Fatalf("revision 2 not acknowledged: %v, %v", ack, err)
	}
	if ack, _ := s.NoticeAcknowledged(3); ack {
		t.Fatal("a different revision was reported as acknowledged")
	}
	var stored int64
	if err := s.db.QueryRow(t.Context(), `SELECT notice_revision FROM account_state WHERE id = 1`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 2 {
		t.Fatalf("stored revision = %d, want 2", stored)
	}
	raw, err := os.ReadFile(filepath.Join(dir, noticeFileName))
	if err != nil {
		t.Fatalf("notice mirror: %v", err)
	}
	if len(raw) > maxNoticeSize {
		t.Fatalf("notice mirror is %d bytes, want at most %d", len(raw), maxNoticeSize)
	}
	plain, err := p.Unprotect(raw)
	if err != nil {
		t.Fatal("the notice mirror is not protector-wrapped")
	}
	wipeBytes(plain)
	if list, _ := mustList(t, s); len(list) != 0 {
		t.Fatal("accepting a notice created an account")
	}
}
