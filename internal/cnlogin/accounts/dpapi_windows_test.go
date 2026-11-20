// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package accounts

import (
	"bytes"
	"errors"
	"path/filepath"
	"testing"

	"github.com/nerv-base/Oddward/internal/database/dbtest"
)

// These tests call the real CurrentUser DPAPI. That only uses the current
// user's own master key; nothing is written outside t.TempDir().

func TestDPAPIRoundTrip(t *testing.T) {
	var p DPAPI
	plain := []byte("0123456789abcdef0123456789abcdef")
	wrapped, err := p.Protect(plain)
	if err != nil {
		t.Fatalf("Protect: %v", err)
	}
	if len(wrapped) == 0 || bytes.Contains(wrapped, plain) {
		t.Fatalf("wrapped blob is empty or contains the plaintext")
	}
	got, err := p.Unprotect(wrapped)
	if err != nil {
		t.Fatalf("Unprotect: %v", err)
	}
	if !bytes.Equal(got, plain) {
		t.Fatal("Unprotect returned different bytes")
	}

	tampered := bytes.Clone(wrapped)
	tampered[len(tampered)-1] ^= 0x01
	if _, err := p.Unprotect(tampered); err == nil {
		t.Fatal("Unprotect accepted a tampered blob")
	}
	if _, err := p.Unprotect([]byte("not a dpapi blob")); err == nil {
		t.Fatal("Unprotect accepted garbage")
	}
	if _, err := p.Protect(nil); err == nil {
		t.Fatal("Protect accepted empty input")
	}
	if _, err := p.Unprotect(nil); err == nil {
		t.Fatal("Unprotect accepted empty input")
	}
}

func TestDPAPIStoreRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "accounts")
	db := dbtest.Open(t)
	s, err := Open(db, dir, DPAPI{})
	if err != nil {
		t.Fatal(err)
	}
	sec := sampleSecret(1)
	a, err := s.Save(Account{Label: "138****0000"}, sec)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	s2, err := Open(db, dir, DPAPI{})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s2.Unlock(a.ID)
	if err != nil || got != sec {
		t.Fatalf("Unlock with DPAPI: %v", err)
	}
	// A DPAPI-wrapped key cannot be opened by a different protector.
	if _, err := openTestStore(t, db, dir, newFakeProtector(t)).Unlock(a.ID); !errors.Is(err, ErrUnlock) {
		t.Fatalf("Unlock with fake protector = %v, want ErrUnlock", err)
	}
}
