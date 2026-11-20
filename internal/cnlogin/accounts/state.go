// SPDX-License-Identifier: GPL-3.0-or-later

package accounts

// The device-wide login state lives in the single account_state row: the subject
// key that derives identity keys, the remembered account and the acknowledged
// notice revision. The subject key keeps its on-disk representation (a DPAPI
// wrapped JSON document), so wrapping, rotation and the mirror file behave
// exactly as before.

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/nerv-base/Oddward/internal/database"
)

const (
	// stateVersion is the schema version of a protected state document.
	stateVersion = 1
	// maxStateSize bounds the wrapped subject key and the wrapped last-account
	// document; maxNoticeSize bounds the smaller notice document.
	maxStateSize  = 4096
	maxNoticeSize = 1024

	// subjectFileName holds the wrapped subject key; lastFileName and
	// noticeFileName hold the other two state documents.
	subjectFileName = "subject-key.bin"
	lastFileName    = "last-login.bin"
	noticeFileName  = "notice.bin"

	// The purpose field binds a protected document to the one column it belongs
	// to, so a swapped file fails to decrypt instead of being misread.
	subjectPurpose = "subject-key"
	lastPurpose    = "last-login"
	noticePurpose  = "login-notice"

	// subjectKeySize is the size of the HMAC key that derives identity keys.
	subjectKeySize = 32
)

var (
	// errState reports a state document that does not validate: wrong version,
	// wrong purpose, or bytes this user's protector cannot unwrap.
	errState = errors.New("accounts: corrupt login state")

	// lastNone is the account_state.last_account value for "explicitly no
	// account". The column is NULL when nothing was ever chosen and its CHECK
	// allows NULL or 32 characters, so the empty string the file format used
	// cannot be stored verbatim; a run of dashes is 32 characters, keeps the
	// "never chosen" case distinguishable, and no real id can equal it because
	// ids are hex.
	lastNone = strings.Repeat("-", 32)
)

// subjectState is the wrapped subject key document.
type subjectState struct {
	Version int    `json:"version"`
	Purpose string `json:"purpose"`
	Key     []byte `json:"key"`
}

// lastState is the wrapped remembered-account document.
type lastState struct {
	Version int    `json:"version"`
	Purpose string `json:"purpose"`
	ID      string `json:"id"`
}

// noticeState is the wrapped notice-acknowledgement document.
type noticeState struct {
	Version         int    `json:"version"`
	Purpose         string `json:"purpose"`
	Revision        int    `json:"revision"`
	AcknowledgedUtc string `json:"acknowledgedUtc"`
}

// decodeState decodes one protected state document and rejects anything that is
// not exactly the document and purpose it was written for.
func decodeState(plain []byte, purpose string, doc any) error {
	dec := json.NewDecoder(bytes.NewReader(plain))
	dec.DisallowUnknownFields()
	if err := dec.Decode(doc); err != nil {
		return fmt.Errorf("%w: decode %s", errState, purpose)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: trailing data in %s", errState, purpose)
	}
	var (
		version int
		got     string
	)
	switch d := doc.(type) {
	case *subjectState:
		version, got = d.Version, d.Purpose
	case *lastState:
		version, got = d.Version, d.Purpose
	case *noticeState:
		version, got = d.Version, d.Purpose
	default:
		return fmt.Errorf("%w: unknown %s document", errState, purpose)
	}
	if version != stateVersion || got != purpose {
		return fmt.Errorf("%w: %s is version %d for %q", errState, purpose, version, got)
	}
	return nil
}

// sealState protects one state document. Only the wrapped bytes leave this
// function; the plaintext is wiped.
func (s *Store) sealState(name string, limit int, doc any) ([]byte, error) {
	plain, err := json.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("accounts: encode %s: %w", name, err)
	}
	defer wipeBytes(plain)
	wrapped, err := s.p.Protect(plain)
	if err != nil {
		return nil, fmt.Errorf("accounts: protect %s: %w", name, err)
	}
	if len(wrapped) == 0 || len(wrapped) > limit {
		return nil, fmt.Errorf("accounts: protect %s: %w: unexpected wrapped size %d", name, ErrCorrupt, len(wrapped))
	}
	return wrapped, nil
}

// readStateRow reads the three device-state values. A missing row reports zero
// values, which is what a store with no state has.
func (s *Store) readStateRow(ctx context.Context) (subject []byte, last sql.NullString, notice int64, err error) {
	err = s.db.QueryRow(ctx, `SELECT subject_key, last_account, notice_revision FROM account_state WHERE id = 1`).
		Scan(&subject, &last, &notice)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return nil, sql.NullString{}, 0, nil
	case err != nil:
		return nil, sql.NullString{}, 0, fmt.Errorf("accounts: read login state: %w", err)
	}
	return subject, last, notice, nil
}

