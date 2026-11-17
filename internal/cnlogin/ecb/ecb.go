// SPDX-License-Identifier: GPL-3.0-or-later

// Package ecb implements AES-128-ECB with PKCS#7 padding, the cipher mode the
// CN SDK uses for its boot configuration, phone/captcha fields and the
// OneLogin envelope (spec §3.1, §3.2, §3.8). ECB is not a secure general
// purpose mode; it exists here only for protocol compatibility and must not
// be used for Oddward's own storage.
package ecb

import (
	"crypto/aes"
	"crypto/subtle"
	"errors"
	"fmt"
)

// ErrCiphertext reports a ciphertext that is empty, not block-aligned, or has
// invalid padding. It never includes plaintext or key material.
var ErrCiphertext = errors.New("ecb: malformed ciphertext")

// Encrypt pads plain with PKCS#7 and encrypts it block by block with the
// 16-byte key.
func Encrypt(key, plain []byte) ([]byte, error) {
	if len(key) != aes.BlockSize {
		return nil, fmt.Errorf("ecb: key must be %d bytes", aes.BlockSize)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	out := make([]byte, len(plain)+pad)
	copy(out, plain)
	for i := len(plain); i < len(out); i++ {
		out[i] = byte(pad)
	}
	for i := 0; i < len(out); i += aes.BlockSize {
		block.Encrypt(out[i:i+aes.BlockSize], out[i:i+aes.BlockSize])
	}
	return out, nil
}

// Decrypt decrypts a block-aligned ciphertext with the 16-byte key and strips
// PKCS#7 padding. The returned slice is freshly allocated; on error any
// partial plaintext is wiped.
func Decrypt(key, ct []byte) ([]byte, error) {
	if len(key) != aes.BlockSize {
		return nil, fmt.Errorf("ecb: key must be %d bytes", aes.BlockSize)
	}
	if len(ct) == 0 || len(ct)%aes.BlockSize != 0 {
		return nil, ErrCiphertext
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(ct))
	for i := 0; i < len(ct); i += aes.BlockSize {
		block.Decrypt(out[i:i+aes.BlockSize], ct[i:i+aes.BlockSize])
	}
	pad := int(out[len(out)-1])
	valid := pad >= 1 && pad <= aes.BlockSize
	if valid {
		want := make([]byte, pad)
		for i := range want {
			want[i] = byte(pad)
		}
		valid = subtle.ConstantTimeCompare(out[len(out)-pad:], want) == 1
	}
	if !valid {
		clear(out)
		return nil, ErrCiphertext
	}
	plain := out[:len(out)-pad]
	clear(out[len(plain):])
	return plain, nil
}
