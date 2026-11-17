// SPDX-License-Identifier: GPL-3.0-or-later

package ecb

import (
	"bytes"
	"encoding/base64"
	"errors"
	"testing"
)

// Synthetic vectors from spec §8.1 (computed with .NET, synthetic key).
func TestEncryptMatchesSpecVectors(t *testing.T) {
	key := []byte("ghijklmnopqrstuv")
	for plain, want := range map[string]string{
		"13800138000": "iQF6i+uWVp5hMHbiKjbi+w==",
		"123456":      "OHWmvBk3Be7jbSCY0BFy1Q==",
	} {
		ct, err := Encrypt(key, []byte(plain))
		if err != nil {
			t.Fatal(err)
		}
		if got := base64.StdEncoding.EncodeToString(ct); got != want {
			t.Errorf("Encrypt(%q) = %s, want %s", plain, got, want)
		}
	}
}

func TestRoundTripPadsEveryLength(t *testing.T) {
	key := []byte("QRSTUVWXYZ012345")
	for n := 0; n <= 48; n++ {
		plain := bytes.Repeat([]byte{'p'}, n)
		ct, err := Encrypt(key, plain)
		if err != nil {
			t.Fatal(err)
		}
		if len(ct)%16 != 0 || len(ct) <= n {
			t.Fatalf("len %d -> ciphertext %d", n, len(ct))
		}
		got, err := Decrypt(key, ct)
		if err != nil || !bytes.Equal(got, plain) {
			t.Fatalf("len %d round trip: %q, %v", n, got, err)
		}
	}
}

func TestDecryptRejectsMalformedInput(t *testing.T) {
	key := []byte("QRSTUVWXYZ012345")
	good, _ := Encrypt(key, []byte("hello"))
	bad := append([]byte(nil), good...)
	bad[len(bad)-1] ^= 0x55 // corrupts the padding block
	cases := map[string][]byte{
		"empty":   nil,
		"partial": good[:15],
		"padding": bad,
	}
	for name, ct := range cases {
		if _, err := Decrypt(key, ct); !errors.Is(err, ErrCiphertext) {
			t.Errorf("%s: err = %v, want ErrCiphertext", name, err)
		}
	}
	if _, err := Decrypt([]byte("short"), good); err == nil {
		t.Error("short key accepted")
	}
	if _, err := Encrypt([]byte("short"), []byte("x")); err == nil {
		t.Error("short key accepted for encryption")
	}
}
