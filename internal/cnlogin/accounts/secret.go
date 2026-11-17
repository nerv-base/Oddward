// SPDX-License-Identifier: GPL-3.0-or-later

package accounts

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// The channel credential travels in the SDK request with a single-byte
	// length prefix per field, so the protocol bounds are 63 and 127 bytes.
	// Anything longer cannot be delivered and is rejected before it is stored.
	maxChannelUIDBytes   = 63
	maxChannelTokenBytes = 127
	redactedSecret       = "accounts.Secret{redacted}"
)

// Secret is the encrypted part of a saved account.
type Secret struct {
	DeviceID     string // 32 lowercase hex chars, generated for this account
	ChannelUID   string
	ChannelToken string
}

// String redacts every field so a Secret is safe to pass to fmt or logs.
func (Secret) String() string { return redactedSecret }

// GoString redacts %#v output.
func (Secret) GoString() string { return redactedSecret }

// LogValue redacts slog output.
func (Secret) LogValue() slog.Value { return slog.StringValue(redactedSecret) }

// Wipe drops the secret's references. Go strings are immutable and cannot be
// zeroed in place; the byte buffers this package uses internally are zeroed
// after use.
func (s *Secret) Wipe() { *s = Secret{} }

func (s Secret) validate() error {
	if !isLowerHex(s.DeviceID, 32) {
		return fmt.Errorf("%w: device id must be 32 lowercase hex chars", ErrInvalidSecret)
	}
	if !validCredentialField(s.ChannelUID, maxChannelUIDBytes) {
		return fmt.Errorf("%w: channel uid", ErrInvalidSecret)
	}
	if !validCredentialField(s.ChannelToken, maxChannelTokenBytes) {
		return fmt.Errorf("%w: channel token", ErrInvalidSecret)
	}
	return nil
}

func validCredentialField(v string, maxBytes int) bool {
	if v == "" || len(v) > maxBytes || !utf8.ValidString(v) || strings.TrimSpace(v) == "" {
		return false
	}
	for _, r := range v {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}

// payload is the plaintext JSON inside the AES-GCM envelope.
type payload struct {
	DeviceID     string `json:"deviceId"`
	ChannelUID   string `json:"channelUid"`
	ChannelToken string `json:"channelToken"`
}

func encodePayload(s Secret) ([]byte, error) {
	return json.Marshal(payload(s))
}

func decodePayload(b []byte) (Secret, error) {
	var p payload
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil || dec.More() {
		return Secret{}, fmt.Errorf("%w: encrypted payload is malformed", ErrCorrupt)
	}
	s := Secret(p)
	if err := s.validate(); err != nil {
		return Secret{}, fmt.Errorf("%w: encrypted payload is invalid", ErrCorrupt)
	}
	return s, nil
}

func wipeBytes(b []byte) { clear(b) }

func isLowerHex(s string, n int) bool {
	return isHex(s, n, 'a', 'f')
}

func isUpperHex(s string, n int) bool {
	return isHex(s, n, 'A', 'F')
}

func isHex(s string, n int, lo, hi byte) bool {
	if len(s) != n {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < lo || c > hi) {
			return false
		}
	}
	return true
}
