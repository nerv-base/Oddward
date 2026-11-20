// SPDX-License-Identifier: GPL-3.0-or-later

package accounts

// A save is staged, not written, until the caller commits it: the launcher stages
// the account, delivers the credential to the game, and only then stores it. A
// login that never reaches the game therefore leaves no row and no mirror.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sync"

	"github.com/nerv-base/Oddward/internal/database"
)

// Pending is a save that has been staged but not stored.
type Pending interface {
	// Account returns the account the save will store.
	Account() Account
	// Commit stores the staged account and its mirrors. It is idempotent; after
	// Rollback it reports an error.
	Commit() error
	// Rollback discards the staged account. It is idempotent and does nothing
	// after Commit.
	Rollback() error
}

// preparedSave is a sealed record staged in the directory. It becomes a row, a
// mirror file and a mirror digest only in Commit, so a staged save can never be
// listed, unlocked or imported.
type preparedSave struct {
	store   *Store
	account Account
	// data is the canonical .acct byte sequence, identical to the bytes the file
	// store wrote.
	data     []byte
	temp     string
	identity string
	// obsolete are the accounts the same channel login replaced.
	obsolete []string

	mu        sync.Mutex
	committed bool
	done      bool
}

// Account returns the account the save will store.
func (t *preparedSave) Account() Account { return t.account }

// Commit writes the row, the replaced rows and the attachment removals in one
// transaction, then publishes the mirror files.
func (t *preparedSave) Commit() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.committed {
		return nil
	}
	if t.done {
		return errors.New("accounts: save was rolled back")
	}
	t.store.mu.Lock()
	defer t.store.mu.Unlock()
	change := mirrorChange{write: map[string][]byte{t.account.ID + recordExt: t.data}}
	for _, id := range t.obsolete {
		// Same order as Remove: the attachment goes before its account.
		change.remove = append(change.remove, id+tajiduoExt, id+recordExt)
	}
	err := t.store.writeTx(context.Background(), func(ctx context.Context, tx *sql.Tx) (mirrorChange, error) {
		now := database.Stamp(t.store.now())
		// A subject key generated while this save was staged has no row yet. It is
		// written here, in the same transaction as the first record derived from
		// it, so a rolled-back save leaves no state behind.
		if wrapped, err := t.store.pendingSubjectKey(); err != nil {
			return mirrorChange{}, err
		} else if wrapped != nil {
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO account_state(id, subject_key, last_account, notice_revision, updated_at)
				VALUES(1, ?, NULL, 0, ?)
				ON CONFLICT(id) DO UPDATE SET subject_key = excluded.subject_key, updated_at = excluded.updated_at`,
				wrapped, now); err != nil {
				return mirrorChange{}, fmt.Errorf("accounts: store subject key: %w", err)
			}
			change.write[subjectFileName] = wrapped
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO accounts(id, label, identity_key, saved_at, protection, record, updated_at)
			VALUES(?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(id) DO UPDATE SET
				label = excluded.label,
				identity_key = excluded.identity_key,
				saved_at = excluded.saved_at,
				protection = excluded.protection,
				record = excluded.record,
				updated_at = excluded.updated_at`,
			t.account.ID, t.account.Label, t.identity,
			database.Stamp(t.account.SavedAt), protectionKind, t.data, now); err != nil {
			return mirrorChange{}, fmt.Errorf("accounts: commit: %w", err)
		}
		for _, id := range t.obsolete {
			if _, err := tx.ExecContext(ctx, `DELETE FROM accounts WHERE id = ?`, id); err != nil {
				return mirrorChange{}, fmt.Errorf("accounts: commit: %w", err)
			}
		}
		return change, nil
	})
	if err != nil {
		return err
	}
	t.store.forgetPendingSubjectKey()
	t.committed, t.done = true, true
	// The candidate holds the same sealed bytes the row now holds; leaving it
	// behind would only make the next Open remove it.
	_ = removeRegular(t.temp, nil)
	return nil
}

// Rollback discards the staged record. The database was never touched, so this
// only removes the candidate file.
func (t *preparedSave) Rollback() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return nil
	}
	t.store.mu.Lock()
	defer t.store.mu.Unlock()
	t.done = true
	info, err := os.Lstat(t.temp)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("accounts: rollback: %w", err)
	}
	if err := removeRegular(t.temp, info); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("accounts: rollback: %w", err)
	}
	return nil
}

