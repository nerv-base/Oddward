// SPDX-License-Identifier: GPL-3.0-or-later

package accounts

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type pendingSave interface {
	Account() Account
	Commit() error
	Rollback() error
}

// Reflection makes an absent API a behavioral assertion rather than a compile failure.
func prepareForTest(t *testing.T, s *Store, a Account, sec Secret) (pendingSave, error) {
	t.Helper()
	method := reflect.ValueOf(s).MethodByName("Prepare")
	if !method.IsValid() {
		t.Fatal("Store has no prepare-before-delivery operation")
	}
	out := method.Call([]reflect.Value{reflect.ValueOf(a), reflect.ValueOf(sec)})
	if !out[1].IsNil() {
		return nil, out[1].Interface().(error)
	}
	tx, ok := out[0].Interface().(pendingSave)
	if !ok {
		t.Fatal("Prepare does not return an account transaction")
	}
	return tx, nil
}

func TestPreparedRefreshRollbackPreservesCommittedBytes(t *testing.T) {
	s, dir, _ := newTestStore(t)
	original := sampleSecret(1)
	a := mustSave(t, s, "personal label", original)
	before, _ := os.ReadFile(recordPath(dir, a.ID))
	next := original
	next.ChannelToken = "replacement-token"
	tx, err := prepareForTest(t, s, Account{Label: "138****0000"}, next)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if tx.Account().ID != a.ID || tx.Account().Label != a.Label {
		t.Fatal("refresh lost account identity or custom label")
	}
	current, _ := os.ReadFile(recordPath(dir, a.ID))
	if !bytes.Equal(before, current) {
		t.Fatal("Prepare published replacement before delivery")
	}
	if got, err := s.Unlock(a.ID); err != nil || got != original {
		t.Fatal("old account unavailable before delivery")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	current, _ = os.ReadFile(recordPath(dir, a.ID))
	if !bytes.Equal(before, current) {
		t.Fatal("delivery failure changed original account")
	}
	assertNoTempFiles(t, dir)
}

func TestPreparedNewAccountCommitAndRollback(t *testing.T) {
	s, dir, _ := newTestStore(t)
	tx, err := prepareForTest(t, s, Account{Label: "A"}, sampleSecret(1))
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if list, _ := mustList(t, s); len(list) != 0 {
		t.Fatal("new account visible before commit")
	}
	hasDurable := false
	for _, name := range dirNames(t, dir) {
		if strings.HasSuffix(name, ".tmp") {
			hasDurable = true
		}
	}
	if !hasDurable {
		t.Fatal("Prepare returned before writing the durable encrypted candidate")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Unlock(tx.Account().ID); err != nil || got != sampleSecret(1) {
		t.Fatal("committed secret unavailable")
	}
	if err := tx.Commit(); err != nil {
		t.Fatal("Commit is not idempotent")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal("Rollback after Commit must be a no-op")
	}
	tx2, err := prepareForTest(t, s, Account{Label: "B"}, sampleSecret(2))
	if err != nil {
		t.Fatal(err)
	}
	if err := tx2.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := tx2.Commit(); err == nil {
		t.Fatal("rolled back transaction committed")
	}
	if list, _ := mustList(t, s); len(list) != 1 {
		t.Fatal("rolled back new account survived")
	}
	assertNoTempFiles(t, dir)
}

func TestLastIsProtectedAndPurposeBound(t *testing.T) {
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
		t.Fatal("last account id is stored in plaintext")
	}
	if _, err := p.Unprotect(raw); err != nil {
		t.Fatal("last account state is not protector-wrapped")
	}
	other := openTestStore(t, dir, newFakeProtector(t))
	if last, _ := other.Last(); last != "" {
		t.Fatal("wrong user could read remembered account")
	}
	if err := s.SetLast(""); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(filepath.Join(dir, lastFileName))
	if len(raw) == 0 {
		t.Fatal("explicit forget must also be protected state")
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
	s := openTestStore(t, dir, p)
	var a Account
	for i := 0; i < 12; i++ {
		a = mustSave(t, s, "A", sampleSecret(i))
	}
	p.opens = 0
	s2 := openTestStore(t, dir, p)
	saved := mustSave(t, s2, "", sampleSecret(11))
	if saved.ID != a.ID {
		t.Fatal("subject key was not stable across reopen")
	}
	if p.opens > 4 {
		t.Fatalf("dedupe unwrapped %d keys; it must not decrypt every account", p.opens)
	}
	if _, err := os.Stat(filepath.Join(dir, "subject-key.bin")); err != nil {
		t.Fatal("no protected stable subject key")
	}
}

func TestScanReadsAtMost64CandidateRecords(t *testing.T) {
	s, dir, _ := newTestStore(t)
	for i := 0; i < 100; i++ {
		if err := os.WriteFile(recordPath(dir, fmt.Sprintf("%032x", i)), []byte("invalid"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	list, corrupt, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 || corrupt != 64 {
		t.Fatalf("scan visited %d invalid records, want bounded 64", corrupt)
	}
	if _, err := s.Save(Account{Label: "A"}, sampleSecret(1)); err == nil {
		t.Fatal("Save ignored a saturated record scan")
	}
}

func TestSecretProtocolByteLimits(t *testing.T) {
	for _, test := range []struct{ uid, token int }{{64, 1}, {1, 128}} {
		sec := sampleSecret(1)
		sec.ChannelUID = strings.Repeat("u", test.uid)
		sec.ChannelToken = strings.Repeat("t", test.token)
		if err := sec.validate(); !errors.Is(err, ErrInvalidSecret) {
			t.Fatal("accepted credential exceeding SDK field bounds")
		}
	}
	sec := sampleSecret(1)
	sec.ChannelUID = "   "
	if err := sec.validate(); !errors.Is(err, ErrInvalidSecret) {
		t.Fatal("accepted blank channel uid")
	}
}

func TestSecretAllFormattingVerbsRedact(t *testing.T) {
	sec := sampleSecret(1)
	for _, verb := range []string{"%x", "%X", "%q", "%#v", "%+v"} {
		out := fmt.Sprintf(verb, sec)
		if strings.Contains(out, sec.ChannelToken) || strings.Contains(out, fmt.Sprintf("%x", sec.ChannelToken)) {
			t.Fatalf("secret formatting %s leaks credential", verb)
		}
	}
}

func TestOpenPreservesUnknownPendingFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "accounts")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, tempPrefix+"0123456789abcdef"+tempSuffix)
	if err := os.WriteFile(path, []byte("not owned by this transaction"), 0600); err != nil {
		t.Fatal(err)
	}
	openTestStore(t, dir, newFakeProtector(t))
	if _, err := os.Stat(path); err != nil {
		t.Fatal("Open deleted an unrecognized file based only on its name")
	}
}

func TestNoticeRevisionIsProtectedAndIndependent(t *testing.T) {
	s, dir, p := newTestStore(t)
	getter := reflect.ValueOf(s).MethodByName("NoticeAcknowledged")
	accept := reflect.ValueOf(s).MethodByName("AcceptNotice")
	if !getter.IsValid() || !accept.IsValid() {
		t.Fatal("protected notice revision API is absent")
	}
	acknowledged := func(revision int) bool {
		out := getter.Call([]reflect.Value{reflect.ValueOf(revision)})
		if !out[1].IsNil() {
			t.Fatal(out[1].Interface())
		}
		return out[0].Bool()
	}
	if acknowledged(2) {
		t.Fatal("new store already acknowledged")
	}
	out := accept.Call([]reflect.Value{reflect.ValueOf(2)})
	if !out[0].IsNil() {
		t.Fatal(out[0].Interface())
	}
	if !acknowledged(2) || acknowledged(3) {
		t.Fatal("notice revision was not checked exactly")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "notice.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 1024 {
		t.Fatal("notice state exceeds bound")
	}
	plain, err := p.Unprotect(raw)
	if err != nil {
		t.Fatal("notice state not wrapped")
	}
	wipeBytes(plain)
	if list, _ := mustList(t, s); len(list) != 0 {
		t.Fatal("notice acceptance created an account")
	}
}
