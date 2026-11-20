// SPDX-License-Identifier: GPL-3.0-or-later

package accounts

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"strings"

	"github.com/nerv-base/Oddward/internal/database"
)

// The Tajiduo attachment is a separate AES-GCM envelope beside the account
// record. It reuses the store's KeyProtector and AES-256-GCM scheme but has its
// own version and AAD domain, and it binds the owning account id and center uid
// so an attachment cannot be transplanted to another account. The database keeps
// it in account_tajiduo; the mirrored <id>.tjd file exists for a rollback build.
const (
	tajiduoVersion    = 1
	tajiduoExt        = ".tjd"
	tajiduoAADContext = "oddward/cnlogin/accounts/tajiduo"

	// Field bounds reuse the credential-field rules from secret.go.
	maxTajiduoCenterUIDBytes    = 63
	maxTajiduoRefreshTokenBytes = 255
	maxTajiduoLaohuUserIDBytes  = 127
	maxTajiduoRoleIDBytes       = 127

	redactedTajiduo = "accounts.TajiduoGrant{redacted}"
)

var (
	// ErrTajiduoMismatch means the Tajiduo account is not the one this saved
	// game account was created for (its LaohuUserID differs from the channel uid).
	ErrTajiduoMismatch = errors.New("accounts: this Tajiduo account does not belong to this game account")
	// ErrNoTajiduo means the account has no Tajiduo attachment.
	ErrNoTajiduo = errors.New("accounts: account has no Tajiduo attachment")
	// ErrTajiduoImmutable means an update tried to change the device id, center
	// uid or LaohuUserID; only the refresh token and role id may change.
	ErrTajiduoImmutable = errors.New("accounts: Tajiduo update cannot change device id, center uid or laohu user id")
)

// TajiduoGrant is the encrypted Tajiduo session stored in account_tajiduo. It
// never holds a plaintext phone, SMS code or access token; only these fields are
// persisted, always encrypted.
type TajiduoGrant struct {
	DeviceID     string // 32 uppercase hex chars, generated for this link
	CenterUID    string
	RefreshToken string
	LaohuUserID  string
	RoleID       string // may be empty
}

// String redacts every field so a TajiduoGrant is safe to pass to fmt or logs.
func (TajiduoGrant) String() string { return redactedTajiduo }

// GoString redacts %#v output.
func (TajiduoGrant) GoString() string { return redactedTajiduo }

// LogValue redacts slog output.
func (TajiduoGrant) LogValue() slog.Value { return slog.StringValue(redactedTajiduo) }

// Format redacts every fmt verb (including %x, %X and %q) so a stray format
// directive cannot leak the refresh token.
func (TajiduoGrant) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, redactedTajiduo) }

// Wipe drops the grant's references. Go strings are immutable and cannot be
// zeroed in place; byte buffers used internally are zeroed after use.
func (g *TajiduoGrant) Wipe() { *g = TajiduoGrant{} }

func (g TajiduoGrant) validate() error {
	if !isUpperHex(g.DeviceID, 32) {
		return fmt.Errorf("%w: device id must be 32 uppercase hex chars", ErrInvalidSecret)
	}
	if !validCredentialField(g.CenterUID, maxTajiduoCenterUIDBytes) {
		return fmt.Errorf("%w: center uid", ErrInvalidSecret)
	}
	if !validCredentialField(g.RefreshToken, maxTajiduoRefreshTokenBytes) {
		return fmt.Errorf("%w: refresh token", ErrInvalidSecret)
	}
	if !validCredentialField(g.LaohuUserID, maxTajiduoLaohuUserIDBytes) {
		return fmt.Errorf("%w: laohu user id", ErrInvalidSecret)
	}
	if g.RoleID != "" && !validCredentialField(g.RoleID, maxTajiduoRoleIDBytes) {
		return fmt.Errorf("%w: role id", ErrInvalidSecret)
	}
	return nil
}

// NewTajiduoDeviceID returns a fresh 32-character uppercase hex device id.
func NewTajiduoDeviceID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(b[:])), nil
}

