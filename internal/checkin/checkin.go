// SPDX-License-Identifier: GPL-3.0-or-later

// Package checkin stores the launcher's own daily check-in ledger.
//
// The ledger exists for one reason: a sign-in write that has left the process
// must never be replayed blindly. Before a write the launcher records intent;
// after the answer arrives it records the outcome. A record left in the
// pending state therefore means "the request may have reached the server", and
// the caller must verify with the server before deciding anything.
//
// The rows live in the launcher's SQLite database. records-v1.json is still
// written next to it as a mirror: the self-updater can roll the launcher back to
// a build that cannot read the database, and that build has to find the records
// this one accepted. The database stays authoritative; the mirror is only read
// back by the importer, and only after another build changed it.
//
// Records carry account ids, role ids, dates and result codes only. Tokens and
// phone numbers never enter this package, so neither the database row nor the
// mirror needs a DPAPI envelope.
package checkin

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nerv-base/Oddward/internal/database"
)

const (
	fileName     = "records-v1.json"
	version      = 1
	maxFileBytes = 256 << 10
	maxRecords   = 512
	keepDays     = 60
	maxDetailLen = 64
	maxIDLen     = 64
	dateLayout   = "2006-01-02"

	// domain is the name this store's rows use in the database's mirror and
	// migration bookkeeping.
	domain = "checkin"

	// ledgerLockName guards one write and its mirror file across processes.
	ledgerLockName = "lock"

	// signLockName guards a whole sign attempt across processes.
	signLockName = "sign.lock"

	// signLeaseWait bounds the wait for a lease another process holds. The
	// attempt itself takes far longer, so a second launcher still reports
	// ErrBusy; the wait only keeps a lease released a moment ago from being
	// reported as busy.
	signLeaseWait = 100 * time.Millisecond
)

// ErrBusy reports that another Oddward process is checking in right now.
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

// Store is the check-in ledger. All exported methods are safe for concurrent
// use. One Store owns its directory: that is where the sign lease and the
// rollback mirror live, while the rows themselves are in the database.
type Store struct {
	db  *database.DB
	dir string
	now func() time.Time

	// mu keeps this process' writes in order. The transaction already prevents
	// lost rows; what needs ordering is the mirror, which is written after the
	// commit, so a later snapshot must never be replaced by an earlier one.
	mu sync.Mutex
}

// Open prepares dir and returns a store backed by db. dir keeps the sign lease
// and the records-v1.json mirror. An existing mirror is not read here: the
// database is authoritative, and ImportLegacy reconciles a mirror that another
// build wrote.
func Open(db *database.DB, dir string) (*Store, error) {
	if db == nil {
		return nil, errors.New("checkin: no database")
	}
	if dir == "" || !filepath.IsAbs(dir) {
		return nil, errors.New("checkin: directory must be absolute")
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	return &Store{db: db, dir: dir, now: time.Now}, nil
}

// LockSign takes a cross-process lease for one sign attempt and returns the
// release function. One write transaction is far too short to keep two
// launchers from both reading "not signed yet" and both posting, so this lease
// is held for the whole attempt and the second process reports ErrBusy instead
// of spending the same day twice.
//
// The kernel drops the lease when the process exits, so a crash mid-attempt
// never blocks the next start; the pending record still protects that case.
func (s *Store) LockSign() (func(), error) {
	ctx, cancel := context.WithTimeout(context.Background(), signLeaseWait)
	defer cancel()
	lock, err := database.AcquireLock(ctx, filepath.Join(s.dir, signLockName))
	if err != nil {
		if errors.Is(err, database.ErrBusy) {
			return nil, ErrBusy
		}
		return nil, err
	}
	return func() { _ = lock.Release() }, nil
}

// Lookup returns the recorded entry for key, if any.
func (s *Store) Lookup(ctx context.Context, k Key) (Record, bool, error) {
	if err := validateKey(k); err != nil {
		return Record{}, false, err
	}
	var (
		kind, result int
		date, detail string
		at           int64
	)
	err := s.db.QueryRow(ctx, `
		SELECT kind, date, result, detail, at FROM checkin_records
		WHERE account_id = ? AND role_id = ? AND kind = ? AND date = ?`,
		k.AccountID, k.RoleID, int(k.Kind), k.Date).
		Scan(&kind, &date, &result, &detail, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, false, nil
	}
	if err != nil {
		return Record{}, false, err
	}
	return Record{
		AccountID: k.AccountID,
		RoleID:    k.RoleID,
		Kind:      Kind(kind),
		Date:      date,
		Result:    Result(result),
		Detail:    detail,
		At:        database.StampInt(at),
	}, true, nil
}

// Put stores or replaces the entry for record's key. Callers write Pending
// before a write and the final Result afterwards.
func (s *Store) Put(ctx context.Context, r Record) error {
	if err := validateRecord(r); err != nil {
		return err
	}
	return s.change(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := putRecord(ctx, tx, r); err != nil {
			return err
		}
		return s.prune(ctx, tx)
	})
}