// Prepare stages a save without storing it. The caller commits only after the
// credential was delivered, so a failed login leaves no account.
func (s *Store) Prepare(a Account, sec Secret) (Pending, error) {
	return s.prepare(a, sec, false)
}

// prepare is the shared body of Save and Prepare. replaceLabel decides whether a
// save that replaces an existing account also replaces its label.
func (s *Store) prepare(a Account, sec Secret, replaceLabel bool) (*preparedSave, error) {
	if err := sec.validate(); err != nil {
		return nil, err
	}
	label := ""
	if a.Label != "" {
		sanitized, err := sanitizeLabel(a.Label)
		if err != nil {
			return nil, err
		}
		label = sanitized
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	if err := s.checkDir(); err != nil {
		return nil, err
	}
	recs, _, err := s.storedRecords(ctx)
	if err != nil {
		return nil, err
	}
	var (
		next     Account
		identity string
		dups     []record
		fresh    bool
	)
	if len(recs) > 0 {
		var key []byte
		key, fresh, err = s.subjectKey(ctx)
		if err != nil {
			return nil, err
		}
		identity = identityKeyFor(key, sec.ChannelUID)
		wipeBytes(key)
		dups = s.matchRecords(recs, identity, fresh, sec.ChannelUID)
	}
	switch {
	case len(dups) > 0:
		next = pickDuplicate(dups).Account
		if replaceLabel && label != "" {
			next.Label = label
		}
	case label == "":
		// A new account is a new picker entry, so it needs a label the user can
		// recognise.
		return nil, fmt.Errorf("%w: a new account needs a label", ErrInvalidLabel)
	case len(recs) >= MaxAccounts:
		return nil, ErrTooManyAccounts
	default:
		id, err := newID()
		if err != nil {
			return nil, fmt.Errorf("accounts: new id: %w", err)
		}
		next = Account{ID: id, Label: label}
	}
	next.SavedAt = s.now().UTC().Round(0)
	// The record is sealed from the canonical values only: the AAD binds id,
	// label, saved-at and identity key, so anything derived differently would
	// not verify when it is read back.
	data, err := s.sealNew(next, identity, sec)
	if err != nil {
		return nil, err
	}
	temp, err := s.writePending(data)
	if err != nil {
		return nil, err
	}
	var obsolete []string
	for _, r := range dups {
		if r.ID != next.ID {
			obsolete = append(obsolete, r.ID)
		}
	}
	return &preparedSave{
		store:    s,
		account:  next,
		data:     data,
		temp:     temp,
		identity: identityColumn(next.ID, identity),
		obsolete: obsolete,
	}, nil
}

// matchRecords finds the accounts one save replaces.
//
// The identity_key column holds the HMAC of the channel identity, so a save that
// repeats it is the same account and needs no decryption; identity_key is unique,
// which is what replaced the directory scan. A record written before a subject
// key existed carries no identity key and can only be recognised by decrypting
// it, which is also the only way to tell before the first key exists at all.
func (s *Store) matchRecords(recs []storedRecord, identity string, fresh bool, uid string) []record {
	var dups []record
	if !fresh {
		for _, r := range recs {
			if r.identity == identity {
				dups = append(dups, r.record)
			}
		}
		if len(dups) > 0 {
			return dups
		}
	}
	for _, r := range recs {
		if !fresh && r.identity != keylessIdentity(r.ID) {
			continue
		}
		if s.sameChannelUID(r.record, uid) {
			dups = append(dups, r.record)
		}
	}
	return dups
}

// pickDuplicate chooses the account to keep when several rows hold the same
// channel login.
func pickDuplicate(dups []record) record {
	best := dups[0]
	for _, r := range dups[1:] {
		if betterDuplicate(r, best) {
			best = r
		}
	}
	return best
}

// betterDuplicate reports whether a should be kept instead of b: an account with
// a real label beats a masked one, then the oldest save wins, then the lowest id,
// so the choice never depends on iteration order.
func betterDuplicate(a, b record) bool {
	aMasked, bMasked := isMaskedLabel(a.Label), isMaskedLabel(b.Label)
	if aMasked != bMasked {
		return bMasked
	}
	if !a.SavedAt.Equal(b.SavedAt) {
		return a.SavedAt.Before(b.SavedAt)
	}
	return a.ID < b.ID
}