// tajiduoFile is the on-disk JSON shape. The clear header carries the account
// id and center uid so they can be bound by the AAD; everything secret is in
// Secret (wrapped key ‖ nonce ‖ sealed payload).
type tajiduoFile struct {
	Version    int    `json:"version"`
	Protection string `json:"protection"`
	ID         string `json:"id"`
	CenterUID  string `json:"centerUid"`
	Secret     []byte `json:"secret"`
}

// tajiduoPayload is the encrypted plaintext.
type tajiduoPayload struct {
	DeviceID     string `json:"deviceId"`
	CenterUID    string `json:"centerUid"`
	RefreshToken string `json:"refreshToken"`
	LaohuUserID  string `json:"laohuUserId"`
	RoleID       string `json:"roleId"`
}

// tajiduoAAD binds the account id, center uid and attachment version, so an
// attachment decrypted under another account (or after tampering with the
// header) fails authentication.
func tajiduoAAD(id, centerUID string, version int) []byte {
	fields := []string{
		tajiduoAADContext,
		protectionKind,
		strconv.Itoa(version),
		id,
		centerUID,
	}
	var b []byte
	for _, f := range fields {
		b = binary.BigEndian.AppendUint32(b, uint32(len(f)))
		b = append(b, f...)
	}
	return b
}

// splitTajiduoSecret splits the blob into wrapped key, nonce and sealed bytes,
// reporting whether the lengths are structurally valid.
func splitTajiduoSecret(secret []byte) (wrapped, nonce, sealed []byte, ok bool) {
	if len(secret) < 2 {
		return nil, nil, nil, false
	}
	n := int(binary.BigEndian.Uint16(secret))
	secret = secret[2:]
	if n == 0 || n > maxWrappedSize || len(secret) < n+gcmNonceSize+gcmTagSize+1 {
		return nil, nil, nil, false
	}
	return secret[:n:n],
		secret[n : n+gcmNonceSize : n+gcmNonceSize],
		secret[n+gcmNonceSize:],
		true
}

// parseTajiduoBytes validates everything that can be checked without the key.
func parseTajiduoBytes(raw []byte) (tajiduoFile, error) {
	bad := func(what string) (tajiduoFile, error) {
		return tajiduoFile{}, fmt.Errorf("%w: %s", ErrCorrupt, what)
	}
	if len(raw) == 0 || len(raw) > MaxRecordSize {
		return bad("size")
	}
	var f tajiduoFile
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return bad("json")
	}
	if _, err := dec.Token(); err != io.EOF {
		return bad("trailing data")
	}
	if f.Version != tajiduoVersion {
		return bad("version")
	}
	if f.Protection != protectionKind {
		return bad("protection")
	}
	if !isLowerHex(f.ID, 32) {
		return bad("id")
	}
	if !validCredentialField(f.CenterUID, maxTajiduoCenterUIDBytes) {
		return bad("centerUid")
	}
	if _, _, _, ok := splitTajiduoSecret(f.Secret); !ok {
		return bad("secret")
	}
	return f, nil
}

// parseTajiduo additionally requires the attachment to belong to the account row
// it was read from.
func parseTajiduo(id string, raw []byte) (tajiduoFile, error) {
	f, err := parseTajiduoBytes(raw)
	if err != nil {
		return tajiduoFile{}, err
	}
	if f.ID != id {
		return tajiduoFile{}, fmt.Errorf("%w: id does not match its account row", ErrCorrupt)
	}
	return f, nil
}

func encodeTajiduoPayload(g TajiduoGrant) ([]byte, error) {
	return json.Marshal(tajiduoPayload(g))
}

func decodeTajiduoPayload(b []byte) (TajiduoGrant, error) {
	var p tajiduoPayload
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil || dec.More() {
		return TajiduoGrant{}, fmt.Errorf("%w: encrypted payload is malformed", ErrCorrupt)
	}
	g := TajiduoGrant(p)
	if err := g.validate(); err != nil {
		return TajiduoGrant{}, fmt.Errorf("%w: encrypted payload is invalid", ErrCorrupt)
	}
	return g, nil
}

