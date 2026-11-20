// SPDX-License-Identifier: GPL-3.0-or-later

package accounts

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

const (
	recordVersion  = 1
	recordExt      = ".acct"
	protectionKind = "DPAPI-CurrentUser-AES256GCM-v1"
	aadContext     = "oddward/cnlogin/accounts/record"
	// identityPrefix is the domain separator of the de-duplication HMAC. The
	// upstream format prefixes the account fields; this format only needs the
	// channel uid because the store holds a single channel.
	identityPrefix = "oddward/cnlogin/accounts/identity|uid:"
	dataKeySize    = 32
	gcmNonceSize   = 12
	gcmTagSize     = 16
	maxWrappedSize = 4096
	identityKeyLen = 2 * sha256.Size

	// keylessIdentityPrefix marks the accounts.identity_key of a record that
	// carries no identity key, which is a record written before a subject key
	// existed. The column is NOT NULL and unique, so such a record needs a value
	// no HMAC can equal: identity keys are upper-case hex, this is neither.
	keylessIdentityPrefix = "keyless:"
)

// recordFile is the on-disk JSON shape. Secret marshals as standard base64.
type recordFile struct {
	Version     int    `json:"version"`
	Protection  string `json:"protection"`
	ID          string `json:"id"`
	Label       string `json:"label"`
	SavedAt     string `json:"savedAt"`
	IdentityKey string `json:"identityKey"`
	Secret      []byte `json:"secret"`
}

// record is a parsed, structurally valid record that has not been unlocked.
type record struct {
	Account
	identityKey string // HMAC of the channel uid; "" for a record without one
	wrapped     []byte
	nonce       []byte
	sealed      []byte
}

func formatTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

// recordAAD binds every public field: changing any of them on disk makes
// AES-GCM authentication fail. The empty field is the lease expiry the upstream
// format carries and this format never sets; it stays bound so the authenticated
// bytes keep the documented layout.
func recordAAD(a Account, identityKey string) []byte {
	fields := []string{
		aadContext,
		protectionKind,
		strconv.Itoa(recordVersion),
		a.ID,
		a.Label,
		formatTime(a.SavedAt),
		"",
		identityKey,
	}
	var b []byte
	for _, f := range fields {
		b = binary.BigEndian.AppendUint32(b, uint32(len(f)))
		b = append(b, f...)
	}
	return b
}

// identityKeyFor derives the de-duplication subject of a channel uid under the
// store's subject key. It is stored in clear in the record and bound by the
// AAD, so a save recognises its own account without unwrapping any data key.
func identityKeyFor(subjectKey []byte, channelUID string) string {
	mac := hmac.New(sha256.New, subjectKey)
	mac.Write([]byte(identityPrefix))
	mac.Write([]byte(channelUID))
	return strings.ToUpper(hex.EncodeToString(mac.Sum(nil)))
}

// keylessIdentity is the identity_key column value of a record with no identity
// key. It keeps the column unique without claiming a channel identity.
func keylessIdentity(id string) string { return keylessIdentityPrefix + id }

// identityColumn is the identity_key column value for a sealed record.
func identityColumn(id, identity string) string {
	if identity == "" {
		return keylessIdentity(id)
	}
	return identity
}

func encodeRecord(r record) ([]byte, error) {
	blob := binary.BigEndian.AppendUint16(nil, uint16(len(r.wrapped)))
	blob = append(blob, r.wrapped...)
	blob = append(blob, r.nonce...)
	blob = append(blob, r.sealed...)
	out, err := json.Marshal(recordFile{
		Version:     recordVersion,
		Protection:  protectionKind,
		ID:          r.ID,
		Label:       r.Label,
		SavedAt:     formatTime(r.SavedAt),
		IdentityKey: r.identityKey,
		Secret:      blob,
	})
	if err != nil {
		return nil, err
	}
	if len(out) > MaxRecordSize {
		return nil, fmt.Errorf("accounts: encoded record is %d bytes, limit %d", len(out), MaxRecordSize)
	}
	return out, nil
}

// parseRecordBytes validates everything that can be checked without the key.
func parseRecordBytes(raw []byte) (record, error) {
	bad := func(what string) (record, error) {
		return record{}, fmt.Errorf("%w: %s", ErrCorrupt, what)
	}
	if len(raw) == 0 || len(raw) > MaxRecordSize {
		return bad("size")
	}
	var f recordFile
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return bad("json")
	}
	if _, err := dec.Token(); err != io.EOF {
		return bad("trailing data")
	}
	if f.Version != recordVersion {
		return bad("version")
	}
	if f.Protection != protectionKind {
		return bad("protection")
	}
	if !isLowerHex(f.ID, 32) {
		return bad("id")
	}
	if label, err := sanitizeLabel(f.Label); err != nil || label != f.Label {
		return bad("label")
	}
	savedAt, err := time.Parse(time.RFC3339Nano, f.SavedAt)
	if err != nil || formatTime(savedAt) != f.SavedAt {
		return bad("savedAt")
	}
	if f.IdentityKey != "" && !isUpperHex(f.IdentityKey, identityKeyLen) {
		return bad("identityKey")
	}
	blob := f.Secret
	if len(blob) < 2 {
		return bad("secret")
	}
	n := int(binary.BigEndian.Uint16(blob))
	blob = blob[2:]
	if n == 0 || n > maxWrappedSize || len(blob) < n+gcmNonceSize+gcmTagSize+1 {
		return bad("secret")
	}
	return record{
		Account:     Account{ID: f.ID, Label: f.Label, SavedAt: savedAt.UTC()},
		identityKey: f.IdentityKey,
		wrapped:     blob[:n:n],
		nonce:       blob[n : n+gcmNonceSize : n+gcmNonceSize],
		sealed:      blob[n+gcmNonceSize:],
	}, nil
}

// parseRecord additionally requires the record to belong to the row it was read
// from.
func parseRecord(id string, raw []byte) (record, error) {
	r, err := parseRecordBytes(raw)
	if err != nil {
		return record{}, err
	}
	if r.ID != id {
		return record{}, fmt.Errorf("%w: id does not match its account row", ErrCorrupt)
	}
	return r, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// seal encrypts plain for r.Account under key, filling r.nonce and r.sealed.
func seal(r *record, key, plain []byte) error {
	aead, err := newGCM(key)
	if err != nil {
		return err
	}
	r.nonce = make([]byte, gcmNonceSize)
	if _, err := rand.Read(r.nonce); err != nil {
		return err
	}
	r.sealed = aead.Seal(nil, r.nonce, plain, recordAAD(r.Account, r.identityKey))
	return nil
}
