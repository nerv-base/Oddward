// SPDX-License-Identifier: GPL-3.0-or-later

package accounts

// The importer is the one-way migration from the file store: the first start of a
// database build reads the accounts the previous build left on disk, and a later
// start reads them again only when a mirror file no longer matches what this
// build wrote (the launcher was rolled back, the user logged in there, and the
// launcher came forward again).

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/nerv-base/Oddward/internal/database"
)

// legacyRecord is one parsed .acct file. raw is kept byte for byte: the record
// ciphertext is bound to its public fields by the AAD, so re-encoding it would
// make it unverifiable.
type legacyRecord struct {
	record
	raw     []byte
	modTime time.Time
}

// legacyAttachment is one .tjd file beside its record.
type legacyAttachment struct {
	id      string
	center  string
	raw     []byte
	modTime time.Time
}

// legacyState is the device state read from the three protected state files. A
// field applies only when its file exists: a missing file keeps what the
// database already holds, because the file store never invented state, it only
// wrote what a user action recorded.
type legacyState struct {
	subject    []byte         // the protected subject key, nil when unusable
	subjectSet bool           // subject-key.bin exists and is a usable key
	last       sql.NullString // NULL when the file records no usable choice
	lastSet    bool           // last-login.bin exists
	notice     int64
	noticeSet  bool // notice.bin exists
}

// ImportLegacy reconciles the rows with the legacy files the previous build
// wrote. The launcher calls it at startup: RunImport skips the work when the
// import already completed and no mirror file changed, so a normal start only
// publishes the index it compares against.
func (s *Store) ImportLegacy(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := os.Lstat(s.dir); errors.Is(err, fs.ErrNotExist) {
		// The directory was removed: there is nothing to read and nowhere to
		// publish a mirror, and the rows remain the only authority.
		return nil
	}
	if err := s.checkDir(); err != nil {
		return err
	}
	// Publish the index before the staleness check. A file another build changed,
	// added or deleted changes the digest, so RunImport re-runs the importer
	// instead of trusting rows that no longer describe the files.
	changed, err := s.mirrorChanged(ctx)
	if err != nil {
		return err
	}
	if !changed {
		// The files are exactly what this build published. A row written after
		// them is this build's own write whose file publication failed, so the
		// rows are the authority and the files are repaired from them. Importing
		// there would undo the account the caller was told had not been saved.
		ahead, err := s.rowsAreAhead(ctx)
		if err != nil {
			return err
		}
		if ahead {
			if err := s.republishFromRows(ctx); err != nil {
				return err
			}
		}
	}
	// Publish the index before the staleness check: a file another build edited,
	// added or deleted changes it and makes RunImport re-run the importer.
	if err := s.refreshMirrorIndex(); err != nil {
		return err
	}
	_, err = s.db.RunImport(ctx, accountsDomain, legacyVersion, s.importLegacy)
	return err
}