// sealTajiduo encrypts g for account id under a fresh data key and wraps the
// key, checking that the protector can unwrap it again before anything is
// written.
func (s *Store) sealTajiduo(id string, g TajiduoGrant) ([]byte, error) {
	key := make([]byte, dataKeySize)
	defer wipeBytes(key)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	wrapped, err := s.p.Protect(key)
	if err != nil {
		return nil, fmt.Errorf("accounts: protect tajiduo key: %w", err)
	}
	if len(wrapped) == 0 || len(wrapped) > maxWrappedSize {
		return nil, fmt.Errorf("accounts: protected tajiduo key is %d bytes", len(wrapped))
	}
	check, err := s.p.Unprotect(wrapped)
	ok := err == nil && subtle.ConstantTimeCompare(check, key) == 1
	wipeBytes(check)
	if !ok {
		return nil, fmt.Errorf("accounts: protected tajiduo key does not unwrap to the original: %v", err)
	}
	plain, err := encodeTajiduoPayload(g)
	if err != nil {
		return nil, err
	}
	defer wipeBytes(plain)
	nonce := make([]byte, gcmNonceSize)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	aead, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	sealed := aead.Seal(nil, nonce, plain, tajiduoAAD(id, g.CenterUID, tajiduoVersion))
	blob := binary.BigEndian.AppendUint16(nil, uint16(len(wrapped)))
	blob = append(blob, wrapped...)
	blob = append(blob, nonce...)
	blob = append(blob, sealed...)
	out, err := json.Marshal(tajiduoFile{
		Version:    tajiduoVersion,
		Protection: protectionKind,
		ID:         id,
		CenterUID:  g.CenterUID,
		Secret:     blob,
	})
	if err != nil {
		return nil, err
	}
	if len(out) > MaxRecordSize {
		return nil, fmt.Errorf("accounts: encoded tajiduo is %d bytes, limit %d", len(out), MaxRecordSize)
	}
	return out, nil
}

// openTajiduo unwraps the key and decrypts f's payload. A structural or
// authentication failure is reported as ErrCorrupt, never ErrUnlock: the file
// itself is unusable for this account.
func (s *Store) openTajiduo(f tajiduoFile) (TajiduoGrant, error) {
	wrapped, nonce, sealed, ok := splitTajiduoSecret(f.Secret)
	if !ok {
		return TajiduoGrant{}, fmt.Errorf("%w: secret", ErrCorrupt)
	}
	key, err := s.p.Unprotect(wrapped)
	if err != nil {
		return TajiduoGrant{}, fmt.Errorf("%w: %w", ErrCorrupt, err)
	}
	defer wipeBytes(key)
	if len(key) != dataKeySize {
		return TajiduoGrant{}, fmt.Errorf("%w: data key has wrong size", ErrCorrupt)
	}
	aead, err := newGCM(key)
	if err != nil {
		return TajiduoGrant{}, fmt.Errorf("%w: %w", ErrCorrupt, err)
	}
	plain, err := aead.Open(nil, nonce, sealed, tajiduoAAD(f.ID, f.CenterUID, f.Version))
	if err != nil {
		return TajiduoGrant{}, fmt.Errorf("%w: authentication failed", ErrCorrupt)
	}
	defer wipeBytes(plain)
	g, err := decodeTajiduoPayload(plain)
	if err != nil {
		return TajiduoGrant{}, err
	}
	if g.CenterUID != f.CenterUID {
		return TajiduoGrant{}, fmt.Errorf("%w: center uid mismatch", ErrCorrupt)
	}
	return g, nil
}