// subjectKey returns the stored HMAC key for identity keys, generating one when
// there is none. created reports a key that did not exist before, which means
// records written without it have to be identified by decrypting them.
//
// A generated key is kept in the store, not written: a prepared save may be
// rolled back, and the key must not outlive it. The Commit that stores the first
// record derived from it writes it in the same transaction.
func (s *Store) subjectKey(ctx context.Context) (key []byte, created bool, err error) {
	var wrapped []byte
	wrapped, _, _, err = s.readStateRow(ctx)
	if err != nil {
		return nil, false, err
	}
	if wrapped != nil {
		if plain, unprotectErr := s.p.Unprotect(wrapped); unprotectErr == nil {
			var doc subjectState
			decodeErr := decodeState(plain, subjectPurpose, &doc)
			wipeBytes(plain)
			if decodeErr == nil && len(doc.Key) == subjectKeySize {
				return doc.Key, false, nil
			}
			wipeBytes(doc.Key)
		}
	}
	// A corrupt or foreign key is replaced, not repaired: it is derived state,
	// and every stored record stays readable without it.
	if s.pending != nil {
		return bytes.Clone(s.pending), true, nil
	}
	key = make([]byte, subjectKeySize)
	if _, err := rand.Read(key); err != nil {
		wipeBytes(key)
		return nil, false, fmt.Errorf("accounts: generate subject key: %w", err)
	}
	s.pending = key
	return bytes.Clone(key), true, nil
}

// pendingSubjectKey protects the key a staged save generated, for the
// transaction that stores the first record derived from it.
func (s *Store) pendingSubjectKey() ([]byte, error) {
	if s.pending == nil {
		return nil, nil
	}
	return s.sealState(subjectFileName, maxStateSize, subjectState{
		Version: stateVersion,
		Purpose: subjectPurpose,
		Key:     s.pending,
	})
}

// forgetPendingSubjectKey drops the staged key once it is stored.
func (s *Store) forgetPendingSubjectKey() {
	wipeBytes(s.pending)
	s.pending = nil
}

// readLast reports the remembered account id and whether a choice was recorded.
// chosen is false only when nothing was ever recorded, so Last can fall back to
// the only account.
func (s *Store) readLast(ctx context.Context) (id string, chosen bool, err error) {
	_, last, _, err := s.readStateRow(ctx)
	if err != nil {
		return "", false, err
	}
	if !last.Valid {
		return "", false, nil
	}
	if !isLowerHex(last.String, 32) {
		// The stored value is the "explicitly none" marker, or something an
		// older build wrote that does not name an account.
		return "", true, nil
	}
	return last.String, true, nil
}

// writeLast records an account choice; an empty id records "explicitly none".
func (s *Store) writeLast(ctx context.Context, id string) error {
	stored := any(lastNone)
	if id != "" {
		stored = id
	}
	wrapped, err := s.sealState(lastFileName, maxStateSize, lastState{
		Version: stateVersion,
		Purpose: lastPurpose,
		ID:      id,
	})
	if err != nil {
		return err
	}
	return s.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) (mirrorChange, error) {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO account_state(id, subject_key, last_account, notice_revision, updated_at)
			VALUES(1, NULL, ?, 0, ?)
			ON CONFLICT(id) DO UPDATE SET last_account = excluded.last_account, updated_at = excluded.updated_at`,
			stored, database.Stamp(s.now())); err != nil {
			return mirrorChange{}, fmt.Errorf("accounts: remember last account: %w", err)
		}
		return mirrorChange{write: map[string][]byte{lastFileName: wrapped}}, nil
	})
}

// forgetLast clears the remembered account inside an existing transaction, which
// is what removing that account does.
func (s *Store) forgetLast(ctx context.Context, tx *sql.Tx, change *mirrorChange) error {
	wrapped, err := s.sealState(lastFileName, maxStateSize, lastState{
		Version: stateVersion,
		Purpose: lastPurpose,
		ID:      "",
	})
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE account_state SET last_account = ?, updated_at = ? WHERE id = 1`,
		lastNone, database.Stamp(s.now())); err != nil {
		return fmt.Errorf("accounts: forget last account: %w", err)
	}
	if change.write == nil {
		change.write = make(map[string][]byte, 1)
	}
	change.write[lastFileName] = wrapped
	return nil
}

// NoticeAcknowledged reports whether revision was acknowledged by this user.
// Acknowledgement is bound to the exact revision: a new notice needs a new
// acknowledgement.
func (s *Store) NoticeAcknowledged(revision int) (bool, error) {
	if revision < 1 {
		return false, fmt.Errorf("%w: %d", ErrInvalidNotice, revision)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _, notice, err := s.readStateRow(context.Background())
	if err != nil {
		return false, err
	}
	return notice == int64(revision), nil
}

// AcceptNotice records that the user acknowledged revision.
func (s *Store) AcceptNotice(revision int) error {
	if revision < 1 {
		return fmt.Errorf("%w: %d", ErrInvalidNotice, revision)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	if err := s.checkDir(); err != nil {
		return err
	}
	wrapped, err := s.sealState(noticeFileName, maxNoticeSize, noticeState{
		Version:         stateVersion,
		Purpose:         noticePurpose,
		Revision:        revision,
		AcknowledgedUtc: formatTime(s.now()),
	})
	if err != nil {
		return err
	}
	return s.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) (mirrorChange, error) {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO account_state(id, subject_key, last_account, notice_revision, updated_at)
			VALUES(1, NULL, NULL, ?, ?)
			ON CONFLICT(id) DO UPDATE SET notice_revision = excluded.notice_revision, updated_at = excluded.updated_at`,
			revision, database.Stamp(s.now())); err != nil {
			return mirrorChange{}, fmt.Errorf("accounts: accept notice: %w", err)
		}
		return mirrorChange{write: map[string][]byte{noticeFileName: wrapped}}, nil
	})
}
