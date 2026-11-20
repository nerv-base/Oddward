// SPDX-License-Identifier: GPL-3.0-or-later

package accounts

// The database is authoritative. The directory beside it keeps the legacy files
// an older build reads, see mirror.go; those files are never read as an
// authority, only mirrored and imported.

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/nerv-base/Oddward/internal/database"
)

const (
	// MaxAccounts bounds how many accounts the store accepts, so the list stays
	// a picker and not an archive.
	MaxAccounts = 16
	// MaxRecordSize bounds one stored record, mirror file included.
	MaxRecordSize = 16 << 10
	// MaxLabelRunes bounds a user-chosen label.
	MaxLabelRunes = 32

	// minPendingBytes is shorter than any complete sealed document, so a shorter
	// temp file is the torn remainder of an interrupted write.
	minPendingBytes = 16
	// tempPrefix marks a staged save. Open removes leftovers: nothing outside a
	// commit may become visible, so a candidate only ever lives until its
	// Commit or Rollback.
	tempPrefix = ".pending-"
	tempSuffix = ".tmp"
)

var (
	// ErrNotFound reports an account id that is not stored.
	ErrNotFound = errors.New("accounts: account not found")
	// ErrCorrupt reports a stored record or attachment that does not validate.
	ErrCorrupt = errors.New("accounts: corrupt account record")
	// ErrUnlock reports a record that decrypts to unusable bytes, which is what
	// a tampered row and a row sealed under a different Windows user look like.
	ErrUnlock = errors.New("accounts: cannot unlock account")
	// ErrInvalidLabel reports a label that cannot be stored.
	ErrInvalidLabel = errors.New("accounts: invalid label")
	// ErrInvalidSecret reports a secret shaped unlike a game login.
	ErrInvalidSecret = errors.New("accounts: invalid secret")
	// ErrInvalidPhone reports a phone number that cannot be masked.
	ErrInvalidPhone = errors.New("accounts: invalid phone number")
	// ErrInvalidNotice reports a notice revision that cannot be stored.
	ErrInvalidNotice = errors.New("accounts: invalid notice revision")
	// ErrTooManyAccounts reports that MaxAccounts accounts already exist.
	ErrTooManyAccounts = errors.New("accounts: too many accounts")
	// ErrUnsafePath reports a path that is not a plain file or directory, or a
	// mirror file that cannot be touched without following a reparse point.
	ErrUnsafePath = errors.New("accounts: unsafe path")

	// errNotRegular and errTooLarge are the two failures a mirror file can show
	// that the importer has to report with the file name attached.
	errNotRegular = errors.New("not a regular file")
	errTooLarge   = errors.New("file is too large")
)

// KeyProtector wraps and unwraps the per-record data keys. The production
// implementation is DPAPI under the current Windows user (DPAPI).
type KeyProtector interface {
	// Protect wraps plain so that only the current user can unwrap it.
	Protect(plain []byte) ([]byte, error)
	// Unprotect unwraps a value produced by Protect.
	Unprotect(wrapped []byte) ([]byte, error)
}

// Account is the non-secret part of a stored login. The Tajiduo fields are
// derived from the attachment, never stored inside the record.
type Account struct {
	ID            string
	Label         string
	SavedAt       time.Time
	TajiduoLinked bool
	// TajiduoState is empty when no attachment is stored, "linked" when one
	// decrypts under this user, and "needs-relink" when one exists but cannot
	// be used.
	TajiduoState string
}

// Store keeps the encrypted accounts in the database and mirrors them into dir
// for an older build. One process owns it; methods are safe for concurrent use
// within that process.
type Store struct {
	db  *database.DB
	dir string
	p   KeyProtector

	mu  sync.Mutex
	now func() time.Time
	// pending is the subject key a staged save generated before the database had
	// one. It is state, not account data, so it is stored by the first Commit
	// that needs it; a save that is rolled back leaves no row behind.
	pending []byte
}

