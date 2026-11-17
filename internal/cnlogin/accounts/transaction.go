// SPDX-License-Identifier: GPL-3.0-or-later

package accounts

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"sync"
)

// Pending is a prepared account write that has been validated and encrypted but
// not published. Prepare returns it, the caller delivers the credential to the game
// and then calls Commit, or calls Rollback when the delivery was refused. A
// prepared write never changes the committed records, so a rejected admission
// leaves the previous account exactly as it was.
type Pending interface {
	// Account is the account the write publishes.
	Account() Account
	// Commit publishes the prepared record. It is idempotent and fails after
	// Rollback.
	Commit() error
	// Rollback discards the prepared record. It is a no-op after Commit.
	Rollback() error
}

type preparedSave struct {
	store    *Store
	account  Account
	target   string   // record file name to publish
	temp     string   // durable encrypted candidate
	obsolete []string // superseded records, removed after the commit

	mu        sync.Mutex
	committed bool
	done      bool
}

// Account returns the account the write publishes.
func (t *preparedSave) Account() Account { return t.account }

// Commit renames the prepared candidate over its record and drops superseded
// duplicates.
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
	if err := t.store.checkDir(); err != nil {
		return err
	}
	if err := t.store.rename(t.temp, filepath.Join(t.store.dir, t.target)); err != nil {
		return fmt.Errorf("accounts: commit %s: %w", t.target, err)
	}
	t.committed, t.done = true, true
	for _, id := range t.obsolete {
		_ = removeRegular(filepath.Join(t.store.dir, id+recordExt), nil)
	}
	return nil
}

// Rollback removes the prepared candidate. It does nothing after Commit.
func (t *preparedSave) Rollback() error {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.done {
		return nil
	}
	t.done = true
	t.store.mu.Lock()
	defer t.store.mu.Unlock()
	if err := removeRegular(t.temp, nil); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("accounts: rollback: %w", err)
	}
	return nil
}

// Prepare validates sec, encrypts it and writes a durable candidate record
// without publishing it. A refresh of a known channel uid keeps the account id
// and the committed label; a new identity needs a label and a free slot.
func (s *Store) Prepare(a Account, sec Secret) (Pending, error) {
	tx, err := s.prepare(a, sec, false)
	if err != nil {
		return nil, err
	}
	return tx, nil
}

// prepare is the shared body of Save (which replaces the label) and Prepare.
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
	if err := s.checkDir(); err != nil {
		return nil, err
	}
	recs, _, saturated, err := s.scan()
	if err != nil {
		return nil, err
	}
	if saturated {
		return nil, ErrScanSaturated
	}
	identity := ""
	fresh := false
	if len(recs) > 0 {
		subject, created, err := s.subjectKey()
		if err != nil {
			return nil, err
		}
		identity = identityKeyFor(subject, sec.ChannelUID)
		wipeBytes(subject)
		fresh = created
	}
	dups := s.matchRecords(recs, identity, fresh, sec.ChannelUID)

	var next Account
	if len(dups) > 0 {
		next = pickDuplicate(dups).Account
		if replaceLabel && label != "" {
			next.Label = label
		}
	} else {
		if label == "" {
			return nil, ErrInvalidLabel
		}
		if len(recs) >= MaxAccounts {
			return nil, ErrTooManyAccounts
		}
		if next.ID, err = newID(); err != nil {
			return nil, err
		}
		next.Label = label
	}
	next.SavedAt = s.now().UTC().Round(0)

	data, err := s.sealNew(next, identity, sec)
	if err != nil {
		return nil, err
	}
	temp, err := s.writePending(data)
	if err != nil {
		return nil, err
	}
	obsolete := make([]string, 0, len(dups))
	for _, r := range dups {
		if r.ID != next.ID {
			obsolete = append(obsolete, r.ID)
		}
	}
	return &preparedSave{store: s, account: next, target: next.ID + recordExt, temp: temp, obsolete: obsolete}, nil
}

// matchRecords returns the records that hold the same channel identity as uid.
// Records that carry an identity key are compared without unwrapping anything;
// a record without one (written before the subject key existed, or before a lost
// key was replaced) is compared by decrypting it. Both passes are bounded by
// MaxAccounts.
func (s *Store) matchRecords(recs []record, identity string, fresh bool, uid string) []record {
	var dups []record
	if !fresh {
		for _, r := range recs {
			if identity != "" && r.identityKey == identity {
				dups = append(dups, r)
			}
		}
		if len(dups) > 0 {
			return dups
		}
	}
	for _, r := range recs {
		if !fresh && r.identityKey != "" {
			continue
		}
		if s.sameChannelUID(r, uid) {
			dups = append(dups, r)
		}
	}
	return dups
}

// pickDuplicate chooses the record a save continues: a user-renamed label wins
// over a generated masked phone, then the oldest record.
func pickDuplicate(dups []record) record {
	best := dups[0]
	for _, r := range dups[1:] {
		if betterDuplicate(r, best) {
			best = r
		}
	}
	return best
}

func betterDuplicate(a, b record) bool {
	if aMasked, bMasked := isMaskedLabel(a.Label), isMaskedLabel(b.Label); aMasked != bMasked {
		return !aMasked
	}
	if !a.SavedAt.Equal(b.SavedAt) {
		return a.SavedAt.Before(b.SavedAt)
	}
	return a.ID < b.ID
}
