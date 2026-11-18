// SPDX-License-Identifier: GPL-3.0-or-later

// Package checkin stores the launcher's own daily check-in ledger.
//
// The ledger exists for one reason: a sign-in write that has left the process
// must never be replayed blindly. Before a write the launcher records intent;
// after the answer arrives it records the outcome. A record left in the
// pending state therefore means "the request may have reached the server", and
// the caller must verify with the server before deciding anything.
//
// Records carry account ids, role ids, dates and result codes only. Tokens and
// phone numbers never enter this package, which is why the file needs no
// DPAPI envelope: it is written inside the per-user local application data
// directory and holds nothing secret.
package checkin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	fileName     = "records-v1.json"
	version      = 1
	maxFileBytes = 256 << 10
	maxRecords   = 512
	keepDays     = 60
	maxDetailLen = 64
	maxIDLen     = 64
	tempPrefix   = ".pending-"
	tempSuffix   = ".tmp"
	dateLayout   = "2006-01-02"
	// ledgerLockName guards one read-modify-write of the ledger file.
	ledgerLockName = "lock"
	// signLockName guards a whole sign attempt across processes.
	signLockName = "sign.lock"
)

// ErrBusy reports that another Oddward process holds the ledger right now.
var ErrBusy = errors.New("checkin: ledger busy")

// ErrCorrupt reports a ledger this build cannot trust. It is never repaired
// silently: the caller decides whether to surface it and continue.
var ErrCorrupt = errors.New("checkin: ledger corrupt")

// Kind separates the two independent daily tasks. A success in one never
// implies anything about the other.
type Kind uint8

const (
	// Game is the in-game check-in for the bound 异环 role.
	Game Kind = iota
	// Community is the Tajiduo community check-in for the account.
	Community
	// kinds counts the fixed set; array sizes in the launcher state rely on it.
	kinds = 2
)

func (k Kind) String() string {
	if k == Community {
		return "community"
	}
	return "game"
}

func parseKind(s string) (Kind, bool) {
	switch s {
	case "game":
		return Game, true
	case "community":
		return Community, true
	}
	return 0, false
}

// Result is the outcome of one attempt on one day.
type Result uint8

const (
	// Pending means a write may have been sent but no verdict is recorded.
	// This is the only state that requires server verification.
	Pending Result = iota
	// Success means this attempt earned the reward.
	Success
	// Already means the server said the task was already complete today.
	Already
	// Unverified means the write was sent and the answer never arrived.
	Unverified
	// Failed means the write definitely did not happen.
	Failed
)

func (r Result) String() string {
	switch r {
	case Success:
		return "success"
	case Already:
		return "already"
	case Unverified:
		return "unverified"
	case Failed:
		return "failed"
	}
	return "pending"
}

func parseResult(s string) (Result, bool) {
	switch s {
	case "pending":
		return Pending, true
	case "success":
		return Success, true
	case "already":
		return Already, true
	case "unverified":
		return Unverified, true
	case "failed":
		return Failed, true
	}
	return 0, false
}

// Key identifies one day's ledger entry.
type Key struct {
	AccountID string
	RoleID    string
	Kind      Kind
	Date      string
}

// Record is one ledger entry. RoleID is empty for the account-scoped
// community task.
type Record struct {
	AccountID, RoleID string
	Kind              Kind
	Date              string
	Result            Result
	Detail            string
	At                time.Time
}

func (r Record) key() Key {
	return Key{AccountID: r.AccountID, RoleID: r.RoleID, Kind: r.Kind, Date: r.Date}
}

// Day returns the calendar day of t in t's own zone, the unit the ledger is
// keyed by. Reading it from t rather than the machine zone keeps a record on the
// day it was taken even when the two disagree.
func Day(t time.Time) string { return t.Format(dateLayout) }

// Store is a small, lock-protected JSON ledger. All exported methods are safe
// for concurrent use and consult the file for every read, so two Oddward
// processes see one consistent ledger.
type Store struct {
	dir string
	now func() time.Time

	mu sync.Mutex
}