// Open validates dir and prepares it for the mirrors. db must already be
// migrated; the launcher runs ImportLegacy before it trusts the rows.
//
// The directory is created with a private DACL when missing because it holds the
// mirror files; the database file protects itself (internal/database).
func Open(db *database.DB, dir string, p KeyProtector) (*Store, error) {
	if db == nil {
		return nil, errors.New("accounts: nil database")
	}
	if p == nil {
		return nil, errors.New("accounts: nil KeyProtector")
	}
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("%w: %q is not absolute", ErrUnsafePath, dir)
	}
	s := &Store{
		db:  db,
		dir: filepath.Clean(dir),
		p:   p,
		now: time.Now,
	}
	if err := s.checkAncestors(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(s.dir), 0o700); err != nil {
		return nil, fmt.Errorf("accounts: create parent directory: %w", err)
	}
	// An existing directory is validated by checkDir instead: mkdir with a
	// private DACL only applies to the one Open creates, and re-running Open on
	// an existing directory is the normal startup after a restart.
	if _, err := os.Lstat(s.dir); errors.Is(err, fs.ErrNotExist) {
		if err := privateMkdir(s.dir); err != nil {
			return nil, fmt.Errorf("accounts: create directory: %w", err)
		}
	}
	if err := s.checkDir(); err != nil {
		return nil, err
	}
	if err := protectDirectory(s.dir); err != nil {
		return nil, fmt.Errorf("accounts: protect directory: %w", err)
	}
	if err := s.removeStaleTemps(); err != nil {
		return nil, err
	}
	return s, nil
}

// checkAncestors rejects a directory below a reparse point: a link anywhere on
// the way in would redirect every mirror write. A parent that does not exist yet
// is fine, Open creates the chain.
func (s *Store) checkAncestors() error {
	dir := s.dir
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil
		}
		info, err := os.Lstat(parent)
		switch {
		case errors.Is(err, fs.ErrNotExist):
		case err != nil:
			return fmt.Errorf("accounts: inspect path: %w", err)
		case !info.IsDir() || isReparse(info):
			return fmt.Errorf("%w: %s is not a plain directory", ErrUnsafePath, parent)
		}
		dir = parent
	}
}

// checkDir rejects a directory that was swapped for a link or a file after Open.
func (s *Store) checkDir() error {
	info, err := os.Lstat(s.dir)
	if err != nil {
		return fmt.Errorf("accounts: inspect directory: %w", err)
	}
	if !info.IsDir() || isReparse(info) {
		return fmt.Errorf("%w: %s is not a plain directory", ErrUnsafePath, s.dir)
	}
	return nil
}

// removeStaleTemps drops candidates an interrupted process left behind. A
// candidate that already holds a complete sealed record is abandoned too: its
// save never committed, so dropping it is the same as a rollback.
func (s *Store) removeStaleTemps() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return fmt.Errorf("accounts: read directory: %w", err)
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, tempPrefix) || !strings.HasSuffix(name, tempSuffix) {
			continue
		}
		info, err := os.Lstat(filepath.Join(s.dir, name))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return fmt.Errorf("accounts: inspect temp file: %w", err)
		}
		if !s.abandonedPending(info) {
			// Something that merely carries the prefix is not provably ours, so
			// it is left alone instead of being deleted or refused.
			continue
		}
		if err := removeRegular(filepath.Join(s.dir, name), info); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("accounts: remove temp file: %w", err)
		}
	}
	return nil
}

// abandonedPending reports whether a temp file is a save this store abandoned: a
// torn write, or a complete sealed record that was never committed.
func (s *Store) abandonedPending(info os.FileInfo) bool {
	if !info.Mode().IsRegular() {
		return false
	}
	if info.Size() < minPendingBytes {
		// Shorter than any complete document: the remainder of a torn write.
		return true
	}
	if info.Size() > MaxRecordSize {
		return false
	}
	raw, err := s.readFile(info.Name(), MaxRecordSize)
	if err != nil {
		return false
	}
	_, err = parseRecordBytes(raw)
	return err == nil
}