// importLegacy replaces the rows with the legacy files. Every file is parsed
// before the transaction starts, so a file that does not parse leaves the files
// and the rows untouched.
func (s *Store) importLegacy(ctx context.Context) (string, string, error) {
	records, attachments, err := s.readLegacyAccounts()
	if err != nil {
		return "", "", err
	}
	state := s.readLegacyState()
	if len(records) == 0 && !state.subjectSet && !state.lastSet && !state.noticeSet {
		// No legacy source. The rows stay as they are and only the index is
		// recorded, so the next start does not consider the directory stale
		// forever.
		err := s.writeTx(ctx, func(context.Context, *sql.Tx) (mirrorChange, error) {
			return mirrorChange{}, nil
		})
		return "legacy-files", "no legacy account files", err
	}
	err = s.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) (mirrorChange, error) {
		// The files describe the whole account set, so they replace it. An empty
		// set is the previous build deleting the last account, which must delete
		// the row too: resurrecting removed credentials would be a leak, not a
		// rescue. A directory with no legacy files at all returned above.
		if _, err := tx.ExecContext(ctx, `DELETE FROM accounts`); err != nil {
			return mirrorChange{}, fmt.Errorf("accounts: import: %w", err)
		}
		insertAccount := `INSERT INTO accounts(id, label, identity_key, saved_at, protection, record, updated_at) VALUES(?, ?, ?, ?, ?, ?, ?)`
		for _, r := range records {
			// updated_at is the file's own age, not the import time: a row written
			// later than its file means this build's failed publication, and the row
			// then wins. Stamping the import time would make every import look newer
			// than the files and hide the next genuine rollback edit.
			if _, err := tx.ExecContext(ctx, insertAccount,
				r.ID, r.Label, identityColumn(r.ID, r.identityKey),
				database.Stamp(r.SavedAt), protectionKind, r.raw,
				database.Stamp(r.modTime)); err != nil {
				return mirrorChange{}, fmt.Errorf("accounts: import: %w", err)
			}
		}
		insertAttachment := `INSERT INTO account_tajiduo(account_id, center_uid, file, updated_at) VALUES(?, ?, ?, ?)`
		for _, a := range attachments {
			if _, err := tx.ExecContext(ctx, insertAttachment,
				a.id, a.center, a.raw, database.Stamp(a.modTime)); err != nil {
				return mirrorChange{}, fmt.Errorf("accounts: import: %w", err)
			}
		}
		if err := s.replaceState(ctx, tx, state); err != nil {
			return mirrorChange{}, err
		}
		return mirrorChange{}, nil
	})
	if err != nil {
		return "", "", err
	}
	return "legacy-files", fmt.Sprintf("imported %d accounts, %d attachments", len(records), len(attachments)), nil
}