// Auto reports the per-account auto-check-in switches, indexed by Kind.
func (s *Store) Auto(ctx context.Context, accountID string) ([kinds]bool, error) {
	var out [kinds]bool
	if !validID(accountID) {
		return out, fmt.Errorf("checkin: bad account id")
	}
	rows, err := s.db.Query(ctx, "SELECT kind, enabled FROM checkin_auto WHERE account_id = ?", accountID)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var kind, enabled int
		if err := rows.Scan(&kind, &enabled); err != nil {
			return out, err
		}
		out[Kind(kind)] = enabled != 0
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	return out, nil
}

// SetAuto changes one switch for one account.
func (s *Store) SetAuto(ctx context.Context, accountID string, k Kind, on bool) error {
	if !validID(accountID) || k >= kinds {
		return fmt.Errorf("checkin: bad argument")
	}
	return s.change(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := putAuto(ctx, tx, accountID, k, on); err != nil {
			return err
		}
		return s.prune(ctx, tx)
	})
}

// ImportLegacy reconciles records-v1.json into the database. RunImport runs it
// once for a domain that was never imported, and again whenever the mirror no
// longer matches what this build wrote (the stub rolled back to a build that
// rewrote the file). A missing file leaves the rows alone.
func (s *Store) ImportLegacy(ctx context.Context) error {
	_, err := s.db.RunImport(ctx, domain, version, s.importLegacy)
	return err
}

func (s *Store) importLegacy(ctx context.Context) (source, detail string, err error) {
	path := filepath.Join(s.dir, fileName)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return path, "no legacy ledger", nil
	}
	if err != nil {
		return path, "", fmt.Errorf("checkin: read %s: %w", path, err)
	}
	auto, records, err := decodeLegacy(raw)
	if err != nil {
		return path, "", fmt.Errorf("%w: %s", ErrCorrupt, path)
	}
	// The file wins while it is being reconciled: a rollback build accepted data
	// this one has not seen, and the mirror is the only record of it. Rows the
	// file no longer holds are therefore dropped, not merged.
	if err := s.change(ctx, func(ctx context.Context, tx *sql.Tx) error {
		return replaceRows(ctx, tx, auto, records)
	}); err != nil {
		return path, "", err
	}
	return path, fmt.Sprintf("%d records, %d accounts", len(records), len(auto)), nil
}

// change applies one ledger change as a unit. It holds the ledger lease for the
// whole change, and the mirror file is replaced inside the transaction that
// carries the rows. A refused commit puts the previous bytes back, so a change
// the caller was told had failed is never imported again on the next start.
func (s *Store) change(ctx context.Context, apply func(context.Context, *sql.Tx) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	lease, err := s.lease(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = lease.Release() }()
	path := filepath.Join(s.dir, fileName)
	previous, hadPrevious, err := database.MirrorSnapshot(path)
	if err != nil {
		return err
	}
	err = s.db.Tx(ctx, func(ctx context.Context, tx *sql.Tx) error {
		if err := apply(ctx, tx); err != nil {
			return err
		}
		doc, err := snapshot(ctx, tx)
		if err != nil {
			return err
		}
		mirror, err := json.Marshal(doc)
		if err != nil {
			return err
		}
		// The file version refused to write a document it could not read back.
		// The bound stays even though the prune keeps the ledger well below it.
		if len(mirror) > maxFileBytes {
			return ErrCorrupt
		}
		return s.db.MirrorInTx(ctx, tx, domain, path, mirror)
	})
	if err != nil {
		if restoreErr := database.RestoreMirror(path, previous, hadPrevious); restoreErr != nil {
			return fmt.Errorf("%w (the check-in mirror could not be restored: %v)", err, restoreErr)
		}
		return err
	}
	return nil
}

// lease serialises this change with other Oddward processes. The transaction
// alone keeps the rows consistent; the lease covers the file replace that happens
// inside it. Without it two processes could replace the mirror between each
// other's write and commit, and the next start would reconcile a file that does
// not describe the rows it committed. Wait it out: the lease only covers one
// transaction and one file replace, and the caller's context bounds the wait,
// because a write that loses this race must not look like a failed check-in.
func (s *Store) lease(ctx context.Context) (*database.Lock, error) {
	lock, err := database.AcquireLock(ctx, filepath.Join(s.dir, ledgerLockName))
	if err != nil {
		// The lock is only refused once the caller's context ends; report that
		// as the context's own error instead of a busy ledger.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, ctxErr
		}
		return nil, err
	}
	return lock, nil
}

// prune keeps the ledger bounded exactly as the file version did: only records
// from the newest keepDays days by date survive, and no more than maxRecords
// entries, newest first.
func (s *Store) prune(ctx context.Context, tx *sql.Tx) error {
	cutoff := s.now().AddDate(0, 0, -keepDays).Format(dateLayout)
	if _, err := tx.ExecContext(ctx, "DELETE FROM checkin_records WHERE date < ?", cutoff); err != nil {
		return err
	}
	// The file ordered entries of one day by its second-precision "at" string;
	// the key columns only make the cut deterministic for records that string
	// could not tell apart.
	_, err := tx.ExecContext(ctx, `
		DELETE FROM checkin_records WHERE rowid NOT IN (
			SELECT rowid FROM checkin_records
			ORDER BY date DESC, at DESC, account_id, role_id, kind
			LIMIT ?
		)`, maxRecords)
	return err
}