// Open prepares dir and validates any existing ledger. A missing ledger is an
// empty one; an unreadable one is ErrCorrupt.
func Open(dir string) (*Store, error) {
	if dir == "" || !filepath.IsAbs(dir) {
		return nil, fmt.Errorf("checkin: directory must be absolute")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, now: time.Now}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// LockSign takes a cross-process lease for one sign attempt and returns the
// release function. The ledger lock in <dir>\lock only covers a single write,
// which is too short to keep two launcher processes from both deciding "not
// signed yet" and both posting. This lease is held for the whole attempt, so
// the second process reports ErrBusy instead of spending the same day twice.
//
// The kernel drops the lease when the process exits, so a crash mid-attempt
// never blocks the next start; the pending record still protects that case.
func (s *Store) LockSign() (func(), error) {
	f, err := lockFile(filepath.Join(s.dir, signLockName))
	if err != nil {
		return nil, err
	}
	return func() { unlock(f) }, nil
}

// Lookup returns the recorded entry for key, if any.
func (s *Store) Lookup(ctx context.Context, k Key) (Record, bool, error) {
	if err := validateKey(k); err != nil {
		return Record{}, false, err
	}
	doc, err := s.read(ctx)
	if err != nil {
		return Record{}, false, err
	}
	for _, w := range doc.Records {
		r, err := decodeRecord(w)
		if err != nil {
			return Record{}, false, ErrCorrupt
		}
		if r.key() == k {
			return r, true, nil
		}
	}
	return Record{}, false, nil
}

// Put stores or replaces the entry for record's key. Callers write Pending
// before a write and the final Result afterwards.
func (s *Store) Put(ctx context.Context, r Record) error {
	if err := validateRecord(r); err != nil {
		return err
	}
	return s.update(ctx, func(doc *document) error {
		w := encodeRecord(r)
		for i := range doc.Records {
			old := doc.Records[i]
			if old.AccountID == w.AccountID && old.RoleID == w.RoleID && old.Kind == w.Kind && old.Date == w.Date {
				doc.Records[i] = w
				return nil
			}
		}
		doc.Records = append(doc.Records, w)
		return nil
	})
}

// Auto reports the per-account auto-check-in switches, indexed by Kind.
func (s *Store) Auto(ctx context.Context, accountID string) ([kinds]bool, error) {
	var out [kinds]bool
	if !validID(accountID) {
		return out, fmt.Errorf("checkin: bad account id")
	}
	doc, err := s.read(ctx)
	if err != nil {
		return out, err
	}
	entry, ok := doc.Auto[accountID]
	if !ok {
		return out, nil
	}
	out[Game] = entry.Game
	out[Community] = entry.Community
	return out, nil
}

// SetAuto changes one switch for one account.
func (s *Store) SetAuto(ctx context.Context, accountID string, k Kind, on bool) error {
	if !validID(accountID) || k >= kinds {
		return fmt.Errorf("checkin: bad argument")
	}
	return s.update(ctx, func(doc *document) error {
		entry := doc.Auto[accountID]
		if k == Game {
			entry.Game = on
		} else {
			entry.Community = on
		}
		if doc.Auto == nil {
			doc.Auto = map[string]autoEntry{}
		}
		doc.Auto[accountID] = entry
		return nil
	})
}

func (s *Store) read(ctx context.Context) (*document, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return s.load()
}

func (s *Store) update(ctx context.Context, mutate func(*document) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	lock, err := lockFile(filepath.Join(s.dir, ledgerLockName))
	if err != nil {
		return err
	}
	defer unlock(lock)
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doc, err := s.load()
	if err != nil {
		return err
	}
	if err := mutate(doc); err != nil {
		return err
	}
	return s.save(doc)
}

func (s *Store) load() (*document, error) {
	path := filepath.Join(s.dir, fileName)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &document{Version: version, Auto: map[string]autoEntry{}, Records: []wireRecord{}}, nil
	}
	if err != nil {
		return nil, err
	}
	if len(raw) > maxFileBytes {
		return nil, ErrCorrupt
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	doc := &document{}
	if err := dec.Decode(doc); err != nil {
		return nil, ErrCorrupt
	}
	if dec.More() {
		return nil, ErrCorrupt
	}
	if doc.Version != version {
		return nil, ErrCorrupt
	}
	if doc.Auto == nil {
		doc.Auto = map[string]autoEntry{}
	}
	for id := range doc.Auto {
		if !validID(id) {
			return nil, ErrCorrupt
		}
	}
	if doc.Records == nil {
		doc.Records = []wireRecord{}
	}
	seen := map[Key]bool{}
	for i, w := range doc.Records {
		r, err := decodeRecord(w)
		if err != nil {
			return nil, err
		}
		if seen[r.key()] {
			return nil, ErrCorrupt
		}
		seen[r.key()] = true
		doc.Records[i] = encodeRecord(r)
	}
	return doc, nil
}

func (s *Store) save(doc *document) error {
	doc.Version = version
	doc.Records = trim(doc.Records, s.now())
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	if len(raw) > maxFileBytes {
		return ErrCorrupt
	}
	var name [8]byte
	if _, err := rand.Read(name[:]); err != nil {
		return err
	}
	temp := filepath.Join(s.dir, tempPrefix+hex.EncodeToString(name[:])+tempSuffix)
	f, err := os.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(raw); err != nil {
		f.Close()
		os.Remove(temp)
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(temp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(temp)
		return err
	}
	if err := os.Rename(temp, filepath.Join(s.dir, fileName)); err != nil {
		os.Remove(temp)
		return err
	}
	return nil
}

type document struct {
	Version int                  `json:"version"`
	Auto    map[string]autoEntry `json:"auto"`
	Records []wireRecord         `json:"records"`
}

type autoEntry struct {
	Game      bool `json:"game"`
	Community bool `json:"community"`
}

type wireRecord struct {
	AccountID string `json:"accountId"`
	RoleID    string `json:"roleId,omitempty"`
	Kind      string `json:"kind"`
	Date      string `json:"date"`
	Result    string `json:"result"`
	Detail    string `json:"detail,omitempty"`
	At        string `json:"at"`
}

func encodeRecord(r Record) wireRecord {
	return wireRecord{
		AccountID: r.AccountID,
		RoleID:    r.RoleID,
		Kind:      r.Kind.String(),
		Date:      r.Date,
		Result:    r.Result.String(),
		Detail:    r.Detail,
		At:        r.At.UTC().Format(time.RFC3339),
	}
}

func decodeRecord(w wireRecord) (Record, error) {
	kind, ok := parseKind(w.Kind)
	if !ok {
		return Record{}, ErrCorrupt
	}
	result, ok := parseResult(w.Result)
	if !ok {
		return Record{}, ErrCorrupt
	}
	at, err := time.Parse(time.RFC3339, w.At)
	if err != nil {
		return Record{}, ErrCorrupt
	}
	r := Record{AccountID: w.AccountID, RoleID: w.RoleID, Kind: kind, Date: w.Date, Result: result, Detail: w.Detail, At: at}
	if err := validateRecord(r); err != nil {
		return Record{}, ErrCorrupt
	}
	return r, nil
}

// trim keeps the ledger bounded: the newest keepDays days by date, and no more
// than maxRecords entries even if many accounts share those days.
func trim(records []wireRecord, now time.Time) []wireRecord {
	cutoff := now.AddDate(0, 0, -keepDays).Format(dateLayout)
	kept := records[:0]
	for _, r := range records {
		if r.Date >= cutoff {
			kept = append(kept, r)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool {
		if kept[i].Date != kept[j].Date {
			return kept[i].Date > kept[j].Date
		}
		return kept[i].At > kept[j].At
	})
	if len(kept) > maxRecords {
		kept = kept[:maxRecords]
	}
	return kept
}

func validateKey(k Key) error {
	if !validID(k.AccountID) || k.Kind >= kinds || !validDate(k.Date) {
		return fmt.Errorf("checkin: bad key")
	}
	if k.RoleID != "" && !validID(k.RoleID) {
		return fmt.Errorf("checkin: bad role id")
	}
	return nil
}

func validateRecord(r Record) error {
	if err := validateKey(r.key()); err != nil {
		return err
	}
	if r.Result > Failed {
		return fmt.Errorf("checkin: bad result")
	}
	// Detail is a short human sentence, so it may hold CJK text, but never a
	// control character, an invalid byte or an unbounded string.
	if !utf8.ValidString(r.Detail) || utf8.RuneCountInString(r.Detail) > maxDetailLen {
		return fmt.Errorf("checkin: bad detail")
	}
	for _, c := range r.Detail {
		if !unicode.IsPrint(c) {
			return fmt.Errorf("checkin: detail not printable")
		}
	}
	if r.At.IsZero() {
		return fmt.Errorf("checkin: missing time")
	}
	return nil
}

func validID(s string) bool {
	if s == "" || len(s) > maxIDLen {
		return false
	}
	for _, b := range []byte(s) {
		if b < 0x21 || b > 0x7e {
			return false
		}
	}
	return true
}

func validDate(s string) bool {
	if len(s) != len(dateLayout) {
		return false
	}
	t, err := time.Parse(dateLayout, s)
	return err == nil && t.Format(dateLayout) == s
}

// Describe renders a record for a log line without exposing anything upstream.
func (r Record) Describe() string {
	role := r.RoleID
	if role == "" {
		role = "-"
	}
	return strings.Join([]string{r.Date, r.Kind.String(), r.Result.String(), role, r.Detail}, "/")
}
