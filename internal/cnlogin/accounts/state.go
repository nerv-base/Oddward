// SPDX-License-Identifier: GPL-3.0-or-later

package accounts

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
)

const (
	stateVersion = 1
	// maxStateSize bounds every protected state file, including the login
	// notice the upstream format caps at 1024 bytes.
	maxStateSize  = 4096
	maxNoticeSize = 1024

	subjectFileName = "subject-key.bin"
	lastFileName    = "last-login.bin"
	noticeFileName  = "notice.bin"

	subjectPurpose = "subject-key"
	lastPurpose    = "last-login"
	noticePurpose  = "login-notice"

	subjectKeySize = 32
)

// errState is returned when a protected state file does not hold the document
// its name promises.
var errState = errors.New("accounts: state file is not recognized")

// Every state file is the protector-wrapped JSON of one of these documents. The
// purpose string is inside the wrapped bytes, so a blob copied to another state
// file is rejected instead of being misread as that state.
type subjectState struct {
	Version int    `json:"version"`
	Purpose string `json:"purpose"`
	Key     []byte `json:"key"`
}

type lastState struct {
	Version int    `json:"version"`
	Purpose string `json:"purpose"`
	ID      string `json:"id"`
}

type noticeState struct {
	Version         int    `json:"version"`
	Purpose         string `json:"purpose"`
	Revision        int    `json:"revision"`
	AcknowledgedUtc string `json:"acknowledgedUtc"`
}

// readState reads and unwraps a protected state file. present reports that the
// file exists even when it cannot be unwrapped; ok reports that the plaintext is
// usable. A blob wrapped by another user's protector is indistinguishable from
// no state, so it is reported as present but unusable: a remembered account
// from another user is forgotten, not guessed.
func (s *Store) readState(name string, limit int) (plain []byte, present, ok bool) {
	raw, err := s.readFile(name, limit)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, false
	}
	if err != nil {
		return nil, true, false
	}
	plain, err = s.p.Unprotect(raw)
	if err != nil {
		return nil, true, false
	}
	return plain, true, true
}

// writeState seals doc and replaces the state file name atomically. limit
// bounds the wrapped file so a state document can never outgrow its reader.
func (s *Store) writeState(name string, limit int, doc any) error {
	plain, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	defer wipeBytes(plain)
	wrapped, err := s.p.Protect(plain)
	if err != nil {
		return fmt.Errorf("accounts: protect %s: %w", name, err)
	}
	if len(wrapped) > limit {
		return fmt.Errorf("accounts: protected %s is %d bytes", name, len(wrapped))
	}
	return s.writeAtomic(name, wrapped)
}

// decodeState checks the envelope of a state document and decodes it strictly.
func decodeState(plain []byte, purpose string, doc any) error {
	var header struct {
		Version int    `json:"version"`
		Purpose string `json:"purpose"`
	}
	if err := json.Unmarshal(plain, &header); err != nil {
		return errState
	}
	if header.Version != stateVersion || header.Purpose != purpose {
		return errState
	}
	dec := json.NewDecoder(bytes.NewReader(plain))
	dec.DisallowUnknownFields()
	if err := dec.Decode(doc); err != nil {
		return errState
	}
	if _, err := dec.Token(); err != io.EOF {
		return errState
	}
	return nil
}

// subjectKey loads the store's de-duplication key, creating it when missing.
// created reports that the key is new: every existing record then carries an
// identity key derived under another key (or none), so those records have to be
// compared by decrypting them once.
func (s *Store) subjectKey() (key []byte, created bool, err error) {
	if plain, _, ok := s.readState(subjectFileName, maxStateSize); ok {
		var st subjectState
		err := decodeState(plain, subjectPurpose, &st)
		wipeBytes(plain)
		if err == nil && len(st.Key) == subjectKeySize {
			return st.Key, false, nil
		}
		// A corrupt or foreign key file is replaced: the key is derived state,
		// not user data, and every record stays readable without it.
		wipeBytes(st.Key)
	}
	key = make([]byte, subjectKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, false, err
	}
	st := subjectState{Version: stateVersion, Purpose: subjectPurpose, Key: key}
	if err := s.writeState(subjectFileName, maxStateSize, st); err != nil {
		wipeBytes(key)
		return nil, false, err
	}
	return key, true, nil
}

// readLast reports the remembered account id and whether a state file exists.
// An unreadable or malformed file means "remember none" and still counts as
// state, so a store that forgot explicitly never falls back to its only account.
func (s *Store) readLast() (id string, state bool) {
	plain, present, ok := s.readState(lastFileName, maxStateSize)
	if !ok {
		return "", present
	}
	var st lastState
	err := decodeState(plain, lastPurpose, &st)
	wipeBytes(plain)
	if err != nil || !isLowerHex(st.ID, 32) {
		return "", true
	}
	return st.ID, true
}

// writeLast remembers id, or forgets it when id is empty. Both are protected
// state: "none" is recorded, never left to inference.
func (s *Store) writeLast(id string) error {
	return s.writeState(lastFileName, maxStateSize, lastState{Version: stateVersion, Purpose: lastPurpose, ID: id})
}

// NoticeAcknowledged reports whether exactly revision was accepted.
func (s *Store) NoticeAcknowledged(revision int) (bool, error) {
	if revision < 1 {
		return false, fmt.Errorf("%w: revision %d", ErrInvalidNotice, revision)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkDir(); err != nil {
		return false, err
	}
	plain, _, ok := s.readState(noticeFileName, maxNoticeSize)
	if !ok {
		return false, nil
	}
	var st noticeState
	err := decodeState(plain, noticePurpose, &st)
	wipeBytes(plain)
	if err != nil {
		// An unreadable notice state is treated as not acknowledged, so the
		// notice is shown again instead of being skipped.
		return false, nil
	}
	return st.Revision == revision, nil
}

// AcceptNotice records that revision was shown and accepted.
func (s *Store) AcceptNotice(revision int) error {
	if revision < 1 {
		return fmt.Errorf("%w: revision %d", ErrInvalidNotice, revision)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkDir(); err != nil {
		return err
	}
	return s.writeState(noticeFileName, maxNoticeSize, noticeState{
		Version:         stateVersion,
		Purpose:         noticePurpose,
		Revision:        revision,
		AcknowledgedUtc: formatTime(s.now()),
	})
}
