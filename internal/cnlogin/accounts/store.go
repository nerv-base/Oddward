// SPDX-License-Identifier: GPL-3.0-or-later

package accounts

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// MaxAccounts is the number of distinct accounts a Store keeps.
	MaxAccounts = 16
	// MaxRecordSize is the largest accepted record file in bytes.
	MaxRecordSize = 16 << 10
	// MaxLabelRunes is the longest accepted label after sanitizing.
	MaxLabelRunes = 32

	// maxScanCandidates bounds how many record files one scan visits, so a
	// directory filled with junk cannot stall a login.
	maxScanCandidates = 64
	// minPendingBytes is shorter than any complete sealed state, so a shorter
	// temp file is a torn write of this package.
	minPendingBytes = 16

	tempPrefix = ".pending-"
	tempSuffix = ".tmp"
)

var (
	ErrNotFound        = errors.New("accounts: account not found")
	ErrCorrupt         = errors.New("accounts: record is corrupt")
	ErrUnlock          = errors.New("accounts: record cannot be unlocked")
	ErrInvalidLabel    = errors.New("accounts: invalid label")
	ErrInvalidSecret   = errors.New("accounts: invalid secret")
	ErrInvalidPhone    = errors.New("accounts: not a mainland mobile number")
	ErrInvalidNotice   = errors.New("accounts: invalid notice revision")
	ErrTooManyAccounts = errors.New("accounts: too many saved accounts")
	ErrUnsafePath      = errors.New("accounts: unsafe store path")
	ErrScanSaturated   = errors.New("accounts: store has too many record files")

	errNotRegular = errors.New("not a regular file")
	errTooLarge   = errors.New("file too large")
)

// KeyProtector wraps and unwraps per-account data keys. Implementations must
// be bound to the current user: DPAPI is the production implementation.
type KeyProtector interface {
	Protect(plain []byte) ([]byte, error)
	Unprotect(wrapped []byte) ([]byte, error)
}

// Account is the public, unencrypted part of a saved account.
type Account struct {
	ID      string // 32 lowercase hex chars
	Label   string // masked phone or user remark, 1..MaxLabelRunes runes
	SavedAt time.Time
	// TajiduoLinked reports whether the account has a readable Tajiduo session
	// attachment (<id>.tjd) beside its record. It is filled in by List; it is
	// not part of the record, so it is never bound by the record's AAD.
	TajiduoLinked bool
	// TajiduoState refines TajiduoLinked for the account list: "linked" when
	// the attachment decrypts, "needs-relink" when it exists but cannot be
	// opened (for example after a machine change), and "" when there is no
	// attachment. Like TajiduoLinked it is derived by List, never stored.
	TajiduoState string
}

// Store is a directory of saved accounts. Methods are safe for concurrent use
// within one process; the directory must not be shared between processes.
type Store struct {
	dir string
	p   KeyProtector

	mu     sync.Mutex
	now    func() time.Time
	rename func(oldpath, newpath string) error
}

// Open uses dir (absolute) as the store, creating it if needed, gives it a
// protected private DACL and removes temp files left by an interrupted write.
func Open(dir string, p KeyProtector) (*Store, error) {
	if p == nil {
		return nil, errors.New("accounts: nil KeyProtector")
	}
	if !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("%w: %q is not absolute", ErrUnsafePath, dir)
	}
	s := &Store{dir: filepath.Clean(dir), p: p, now: time.Now, rename: os.Rename}
	if err := checkAncestors(s.dir); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(s.dir); errors.Is(err, fs.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(s.dir), 0o700); err != nil {
			return nil, fmt.Errorf("accounts: create store: %w", err)
		}
		if err := privateMkdir(s.dir); err != nil {
			return nil, fmt.Errorf("accounts: create store: %w", err)
		}
	}
	if err := s.checkDir(); err != nil {
		return nil, err
	}
	if err := protectDirectory(s.dir); err != nil {
		return nil, fmt.Errorf("%w: cannot protect %s: %w", ErrUnsafePath, s.dir, err)
	}
	s.removeStaleTemps()
	return s, nil
}