// putRecord writes one row, replacing the entry for its key.
func putRecord(ctx context.Context, tx *sql.Tx, r Record) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO checkin_records(account_id, role_id, kind, date, result, detail, at)
		VALUES(?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(account_id, role_id, kind, date) DO UPDATE SET
			result = excluded.result,
			detail = excluded.detail,
			at = excluded.at`,
		r.AccountID, r.RoleID, int(r.Kind), r.Date, int(r.Result), r.Detail, database.Stamp(r.At))
	return err
}

// putAuto writes one switch, replacing the entry for its key.
func putAuto(ctx context.Context, tx *sql.Tx, accountID string, k Kind, on bool) error {
	enabled := 0
	if on {
		enabled = 1
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO checkin_auto(account_id, kind, enabled) VALUES(?, ?, ?)
		ON CONFLICT(account_id, kind) DO UPDATE SET enabled = excluded.enabled`,
		accountID, int(k), enabled)
	return err
}

// replaceRows makes the rows hold exactly the legacy document. The delete and
// the inserts are one transaction, so an interrupted import leaves the previous
// rows untouched.
func replaceRows(ctx context.Context, tx *sql.Tx, auto map[string]autoEntry, records []Record) error {
	if _, err := tx.ExecContext(ctx, "DELETE FROM checkin_records"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM checkin_auto"); err != nil {
		return err
	}
	for _, r := range records {
		if err := putRecord(ctx, tx, r); err != nil {
			return err
		}
	}
	for accountID, entry := range auto {
		for k := Kind(0); k < kinds; k++ {
			on := entry.Game
			if k == Community {
				on = entry.Community
			}
			if err := putAuto(ctx, tx, accountID, k, on); err != nil {
				return err
			}
		}
	}
	return nil
}

// snapshot renders the whole ledger in the shape the file version wrote: records
// newest first, and one entry per account that has a switch recorded.
func snapshot(ctx context.Context, tx *sql.Tx) (*document, error) {
	records, err := snapshotRecords(ctx, tx)
	if err != nil {
		return nil, err
	}
	auto, err := snapshotAuto(ctx, tx)
	if err != nil {
		return nil, err
	}
	return &document{Version: version, Auto: auto, Records: records}, nil
}

func snapshotRecords(ctx context.Context, tx *sql.Tx) ([]wireRecord, error) {
	rows, err := tx.QueryContext(ctx, `
		SELECT account_id, role_id, kind, date, result, detail, at
		FROM checkin_records
		ORDER BY date DESC, at DESC, account_id, role_id, kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []wireRecord{}
	for rows.Next() {
		var (
			accountID, role, date, detail string
			kind, result                  int
			at                            int64
		)
		if err := rows.Scan(&accountID, &role, &kind, &date, &result, &detail, &at); err != nil {
			return nil, err
		}
		records = append(records, encodeRecord(Record{
			AccountID: accountID,
			RoleID:    role,
			Kind:      Kind(kind),
			Date:      date,
			Result:    Result(result),
			Detail:    detail,
			At:        database.StampInt(at),
		}))
	}
	return records, rows.Err()
}

func snapshotAuto(ctx context.Context, tx *sql.Tx) (map[string]autoEntry, error) {
	auto := map[string]autoEntry{}
	rows, err := tx.QueryContext(ctx, "SELECT account_id, kind, enabled FROM checkin_auto")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			accountID     string
			kind, enabled int
		)
		if err := rows.Scan(&accountID, &kind, &enabled); err != nil {
			return nil, err
		}
		entry := auto[accountID]
		if Kind(kind) == Game {
			entry.Game = enabled != 0
		} else {
			entry.Community = enabled != 0
		}
		auto[accountID] = entry
	}
	return auto, rows.Err()
}

// decodeLegacy reads a records-v1.json document exactly as the file version did
// and returns its switches and records. The ledger has only ever been written as
// version 1, so any other version is a document this build cannot trust.
func decodeLegacy(raw []byte) (map[string]autoEntry, []Record, error) {
	if len(raw) > maxFileBytes {
		return nil, nil, ErrCorrupt
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	doc := &document{}
	if err := dec.Decode(doc); err != nil {
		return nil, nil, ErrCorrupt
	}
	if dec.More() {
		return nil, nil, ErrCorrupt
	}
	if doc.Version != version {
		return nil, nil, ErrCorrupt
	}
	if doc.Auto == nil {
		doc.Auto = map[string]autoEntry{}
	}
	for id := range doc.Auto {
		if !validID(id) {
			return nil, nil, ErrCorrupt
		}
	}
	seen := map[Key]bool{}
	records := make([]Record, 0, len(doc.Records))
	for _, w := range doc.Records {
		r, err := decodeRecord(w)
		if err != nil {
			return nil, nil, ErrCorrupt
		}
		if seen[r.key()] {
			return nil, nil, ErrCorrupt
		}
		seen[r.key()] = true
		records = append(records, r)
	}
	return doc.Auto, records, nil
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