// List returns the readable accounts, newest first, and how many rows hold a
// record that no longer validates. Those rows are skipped here and can still be
// removed.
func (s *Store) List() ([]Account, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	if err := s.checkDir(); err != nil {
		return nil, 0, err
	}
	recs, corrupt, err := s.storedRecords(ctx)
	if err != nil {
		return nil, 0, err
	}
	attachments, err := s.attachments(ctx)
	if err != nil {
		return nil, 0, err
	}
	out := make([]Account, 0, len(recs))
	for _, r := range recs {
		a := r.Account
		a.TajiduoLinked, a.TajiduoState = s.tajiduoState(r.ID, attachments[r.ID])
		out = append(out, a)
	}
	return out, corrupt, nil
}

// storedRecord is one accounts row: the parsed record and the identity_key
// column, which is the record's own identity key or a placeholder when the
// record carries none.
type storedRecord struct {
	record
	identity string
}

// storedRecords reads every account row, newest first. A row whose record does
// not parse is counted and skipped.
func (s *Store) storedRecords(ctx context.Context) ([]storedRecord, int, error) {
	rows, err := s.db.Query(ctx, `SELECT id, identity_key, record FROM accounts ORDER BY saved_at DESC, id`)
	if err != nil {
		return nil, 0, fmt.Errorf("accounts: list: %w", err)
	}
	defer rows.Close()
	var (
		recs    []storedRecord
		corrupt int
	)
	for rows.Next() {
		var (
			id  string
			key string
			raw []byte
		)
		if err := rows.Scan(&id, &key, &raw); err != nil {
			return nil, 0, fmt.Errorf("accounts: list: %w", err)
		}
		r, err := parseRecord(id, raw)
		if err != nil {
			corrupt++
			continue
		}
		recs = append(recs, storedRecord{record: r, identity: key})
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("accounts: list: %w", err)
	}
	return recs, corrupt, nil
}

// attachments reads the stored Tajiduo files by account id.
func (s *Store) attachments(ctx context.Context) (map[string][]byte, error) {
	rows, err := s.db.Query(ctx, `SELECT account_id, file FROM account_tajiduo`)
	if err != nil {
		return nil, fmt.Errorf("accounts: list attachments: %w", err)
	}
	defer rows.Close()
	out := make(map[string][]byte)
	for rows.Next() {
		var (
			id  string
			raw []byte
		)
		if err := rows.Scan(&id, &raw); err != nil {
			return nil, fmt.Errorf("accounts: list attachments: %w", err)
		}
		out[id] = raw
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("accounts: list attachments: %w", err)
	}
	return out, nil
}

// Save stores sec and returns the account it belongs to. A save whose identity
// key is already stored updates that account; a new account needs a label.
func (s *Store) Save(a Account, sec Secret) (Account, error) {
	tx, err := s.prepare(a, sec, true)
	if err != nil {
		return Account{}, err
	}
	if err := tx.Commit(); err != nil {
		return Account{}, err
	}
	return tx.account, nil
}

// soleAccount reports the only readable account, or "" when there are none or
// several.
func (s *Store) soleAccount(ctx context.Context) (string, error) {
	recs, _, err := s.storedRecords(ctx)
	if err != nil {
		return "", err
	}
	if len(recs) != 1 {
		return "", nil
	}
	return recs[0].ID, nil
}

// sameChannelUID reports whether a stored record belongs to the same channel
// login. The comparison is constant time so a save does not leak how many bytes
// of a ChannelUID matched.
func (s *Store) sameChannelUID(r record, uid string) bool {
	_, plain, err := s.open(r)
	if err != nil {
		return false
	}
	sec, err := decodePayload(plain)
	wipeBytes(plain)
	if err != nil {
		return false
	}
	defer sec.Wipe()
	return subtle.ConstantTimeCompare([]byte(sec.ChannelUID), []byte(uid)) == 1
}