// checkAncestors rejects a reparse point or non-directory above the store: a
// junction in the path would redirect every write somewhere else.
func checkAncestors(path string) error {
	volume := filepath.VolumeName(path)
	parts := strings.Split(strings.Trim(path[len(volume):], string(filepath.Separator)), string(filepath.Separator))
	current := volume
	for _, part := range parts[:len(parts)-1] {
		current += string(filepath.Separator) + part
		fi, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("%w: %w", ErrUnsafePath, err)
		}
		if !fi.IsDir() {
			return fmt.Errorf("%w: %s is not a directory", ErrUnsafePath, current)
		}
		if isReparse(fi) {
			return fmt.Errorf("%w: %s is a reparse point", ErrUnsafePath, current)
		}
	}
	return nil
}

func (s *Store) checkDir() error {
	fi, err := os.Lstat(s.dir)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnsafePath, err)
	}
	if !fi.IsDir() || isReparse(fi) {
		return fmt.Errorf("%w: %s is not a plain directory", ErrUnsafePath, s.dir)
	}
	return nil
}

// removeStaleTemps removes temp files this package provably abandoned. A file
// that only looks like one of ours is left alone: a crash after the write must
// not destroy anything that is not a torn or complete candidate of this store.
func (s *Store) removeStaleTemps() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		mid, ok := strings.CutPrefix(e.Name(), tempPrefix)
		if !ok {
			continue
		}
		if mid, ok = strings.CutSuffix(mid, tempSuffix); !ok || !isLowerHex(mid, 16) {
			continue
		}
		path := filepath.Join(s.dir, e.Name())
		fi, err := os.Lstat(path)
		if err != nil || !fi.Mode().IsRegular() || isReparse(fi) {
			continue
		}
		if !s.abandonedPending(path, fi.Size()) {
			continue
		}
		_ = removeRegular(path, fi)
	}
}

// abandonedPending reports whether path holds a torn or complete write of this
// package: shorter than any complete sealed state, or a record that parses.
func (s *Store) abandonedPending(path string, size int64) bool {
	if size < minPendingBytes {
		return true
	}
	if size > MaxRecordSize {
		return false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	_, err = parseRecordBytes(raw)
	return err == nil
}

// List returns the readable accounts, newest first, and how many record
// files were skipped as corrupt.
func (s *Store) List() ([]Account, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	recs, corrupt, _, err := s.scan()
	if err != nil {
		return nil, 0, err
	}
	out := make([]Account, len(recs))
	for i, r := range recs {
		a := r.Account
		a.TajiduoLinked, a.TajiduoState = s.tajiduoState(a.ID)
		out[i] = a
	}
	return out, corrupt, nil
}

// Save stores sec: a refresh of a known channel uid replaces that account in
// place (same id, new SavedAt) and keeps the committed label unless a non-empty
// one is given; a new identity gets a new id and needs a label. a.ID and
// a.SavedAt are ignored. Saving publishes the record atomically, so it never
// leaves a partial record behind; use Prepare to hold the write until delivery.
func (s *Store) Save(a Account, sec Secret) (Account, error) {
	tx, err := s.prepare(a, sec, true)
	if err != nil {
		return Account{}, err
	}
	if err := tx.Commit(); err != nil {
		_ = tx.Rollback()
		return Account{}, err
	}
	return tx.account, nil
}

func (s *Store) sameChannelUID(r record, uid string) bool {
	key, plain, err := s.open(r)
	if err != nil {
		return false
	}
	defer wipeBytes(key)
	defer wipeBytes(plain)
	sec, err := decodePayload(plain)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(sec.ChannelUID), []byte(uid)) == 1
}