// loadTajiduo reads and decrypts the attachment of id. Callers hold s.mu.
func (s *Store) loadTajiduo(ctx context.Context, id string) (TajiduoGrant, error) {
	if !isLowerHex(id, 32) {
		return TajiduoGrant{}, ErrNoTajiduo
	}
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT file FROM account_tajiduo WHERE account_id = ?`, id).Scan(&raw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return TajiduoGrant{}, ErrNoTajiduo
	case err != nil:
		return TajiduoGrant{}, fmt.Errorf("accounts: load Tajiduo attachment: %w", err)
	}
	f, err := parseTajiduo(id, raw)
	if err != nil {
		return TajiduoGrant{}, err
	}
	return s.openTajiduo(f)
}

// tajiduoState distinguishes a readable attachment from one that needs a new
// link. An attachment that does not parse or decrypt must not be presented as
// usable.
func (s *Store) tajiduoState(id string, raw []byte) (bool, string) {
	if raw == nil {
		return false, ""
	}
	f, err := parseTajiduo(id, raw)
	if err != nil {
		return false, "needs-relink"
	}
	if _, err := s.openTajiduo(f); err != nil {
		return false, "needs-relink"
	}
	return true, "linked"
}

// LinkTajiduo stores g as the encrypted Tajiduo attachment for account id. The
// grant's LaohuUserID must equal the account's channel uid; otherwise it is not
// this game account's Tajiduo session and ErrTajiduoMismatch is returned without
// writing anything.
func (s *Store) LinkTajiduo(id string, g TajiduoGrant) error {
	if err := g.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	if err := s.checkDir(); err != nil {
		return err
	}
	r, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	key, plain, err := s.open(r)
	if err != nil {
		return err
	}
	sec, decErr := decodePayload(plain)
	wipeBytes(key)
	wipeBytes(plain)
	if decErr != nil {
		return decErr
	}
	defer sec.Wipe()
	if subtle.ConstantTimeCompare([]byte(g.LaohuUserID), []byte(sec.ChannelUID)) != 1 {
		return ErrTajiduoMismatch
	}
	data, err := s.sealTajiduo(id, g)
	if err != nil {
		return err
	}
	return s.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) (mirrorChange, error) {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO account_tajiduo(account_id, center_uid, file, updated_at)
			VALUES(?, ?, ?, ?)
			ON CONFLICT(account_id) DO UPDATE SET
				center_uid = excluded.center_uid,
				file = excluded.file,
				updated_at = excluded.updated_at`,
			id, g.CenterUID, data, database.Stamp(s.now())); err != nil {
			return mirrorChange{}, fmt.Errorf("accounts: link Tajiduo: %w", err)
		}
		return mirrorChange{write: map[string][]byte{id + tajiduoExt: data}}, nil
	})
}

// UnlockTajiduo returns the decrypted Tajiduo attachment for id. It returns
// ErrNoTajiduo when there is no attachment and ErrCorrupt when it is damaged.
func (s *Store) UnlockTajiduo(id string) (TajiduoGrant, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	if err := s.checkDir(); err != nil {
		return TajiduoGrant{}, err
	}
	return s.loadTajiduo(ctx, id)
}

// UpdateTajiduo persists a refreshed grant. The attachment must already exist
// and its DeviceID, CenterUID and LaohuUserID must be unchanged; only the
// refresh token and role id may differ.
func (s *Store) UpdateTajiduo(id string, g TajiduoGrant) error {
	if err := g.validate(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	if err := s.checkDir(); err != nil {
		return err
	}
	existing, err := s.loadTajiduo(ctx, id)
	if err != nil {
		return err
	}
	if g.DeviceID != existing.DeviceID || g.CenterUID != existing.CenterUID || g.LaohuUserID != existing.LaohuUserID {
		return ErrTajiduoImmutable
	}
	data, err := s.sealTajiduo(id, g)
	if err != nil {
		return err
	}
	return s.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) (mirrorChange, error) {
		if _, err := tx.ExecContext(ctx, `UPDATE account_tajiduo SET center_uid = ?, file = ?, updated_at = ? WHERE account_id = ?`,
			g.CenterUID, data, database.Stamp(s.now()), id); err != nil {
			return mirrorChange{}, fmt.Errorf("accounts: update Tajiduo: %w", err)
		}
		return mirrorChange{write: map[string][]byte{id + tajiduoExt: data}}, nil
	})
}

// UnlinkTajiduo deletes the Tajiduo attachment for id. It is idempotent: a
// missing attachment (or unknown id) is not an error.
func (s *Store) UnlinkTajiduo(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	if err := s.checkDir(); err != nil {
		return err
	}
	if !isLowerHex(id, 32) {
		return nil
	}
	return s.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) (mirrorChange, error) {
		if _, err := tx.ExecContext(ctx, `DELETE FROM account_tajiduo WHERE account_id = ?`, id); err != nil {
			return mirrorChange{}, fmt.Errorf("accounts: unlink Tajiduo: %w", err)
		}
		return mirrorChange{remove: []string{id + tajiduoExt}}, nil
	})
}