// sealNew seals sec under a fresh random data key and builds the canonical
// record bytes. The key exists only inside this call and inside the sealed
// record, so the database never holds a key the DPAPI wrapper does not protect.
func (s *Store) sealNew(a Account, identity string, sec Secret) ([]byte, error) {
	key := make([]byte, dataKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("accounts: generate data key: %w", err)
	}
	defer wipeBytes(key)
	wrapped, err := s.p.Protect(key)
	if err != nil {
		return nil, fmt.Errorf("accounts: protect data key: %w", err)
	}
	if len(wrapped) == 0 || len(wrapped) > maxWrappedSize {
		return nil, fmt.Errorf("accounts: protect data key: %w: unexpected wrapped size %d", ErrCorrupt, len(wrapped))
	}
	// A protector that cannot unwrap its own output would produce a record
	// nothing can ever read; refuse before writing anything.
	back, err := s.p.Unprotect(wrapped)
	if err != nil {
		return nil, fmt.Errorf("accounts: verify data key: %w", err)
	}
	ok := len(back) == dataKeySize && subtle.ConstantTimeCompare(back, key) == 1
	wipeBytes(back)
	if !ok {
		return nil, fmt.Errorf("accounts: verify data key: %w: round trip mismatch", ErrCorrupt)
	}
	plain, err := encodePayload(sec)
	if err != nil {
		return nil, err
	}
	defer wipeBytes(plain)
	r := record{Account: a, identityKey: identity, wrapped: wrapped}
	if err := seal(&r, key, plain); err != nil {
		return nil, err
	}
	return encodeRecord(r)
}

// open decrypts one record and returns its data key and plaintext.
func (s *Store) open(r record) (key, plain []byte, err error) {
	key, err = s.p.Unprotect(r.wrapped)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: unprotect data key: %w", ErrUnlock, err)
	}
	if len(key) != dataKeySize {
		wipeBytes(key)
		return nil, nil, fmt.Errorf("%w: data key has %d bytes", ErrCorrupt, len(key))
	}
	aead, err := newGCM(key)
	if err != nil {
		wipeBytes(key)
		return nil, nil, err
	}
	plain, err = aead.Open(nil, r.nonce, r.sealed, recordAAD(r.Account, r.identityKey))
	if err != nil {
		wipeBytes(key)
		return nil, nil, fmt.Errorf("%w: open record: %w", ErrUnlock, err)
	}
	return key, plain, nil
}

// Unlock decrypts the stored secret of one account.
func (s *Store) Unlock(id string) (Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	if err := s.checkDir(); err != nil {
		return Secret{}, err
	}
	r, err := s.load(ctx, id)
	if err != nil {
		return Secret{}, err
	}
	return s.unlock(r)
}

// unlock decrypts one already-loaded record. The caller holds s.mu.
func (s *Store) unlock(r record) (Secret, error) {
	_, plain, err := s.open(r)
	if err != nil {
		return Secret{}, err
	}
	defer wipeBytes(plain)
	return decodePayload(plain)
}

// Rename changes the label of a stored account.
func (s *Store) Rename(id, label string) error {
	label, err := sanitizeLabel(label)
	if err != nil {
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
	defer wipeBytes(key)
	defer wipeBytes(plain)
	// The label is part of the AAD, so the record has to be sealed again; the
	// data key stays the same and SavedAt keeps the account's order.
	r.Label = label
	if err := seal(&r, key, plain); err != nil {
		return err
	}
	data, err := encodeRecord(r)
	if err != nil {
		return err
	}
	return s.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) (mirrorChange, error) {
		if _, err := tx.ExecContext(ctx, `UPDATE accounts SET label = ?, record = ?, updated_at = ? WHERE id = ?`,
			r.Label, data, database.Stamp(s.now()), r.ID); err != nil {
			return mirrorChange{}, fmt.Errorf("accounts: rename: %w", err)
		}
		return mirrorChange{write: map[string][]byte{r.ID + recordExt: data}}, nil
	})
}