// replaceState writes the device state the files describe and keeps every value
// whose file is absent, so a rollback reconcile never rotates key material the
// database already derived identity keys from.
func (s *Store) replaceState(ctx context.Context, tx *sql.Tx, state legacyState) error {
	var (
		subject []byte
		last    sql.NullString
		notice  int64
	)
	err := tx.QueryRowContext(ctx, `SELECT subject_key, last_account, notice_revision FROM account_state WHERE id = 1`).
		Scan(&subject, &last, &notice)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		// A fresh database: the imported files are the whole state.
	case err != nil:
		return fmt.Errorf("accounts: import state: %w", err)
	}
	if state.subjectSet {
		subject = state.subject
	}
	if state.lastSet {
		last = state.last
	}
	if state.noticeSet {
		notice = state.notice
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO account_state(id, subject_key, last_account, notice_revision, updated_at)
		VALUES(1, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			subject_key = excluded.subject_key,
			last_account = excluded.last_account,
			notice_revision = excluded.notice_revision,
			updated_at = excluded.updated_at`,
		subject, last, notice, database.Stamp(s.now())); err != nil {
		return fmt.Errorf("accounts: import state: %w", err)
	}
	return nil
}

// readLegacyAccounts parses every legacy record and attachment.
//
// A record that does not parse, an attachment beside no record, or two records
// that claim the same channel identity is an error naming the file: the files are
// reconciled as one set, so importing a partial set would silently drop the rest.
func (s *Store) readLegacyAccounts() ([]legacyRecord, []legacyAttachment, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("accounts: read legacy directory: %w", err)
	}
	var recordNames, attachmentNames []string
	for _, e := range entries {
		name := e.Name()
		if id, ok := strings.CutSuffix(name, recordExt); ok && isLowerHex(id, 32) {
			recordNames = append(recordNames, name)
			continue
		}
		if id, ok := strings.CutSuffix(name, tajiduoExt); ok && isLowerHex(id, 32) {
			attachmentNames = append(attachmentNames, name)
		}
	}
	sort.Strings(recordNames)
	sort.Strings(attachmentNames)

	times := make(map[string]time.Time)
	for _, e := range entries {
		if info, err := e.Info(); err == nil {
			times[e.Name()] = info.ModTime()
		}
	}
	records := make([]legacyRecord, 0, len(recordNames))
	ids := make(map[string]bool, len(recordNames))
	identities := make(map[string]string, len(recordNames))
	for _, name := range recordNames {
		id := strings.TrimSuffix(name, recordExt)
		raw, err := s.readFile(name, MaxRecordSize)
		if err != nil {
			return nil, nil, fmt.Errorf("accounts: import %s: %w", name, err)
		}
		r, err := parseRecord(id, raw)
		if err != nil {
			return nil, nil, fmt.Errorf("accounts: import %s: %w", name, err)
		}
		if r.identityKey != "" {
			if other, ok := identities[r.identityKey]; ok {
				return nil, nil, fmt.Errorf("accounts: import %s: %w: same channel identity as %s", name, ErrCorrupt, other)
			}
			identities[r.identityKey] = name
		}
		ids[id] = true
		records = append(records, legacyRecord{record: r, raw: raw, modTime: times[name]})
	}
	attachments := make([]legacyAttachment, 0, len(attachmentNames))
	for _, name := range attachmentNames {
		id := strings.TrimSuffix(name, tajiduoExt)
		if !ids[id] {
			// The file store counted an attachment beside no record as corrupt;
			// importing it would create an account that never existed.
			return nil, nil, fmt.Errorf("accounts: import %s: %w: attachment has no account", name, ErrCorrupt)
		}
		raw, err := s.readFile(name, MaxRecordSize)
		if err != nil {
			return nil, nil, fmt.Errorf("accounts: import %s: %w", name, err)
		}
		// An attachment that does not parse is stored as it is: List reports it
		// as needing a new link, and dropping it would hide that a link exists.
		var center string
		if f, err := parseTajiduo(id, raw); err == nil {
			center = f.CenterUID
		}
		attachments = append(attachments, legacyAttachment{id: id, center: center, raw: raw, modTime: times[name]})
	}
	return records, attachments, nil
}

// readLegacyState reads the three protected state files. A file that exists but
// cannot be unwrapped still counts as present where the file store treated it as
// a recorded state (last-login and notice); an unusable subject key is not
// imported, because the row's key is the only source of identity keys.
func (s *Store) readLegacyState() legacyState {
	var state legacyState
	if raw, ok := s.readLegacyStateFile(subjectFileName, maxStateSize); ok && raw != nil {
		if plain, err := s.p.Unprotect(raw); err == nil {
			var doc subjectState
			if decodeState(plain, subjectPurpose, &doc) == nil && len(doc.Key) == subjectKeySize {
				state.subject, state.subjectSet = raw, true
			}
			wipeBytes(doc.Key)
			wipeBytes(plain)
		}
	}
	if raw, ok := s.readLegacyStateFile(lastFileName, maxStateSize); ok {
		// A recorded choice that cannot be unwrapped still means a choice was
		// made; the file store treated it the same way.
		state.last = sql.NullString{String: lastNone, Valid: true}
		state.lastSet = true
		if raw != nil {
			if plain, err := s.p.Unprotect(raw); err == nil {
				var doc lastState
				if decodeState(plain, lastPurpose, &doc) == nil && isLowerHex(doc.ID, 32) {
					state.last = sql.NullString{String: doc.ID, Valid: true}
				}
				wipeBytes(plain)
			}
		}
	}
	if raw, ok := s.readLegacyStateFile(noticeFileName, maxNoticeSize); ok {
		state.noticeSet = true
		if raw != nil {
			if plain, err := s.p.Unprotect(raw); err == nil {
				var doc noticeState
				if decodeState(plain, noticePurpose, &doc) == nil && doc.Revision > 0 {
					state.notice = int64(doc.Revision)
				}
				wipeBytes(plain)
			}
		}
	}
	return state
}

// readLegacyStateFile reads one protected state file and reports whether it
// exists. A file that exists but cannot be read as a plain file is present and
// unusable.
func (s *Store) readLegacyStateFile(name string, limit int) (raw []byte, present bool) {
	raw, err := s.readFile(name, limit)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false
	}
	if err != nil {
		return nil, true
	}
	return raw, true
}