// sealNew encrypts sec under a fresh data key and wraps the key, checking
// that the protector can unwrap it again before anything is written.
func (s *Store) sealNew(a Account, identity string, sec Secret) ([]byte, error) {
	key := make([]byte, dataKeySize)
	defer wipeBytes(key)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	wrapped, err := s.p.Protect(key)
	if err != nil {
		return nil, fmt.Errorf("accounts: protect data key: %w", err)
	}
	if len(wrapped) == 0 || len(wrapped) > maxWrappedSize {
		return nil, fmt.Errorf("accounts: protected key is %d bytes", len(wrapped))
	}
	check, err := s.p.Unprotect(wrapped)
	ok := err == nil && subtle.ConstantTimeCompare(check, key) == 1
	wipeBytes(check)
	if !ok {
		return nil, fmt.Errorf("accounts: protected key does not unwrap to the original: %v", err)
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

// open unwraps r's key and decrypts its payload. Callers wipe both.
func (s *Store) open(r record) (key, plain []byte, err error) {
	key, err = s.p.Unprotect(r.wrapped)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrUnlock, err)
	}
	if len(key) != dataKeySize {
		wipeBytes(key)
		return nil, nil, fmt.Errorf("%w: data key has wrong size", ErrUnlock)
	}
	aead, err := newGCM(key)
	if err == nil {
		plain, err = aead.Open(nil, r.nonce, r.sealed, recordAAD(r.Account, r.identityKey))
	}
	if err != nil {
		wipeBytes(key)
		return nil, nil, fmt.Errorf("%w: authentication failed", ErrUnlock)
	}
	return key, plain, nil
}

// Unlock decrypts the secret of account id.
func (s *Store) Unlock(id string) (Secret, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkDir(); err != nil {
		return Secret{}, err
	}
	r, err := s.load(id)
	if err != nil {
		return Secret{}, err
	}
	key, plain, err := s.open(r)
	if err != nil {
		return Secret{}, err
	}
	defer wipeBytes(key)
	defer wipeBytes(plain)
	return decodePayload(plain)
}

// Rename changes the label of account id, keeping its secret and SavedAt.
func (s *Store) Rename(id, label string) error {
	label, err := sanitizeLabel(label)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkDir(); err != nil {
		return err
	}
	r, err := s.load(id)
	if err != nil {
		return err
	}
	key, plain, err := s.open(r)
	if err != nil {
		return err
	}
	defer wipeBytes(key)
	defer wipeBytes(plain)
	r.Label = label
	if err := seal(&r, key, plain); err != nil {
		return err
	}
	data, err := encodeRecord(r)
	if err != nil {
		return err
	}
	return s.writeAtomic(r.ID+recordExt, data)
}

// Remove deletes account id, including a corrupt record, and forgets it as the
// last used account. A junction or symlink in its place is never followed.
func (s *Store) Remove(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkDir(); err != nil {
		return err
	}
	if !isLowerHex(id, 32) {
		return ErrNotFound
	}
	path := filepath.Join(s.dir, id+recordExt)
	before, err := os.Lstat(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return ErrNotFound
	case err != nil:
		return fmt.Errorf("accounts: remove: %w", err)
	}
	// The attachment goes first: an interruption between the two removals then
	// leaves an account without a Tajiduo link, never an orphan attachment.
	if err := s.removeTajiduoFile(id); err != nil {
		return fmt.Errorf("accounts: remove tajiduo: %w", err)
	}
	if err := removeRegular(path, before); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ErrNotFound
		}
		return fmt.Errorf("accounts: remove: %w", err)
	}
	if last, state := s.readLast(); state && last == id {
		return s.writeLast("")
	}
	return nil
}

// Last returns the remembered account id, or "" when none is remembered or
// the remembered record is gone or corrupt. If nothing was ever remembered
// and exactly one account exists, that account is returned.
func (s *Store) Last() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkDir(); err != nil {
		return "", err
	}
	last, state := s.readLast()
	if !state {
		recs, _, saturated, err := s.scan()
		if err != nil {
			return "", err
		}
		if saturated {
			return "", ErrScanSaturated
		}
		if len(recs) != 1 {
			return "", nil
		}
		return recs[0].ID, nil
	}
	if last == "" {
		return "", nil
	}
	if _, err := s.load(last); err != nil {
		return "", nil
	}
	return last, nil
}

// SetLast remembers id as the last used account; "" remembers none.
func (s *Store) SetLast(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.checkDir(); err != nil {
		return err
	}
	if id != "" {
		if _, err := s.load(id); err != nil {
			return err
		}
	}
	return s.writeLast(id)
}

