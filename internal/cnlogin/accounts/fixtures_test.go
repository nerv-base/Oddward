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
)

// fakeProtector wraps data keys with AES-GCM under its own random key, so two
// fakes behave like two different Windows users: neither can unwrap the
// other's keys.
type fakeProtector struct {
	aead cipher.AEAD
	fail error
}

func newFakeProtector(t *testing.T) *fakeProtector {
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

func openTestStore(t *testing.T, dir string, p KeyProtector) *Store {
	t.Helper()
	s, err := Open(dir, p)
	if err != nil {
		t.Fatalf("Open(%q): %v", dir, err)
	}
	clock := &testClock{t: time.Date(2026, 11, 13, 8, 0, 0, 123456789, time.UTC)}
	s.now = clock.now
	return s
}

func newTestStore(t *testing.T) (*Store, string, *fakeProtector) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "accounts")
	p := newFakeProtector(t)
	return openTestStore(t, dir, p), dir, p
}

func sampleSecret(n int) Secret {
	return Secret{
		DeviceID:     fmt.Sprintf("%032x", 0xabc000+n),
		ChannelUID:   fmt.Sprintf("uid-%d-7788", n),
		ChannelToken: fmt.Sprintf("token-%d-SECRETVALUE", n),
	}
}

func mustSave(t *testing.T, s *Store, label string, sec Secret) Account {
	t.Helper()
	a, err := s.Save(Account{Label: label}, sec)
	if err != nil {
		t.Fatalf("Save(%q): %v", label, err)
	}
	return a
}

func mustList(t *testing.T, s *Store) ([]Account, int) {
	t.Helper()
	list, corrupt, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	return list, corrupt
}

func recordPath(dir, id string) string { return filepath.Join(dir, id+".acct") }

// editRecord decodes the on-disk JSON, lets edit change it and writes it to
// the path returned by edit (the same file when it returns "").
func editRecord(t *testing.T, dir, id string, edit func(map[string]any) string) {
	t.Helper()
	path := recordPath(dir, id)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	target := edit(m)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if target != "" && target != path {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		path = target
	}
	if err := os.WriteFile(path, out, 0o600); err != nil {
		t.Fatal(err)
	}
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