// Remove deletes a stored account and its mirrors. Removing an account that is
// not stored reports ErrNotFound; removing its files twice does not fail.
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	if !isLowerHex(id, 32) {
		return ErrNotFound
	}
	if err := s.checkDir(); err != nil {
		return err
	}
	// Refuse before the rows go: a mirror swapped for a reparse point must never
	// be followed, and the account must stay when its files cannot be cleaned
	// up. A missing mirror is fine, the database is the authority.
	path := s.mirrorPath(id + recordExt)
	fi, err := os.Lstat(path)
	switch {
	case err == nil:
		if !fi.Mode().IsRegular() || isReparse(fi) {
			return fmt.Errorf("%w: %s is not a plain file", ErrUnsafePath, path)
		}
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return fmt.Errorf("accounts: remove: %w", err)
	}
	// The attachment goes first: an interruption between the two removals leaves
	// an account without a Tajiduo link, never an orphan attachment.
	removed := []string{id + tajiduoExt, id + recordExt}
	return s.writeTx(ctx, func(ctx context.Context, tx *sql.Tx) (mirrorChange, error) {
		res, err := tx.ExecContext(ctx, `DELETE FROM accounts WHERE id = ?`, id)
		if err != nil {
			return mirrorChange{}, fmt.Errorf("accounts: remove: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return mirrorChange{}, fmt.Errorf("accounts: remove: %w", err)
		} else if n == 0 {
			return mirrorChange{}, ErrNotFound
		}
		change := mirrorChange{remove: removed}
		var last sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT last_account FROM account_state WHERE id = 1`).Scan(&last)
		switch {
		case errors.Is(err, sql.ErrNoRows):
			return change, nil
		case err != nil:
			return mirrorChange{}, fmt.Errorf("accounts: remove: %w", err)
		}
		if !last.Valid || last.String != id {
			return change, nil
		}
		if err := s.forgetLast(ctx, tx, &change); err != nil {
			return mirrorChange{}, err
		}
		return change, nil
	})
}

// Last returns the remembered account, or "" when nothing is remembered. When no
// choice was ever recorded, the only account is the answer: a lone account needs
// no picker.
func (s *Store) Last() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	if err := s.checkDir(); err != nil {
		return "", err
	}
	id, chosen, err := s.readLast(ctx)
	if err != nil {
		return "", err
	}
	if !chosen {
		return s.soleAccount(ctx)
	}
	if id == "" {
		return "", nil
	}
	// A remembered account that no longer loads is not a choice any more.
	if _, err := s.load(ctx, id); err != nil {
		return "", nil
	}
	return id, nil
}

// SetLast remembers the account the launcher starts with. An empty id records
// "explicitly none", which is not the same as never having chosen.
func (s *Store) SetLast(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	ctx := context.Background()
	if err := s.checkDir(); err != nil {
		return err
	}
	if id != "" {
		if _, err := s.load(ctx, id); err != nil {
			return err
		}
	}
	return s.writeLast(ctx, id)
}

// load reads one record row by id.
func (s *Store) load(ctx context.Context, id string) (record, error) {
	if !isLowerHex(id, 32) {
		return record{}, ErrNotFound
	}
	var raw []byte
	err := s.db.QueryRow(ctx, `SELECT record FROM accounts WHERE id = ?`, id).Scan(&raw)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return record{}, ErrNotFound
	case err != nil:
		return record{}, fmt.Errorf("accounts: load: %w", err)
	}
	return parseRecord(id, raw)
}

// readFile reads one mirror file without following a reparse point and without
// trusting its size.
func (s *Store) readFile(name string, limit int) ([]byte, error) {
	path := s.mirrorPath(name)
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || isReparse(info) {
		return nil, errNotRegular
	}
	if info.Size() > int64(limit) {
		return nil, errTooLarge
	}
	f, err := openRegular(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// The file may have been swapped between Lstat and open; compare the
	// identity before reading any content.
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, opened) {
		return nil, errNotRegular
	}
	raw, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > limit {
		return nil, errTooLarge
	}
	return raw, nil
}

// writePending stages a sealed record in the directory. It stays invisible to
// every reader until Commit replaces the target with it.
func (s *Store) writePending(data []byte) (string, error) {
	path, err := s.tempPath()
	if err != nil {
		return "", err
	}
	if err := writePrivateFile(path, data); err != nil {
		return "", fmt.Errorf("accounts: stage record: %w", err)
	}
	return path, nil
}

func (s *Store) tempPath() (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("accounts: temp name: %w", err)
	}
	return filepath.Join(s.dir, tempPrefix+hex.EncodeToString(b[:])+tempSuffix), nil
}

// writePrivateFile creates path with a private DACL and flushes it, so a crash
// cannot leave a file whose content is shorter than its size.
func writePrivateFile(path string, data []byte) error {
	f, err := createPrivate(path)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(path)
		return err
	}
	return f.Close()
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