// scan reads up to maxScanCandidates record files, newest first, counting
// invalid ones. saturated reports that candidates were left unvisited.
func (s *Store) scan() (recs []record, corrupt int, saturated bool, err error) {
	if err := s.checkDir(); err != nil {
		return nil, 0, false, err
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, 0, false, fmt.Errorf("accounts: read store: %w", err)
	}
	// Record names present on disk, valid or corrupt, decide whether a .tjd has
	// an owning record. A .tjd beside no .acct is an orphan: the existing
	// cleanup only counts unusable files and never deletes them, so an orphan is
	// counted as corrupt and left in place.
	recordIDs := make(map[string]bool, len(entries))
	for _, e := range entries {
		if id, ok := strings.CutSuffix(e.Name(), recordExt); ok && isLowerHex(id, 32) {
			recordIDs[id] = true
		}
	}
	visited := 0
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), recordExt)
		if !ok || !isLowerHex(id, 32) {
			continue
		}
		if visited == maxScanCandidates {
			saturated = true
			break
		}
		visited++
		r, err := s.load(id)
		switch {
		case err == nil:
			recs = append(recs, r)
		case errors.Is(err, ErrNotFound):
		default:
			corrupt++
		}
	}
	for _, e := range entries {
		id, ok := strings.CutSuffix(e.Name(), tajiduoExt)
		if !ok || !isLowerHex(id, 32) {
			continue
		}
		if !recordIDs[id] {
			corrupt++
		}
	}
	sort.Slice(recs, func(i, j int) bool {
		if !recs[i].SavedAt.Equal(recs[j].SavedAt) {
			return recs[i].SavedAt.After(recs[j].SavedAt)
		}
		return recs[i].ID < recs[j].ID
	})
	return recs, corrupt, saturated, nil
}

func (s *Store) load(id string) (record, error) {
	if !isLowerHex(id, 32) {
		return record{}, ErrNotFound
	}
	raw, err := s.readFile(id+recordExt, MaxRecordSize)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return record{}, ErrNotFound
	case errors.Is(err, errNotRegular), errors.Is(err, errTooLarge):
		return record{}, fmt.Errorf("%w: %w", ErrCorrupt, err)
	case err != nil:
		return record{}, err
	}
	return parseRecord(id, raw)
}

// readFile reads a regular, non-reparse file of at most limit bytes and
// re-applies the private DACL on the way in.
func (s *Store) readFile(name string, limit int) ([]byte, error) {
	path := filepath.Join(s.dir, name)
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || isReparse(before) {
		return nil, errNotRegular
	}
	if before.Size() > int64(limit) {
		return nil, errTooLarge
	}
	f, err := openRegular(path)
	if err != nil {
		if errors.Is(err, ErrUnsafePath) {
			return nil, errNotRegular
		}
		return nil, err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, opened) {
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

// writeAtomic writes data to a fresh private temp file, fsyncs it and renames
// it over name. On any failure the temp file is removed and name is unchanged.
func (s *Store) writeAtomic(name string, data []byte) error {
	tmp, err := s.tempPath()
	if err != nil {
		return err
	}
	if err := writePrivateFile(tmp, data); err != nil {
		return err
	}
	if err := s.rename(tmp, filepath.Join(s.dir, name)); err != nil {
		_ = removeRegular(tmp, nil)
		return fmt.Errorf("accounts: commit %s: %w", name, err)
	}
	return nil
}

// writePending writes data to a durable, private candidate file and returns its
// path. The candidate stays invisible to List and Unlock until Commit renames
// it into place.
func (s *Store) writePending(data []byte) (string, error) {
	tmp, err := s.tempPath()
	if err != nil {
		return "", err
	}
	if err := writePrivateFile(tmp, data); err != nil {
		return "", err
	}
	return tmp, nil
}

func (s *Store) tempPath() (string, error) {
	var suffix [8]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		return "", err
	}
	return filepath.Join(s.dir, tempPrefix+hex.EncodeToString(suffix[:])+tempSuffix), nil
}

// writePrivateFile creates path with the private DACL, writes data and flushes
// it to disk. On failure the partial file is removed.
func writePrivateFile(path string, data []byte) error {
	f, err := createPrivate(path)
	if err != nil {
		return fmt.Errorf("accounts: create temp file: %w", err)
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = removeRegular(path, nil)
		return fmt.Errorf("accounts: write temp file: %w", err)
	}
	return nil
}

func newID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}
