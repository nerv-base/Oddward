// SPDX-License-Identifier: GPL-3.0-or-later

package checkin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/database/dbtest"
)

func testDay(offset int) string {
	return time.Now().AddDate(0, 0, offset).Format(dateLayout)
}

func gameRecord(day string) Record {
	return Record{AccountID: "acc-1", RoleID: "123", Kind: Game, Date: day, Result: Success, At: time.Now()}
}

// openTest opens a store over a throwaway database. The second result is the
// directory the store keeps its lease and mirror in.
func openTest(t *testing.T) (*Store, string) {
	t.Helper()
	s, err := Open(dbtest.Open(t), filepath.Join(t.TempDir(), "checkin"))
	if err != nil {
		t.Fatal(err)
	}
	return s, s.dir
}

// reopen returns a second Store on the same database and directory, the way a
// second launcher process would open it.
func reopen(t *testing.T, s *Store) *Store {
	t.Helper()
	again, err := Open(s.db, s.dir)
	if err != nil {
		t.Fatal(err)
	}
	return again
}

// mirrorOf reads the rollback file the store keeps beside the database.
func mirrorOf(t *testing.T, s *Store) document {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(s.dir, fileName))
	if err != nil {
		t.Fatal(err)
	}
	var doc document
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("%s is not a ledger: %v", fileName, err)
	}
	return doc
}

// writeLegacy puts a document on disk the way a rollback build leaves it.
func writeLegacy(t *testing.T, dir string, records []Record, auto map[string]autoEntry) {
	t.Helper()
	if auto == nil {
		auto = map[string]autoEntry{}
	}
	doc := document{Version: version, Auto: auto, Records: []wireRecord{}}
	for _, r := range records {
		doc.Records = append(doc.Records, encodeRecord(r))
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fileName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func countRows(t *testing.T, s *Store) int {
	t.Helper()
	var n int
	if err := s.db.QueryRow(context.Background(), "SELECT COUNT(*) FROM checkin_records").Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// dumpLedger renders every row so two imports can be compared exactly.
func dumpLedger(t *testing.T, s *Store) string {
	t.Helper()
	ctx := context.Background()
	var b strings.Builder
	rows, err := s.db.Query(ctx, `
		SELECT account_id, role_id, kind, date, result, detail, at
		FROM checkin_records ORDER BY account_id, role_id, kind, date`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var accountID, roleID, date, detail string
		var kind, result int
		var at int64
		if err := rows.Scan(&accountID, &roleID, &kind, &date, &result, &detail, &at); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "record %q %q %d %q %d %q %d\n", accountID, roleID, kind, date, result, detail, at)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	switches, err := s.db.Query(ctx, "SELECT account_id, kind, enabled FROM checkin_auto ORDER BY account_id, kind")
	if err != nil {
		t.Fatal(err)
	}
	for switches.Next() {
		var accountID string
		var kind, enabled int
		if err := switches.Scan(&accountID, &kind, &enabled); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&b, "switch %q %d %d\n", accountID, kind, enabled)
	}
	if err := switches.Err(); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// legacyTrim is the file store's trim, kept as the oracle the database prune is
// measured against. It mirrors the code the migration replaced.
func legacyTrim(records []wireRecord, now time.Time) []wireRecord {
	cutoff := now.AddDate(0, 0, -keepDays).Format(dateLayout)
	kept := make([]wireRecord, 0, len(records))
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

func TestPutLookupSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	s, _ := openTest(t)
	want := gameRecord(testDay(0))
	if err := s.Put(ctx, want); err != nil {
		t.Fatal(err)
	}
	again := reopen(t, s)
	got, ok, err := again.Lookup(ctx, want.key())
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("a second store must see the record")
	}
	if got.Result != want.Result || got.Detail != want.Detail || !got.At.Equal(want.At) {
		t.Fatalf("record = %+v, want %+v", got, want)
	}
	other := Key{AccountID: "acc-1", RoleID: "123", Kind: Game, Date: testDay(-1)}
	if _, ok, err := again.Lookup(ctx, other); err != nil || ok {
		t.Fatalf("other day: ok=%v err=%v", ok, err)
	}
}

func TestPutReplacesSameDay(t *testing.T) {
	ctx := context.Background()
	s, _ := openTest(t)
	day := testDay(0)
	if err := s.Put(ctx, gameRecord(day)); err != nil {
		t.Fatal(err)
	}
	want := gameRecord(day)
	want.Result = Unverified
	want.Detail = "timeout"
	if err := s.Put(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Lookup(ctx, want.key())
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	if got.Result != Unverified || got.Detail != "timeout" {
		t.Fatalf("record = %s, want unverified/timeout", got.Describe())
	}
	if n := countRows(t, s); n != 1 {
		t.Fatalf("rows = %d, want one entry per key", n)
	}
	if doc := mirrorOf(t, s); len(doc.Records) != 1 {
		t.Fatalf("mirror records = %d, want 1", len(doc.Records))
	}
}

func TestAutoPerAccount(t *testing.T) {
	ctx := context.Background()
	s, _ := openTest(t)
	if err := s.SetAuto(ctx, "acc-1", Game, true); err != nil {
		t.Fatal(err)
	}
	again := reopen(t, s)
	got, err := again.Auto(ctx, "acc-1")
	if err != nil {
		t.Fatal(err)
	}
	if got != [kinds]bool{true, false} {
		t.Fatalf("auto = %v", got)
	}
	if other, err := again.Auto(ctx, "acc-2"); err != nil || other != [kinds]bool{} {
		t.Fatalf("other account = %v, %v", other, err)
	}
	if doc := mirrorOf(t, again); doc.Auto["acc-1"] != (autoEntry{Game: true}) {
		t.Fatalf("mirror auto = %+v", doc.Auto)
	}
}

func TestPruneKeepsTheBoundary(t *testing.T) {
	ctx := context.Background()
	s, _ := openTest(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	kept := gameRecord(Day(now.AddDate(0, 0, -keepDays)))
	gone := gameRecord(Day(now.AddDate(0, 0, -keepDays-1)))
	if err := s.Put(ctx, gone); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, kept); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Lookup(ctx, kept.key()); err != nil || !ok {
		t.Fatalf("the oldest kept day must survive: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.Lookup(ctx, gone.key()); err != nil || ok {
		t.Fatalf("a day past the limit must be pruned: ok=%v err=%v", ok, err)
	}
}

func TestPruneKeepsTheNewestRecords(t *testing.T) {
	ctx := context.Background()
	s, dir := openTest(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	base := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	legacy := make([]Record, 0, maxRecords+3)
	for i := 0; i < maxRecords+3; i++ {
		legacy = append(legacy, Record{
			AccountID: "acc-1",
			RoleID:    fmt.Sprintf("r%03d", i),
			Kind:      Game,
			Date:      Day(base),
			Result:    Failed,
			At:        base.Add(time.Duration(i) * time.Second),
		})
	}
	writeLegacy(t, dir, legacy, nil)
	if err := s.ImportLegacy(ctx); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s); n != maxRecords+3 {
		t.Fatalf("the import must keep every row: rows = %d", n)
	}
	fresh := Record{AccountID: "acc-1", RoleID: "fresh", Kind: Game, Date: Day(base), Result: Pending, At: base.Add(time.Duration(maxRecords+3) * time.Second)}
	if err := s.Put(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, s); n != maxRecords {
		t.Fatalf("rows = %d, want %d", n, maxRecords)
	}
	dropped := 4 // three extra imported records plus the one the cap now excludes
	for i := 0; i < dropped; i++ {
		k := Key{AccountID: "acc-1", RoleID: fmt.Sprintf("r%03d", i), Kind: Game, Date: Day(base)}
		if _, ok, err := s.Lookup(ctx, k); err != nil || ok {
			t.Fatalf("record %d must be pruned: ok=%v err=%v", i, ok, err)
		}
	}
	if _, ok, err := s.Lookup(ctx, fresh.key()); err != nil || !ok {
		t.Fatalf("the newest record must survive: ok=%v err=%v", ok, err)
	}
}

func TestPruneMatchesTheFileTrim(t *testing.T) {
	ctx := context.Background()
	s, dir := openTest(t)
	now := time.Now()
	s.now = func() time.Time { return now }
	base := time.Date(now.Year(), now.Month(), now.Day(), 9, 0, 0, 0, now.Location())
	var legacy []Record
	for i, offset := range []int{0, -1, -30, -59, -60, -61, -120} {
		legacy = append(legacy, Record{
			AccountID: "acc-a",
			RoleID:    fmt.Sprintf("r%d", i),
			Kind:      Game,
			Date:      Day(base.AddDate(0, 0, offset)),
			Result:    Success,
			Detail:    fmt.Sprintf("第 %d 天", -offset),
			At:        base.AddDate(0, 0, offset),
		})
	}
	legacy = append(legacy, Record{AccountID: "acc-b", Kind: Community, Date: Day(base), Result: Already, At: base.Add(2 * time.Hour)})
	auto := map[string]autoEntry{"acc-a": {Game: true}}
	writeLegacy(t, dir, legacy, auto)
	if err := s.ImportLegacy(ctx); err != nil {
		t.Fatal(err)
	}
	// The import does not prune: a rollback build must find everything back.
	if _, ok, err := s.Lookup(ctx, legacy[6].key()); err != nil || !ok {
		t.Fatalf("the import must keep the oldest record: ok=%v err=%v", ok, err)
	}
	// Any write prunes, exactly like the file store's save() did.
	fresh := Record{AccountID: "acc-c", RoleID: "9", Kind: Game, Date: Day(base), Result: Pending, At: base.Add(time.Minute)}
	if err := s.Put(ctx, fresh); err != nil {
		t.Fatal(err)
	}
	all := append(append([]Record{}, legacy...), fresh)
	var input []wireRecord
	for _, r := range all {
		input = append(input, encodeRecord(r))
	}
	want := legacyTrim(input, now)
	doc := mirrorOf(t, s)
	if !reflect.DeepEqual(doc.Records, want) {
		t.Fatalf("mirror records =\n%v\nwant\n%v", doc.Records, want)
	}
	if !reflect.DeepEqual(doc.Auto, auto) {
		t.Fatalf("mirror auto = %+v, want %+v", doc.Auto, auto)
	}
	if n := countRows(t, s); n != len(want) {
		t.Fatalf("rows = %d, want %d", n, len(want))
	}
	cutoff := Day(now.AddDate(0, 0, -keepDays))
	for _, r := range all {
		_, ok, err := s.Lookup(ctx, r.key())
		if err != nil {
			t.Fatal(err)
		}
		if keep := r.Date >= cutoff; keep != ok {
			t.Fatalf("%s present = %v, want %v", r.Describe(), ok, keep)
		}
	}
}

func TestPutMarksTheMirrorFresh(t *testing.T) {
	ctx := context.Background()
	s, _ := openTest(t)
	if err := s.Put(ctx, gameRecord(testDay(0))); err != nil {
		t.Fatal(err)
	}
	stale, err := s.db.MirrorStale(ctx, domain)
	if err != nil {
		t.Fatal(err)
	}
	if stale {
		t.Fatal("a completed write must leave a mirror that matches its digest")
	}
}

func TestConcurrentPutsKeepEveryRecord(t *testing.T) {
	ctx := context.Background()
	s, _ := openTest(t)
	const workers, perWorker = 8, 8
	var wg sync.WaitGroup
	errs := make(chan error, workers*perWorker)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < perWorker; i++ {
				r := Record{
					AccountID: "acc-1",
					RoleID:    fmt.Sprintf("w%d-%d", w, i),
					Kind:      Game,
					Date:      testDay(0),
					Result:    Failed,
					At:        time.Now(),
				}
				if err := s.Put(ctx, r); err != nil {
					errs <- err
				}
			}
		}(w)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if n := countRows(t, s); n != workers*perWorker {
		t.Fatalf("rows = %d, want %d", n, workers*perWorker)
	}
}

func TestRejectsBadRecords(t *testing.T) {
	ctx := context.Background()
	s, _ := openTest(t)
	day := testDay(0)
	cases := map[string]Record{
		"empty account": {Kind: Game, Date: day, Result: Success, At: time.Now()},
		"bad kind":      {AccountID: "acc-1", Kind: 2, Date: day, Result: Success, At: time.Now()},
		"bad date":      {AccountID: "acc-1", Kind: Game, Date: "2026-13-40", Result: Success, At: time.Now()},
		"bad result":    {AccountID: "acc-1", Kind: Game, Date: day, Result: 5, At: time.Now()},
		"missing time":  {AccountID: "acc-1", Kind: Game, Date: day, Result: Success},
		"long detail":   {AccountID: "acc-1", Kind: Game, Date: day, Result: Success, Detail: strings.Repeat("x", maxDetailLen+1), At: time.Now()},
		"control text":  {AccountID: "acc-1", Kind: Game, Date: day, Result: Success, Detail: "bad\nline", At: time.Now()},
		"space in id":   {AccountID: "acc 1", Kind: Game, Date: day, Result: Success, At: time.Now()},
	}
	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			if err := s.Put(ctx, r); err == nil {
				t.Fatal("Put accepted a record it must refuse")
			}
		})
	}
	if err := s.Put(ctx, gameRecord(day)); err != nil {
		t.Fatalf("a valid record was refused: %v", err)
	}
}

func TestImportReadsEveryRecordAndSwitch(t *testing.T) {
	ctx := context.Background()
	s, dir := openTest(t)
	at := func(day, hour int) time.Time { return time.Date(2026, 1, day, hour, 0, 0, 0, time.UTC) }
	legacy := []Record{
		{AccountID: "acc-a", RoleID: "123", Kind: Game, Date: "2026-01-05", Result: Success, Detail: "经验 +10", At: at(5, 9)},
		{AccountID: "acc-a", RoleID: "456", Kind: Game, Date: "2026-01-05", Result: Already, At: at(5, 10)},
		{AccountID: "acc-a", Kind: Community, Date: "2026-01-05", Result: Success, Detail: "金币 +5", At: at(5, 11)},
		{AccountID: "acc-b", RoleID: "123", Kind: Game, Date: "2026-01-04", Result: Failed, Detail: "网络错误", At: at(4, 9)},
		{AccountID: "acc-b", Kind: Community, Date: "2026-01-04", Result: Pending, At: at(4, 10)},
		{AccountID: "acc-a", RoleID: "123", Kind: Game, Date: "2026-01-03", Result: Unverified, At: at(3, 9)},
	}
	auto := map[string]autoEntry{
		"acc-a": {Game: true},
		"acc-b": {Community: true},
	}
	writeLegacy(t, dir, legacy, auto)
	if err := s.ImportLegacy(ctx); err != nil {
		t.Fatal(err)
	}
	for _, want := range legacy {
		got, ok, err := s.Lookup(ctx, want.key())
		if err != nil || !ok {
			t.Fatalf("%s: ok=%v err=%v", want.Describe(), ok, err)
		}
		if got.Result != want.Result || got.Detail != want.Detail || got.RoleID != want.RoleID ||
			got.Kind != want.Kind || got.Date != want.Date || !got.At.Equal(want.At) {
			t.Fatalf("record = %+v, want %+v", got, want)
		}
	}
	for accountID, want := range auto {
		got, err := s.Auto(ctx, accountID)
		if err != nil {
			t.Fatal(err)
		}
		if got != [kinds]bool{want.Game, want.Community} {
			t.Fatalf("auto(%s) = %v, want %+v", accountID, got, want)
		}
	}
	if doc := mirrorOf(t, s); len(doc.Records) != len(legacy) {
		t.Fatalf("mirror records = %d, want %d", len(doc.Records), len(legacy))
	}
}

func TestImportMissingFileDoesNothing(t *testing.T) {
	ctx := context.Background()
	s, dir := openTest(t)
	for i := 0; i < 2; i++ {
		if err := s.ImportLegacy(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if n := countRows(t, s); n != 0 {
		t.Fatalf("rows = %d", n)
	}
	if _, err := os.Stat(filepath.Join(dir, fileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("an absent legacy file must not be recreated: %v", err)
	}
}

func TestImportTwiceKeepsTheSameRows(t *testing.T) {
	ctx := context.Background()
	s, dir := openTest(t)
	writeLegacy(t, dir, []Record{gameRecord("2026-01-05")}, map[string]autoEntry{"acc-1": {Game: true}})
	if err := s.ImportLegacy(ctx); err != nil {
		t.Fatal(err)
	}
	before := dumpLedger(t, s)
	// The same document written with different bytes is still a rollback
	// rewrite, so the importer runs a second time.
	path := filepath.Join(dir, fileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.ImportLegacy(ctx); err != nil {
		t.Fatal(err)
	}
	if after := dumpLedger(t, s); after != before {
		t.Fatalf("ledger after the second import =\n%s\nwant\n%s", after, before)
	}
}

func TestImportReconcilesARewrittenMirror(t *testing.T) {
	ctx := context.Background()
	s, dir := openTest(t)
	first := Record{AccountID: "acc-a", RoleID: "1", Kind: Game, Date: "2026-02-01", Result: Success, At: time.Date(2026, 2, 1, 9, 0, 0, 0, time.UTC)}
	writeLegacy(t, dir, []Record{first}, nil)
	if err := s.ImportLegacy(ctx); err != nil {
		t.Fatal(err)
	}
	second := Record{AccountID: "acc-b", Kind: Community, Date: "2026-02-02", Result: Already, At: time.Date(2026, 2, 2, 9, 0, 0, 0, time.UTC)}
	if err := s.Put(ctx, second); err != nil {
		t.Fatal(err)
	}
	// A rolled-back build rewrote the file with what it knew: one older record
	// and its switch.
	older := Record{AccountID: "acc-a", RoleID: "1", Kind: Game, Date: "2026-01-31", Result: Already, At: time.Date(2026, 1, 31, 9, 0, 0, 0, time.UTC)}
	writeLegacy(t, dir, []Record{first, older}, map[string]autoEntry{"acc-a": {Game: true}})
	if err := s.ImportLegacy(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Lookup(ctx, second.key()); err != nil || ok {
		t.Fatalf("the reconciled file is authoritative: ok=%v err=%v", ok, err)
	}
	if _, ok, err := s.Lookup(ctx, older.key()); err != nil || !ok {
		t.Fatalf("the rollback build's record must be imported: ok=%v err=%v", ok, err)
	}
	if got, err := s.Auto(ctx, "acc-a"); err != nil || got != [kinds]bool{true, false} {
		t.Fatalf("auto = %v, %v", got, err)
	}
}

func TestImportRejectsCorruptLedger(t *testing.T) {
	bodies := map[string]string{
		"truncated":     "{",
		"other version": `{"version":2,"auto":{},"records":[]}`,
		"unknown field": `{"version":1,"auto":{},"records":[],"note":"x"}`,
		"bad kind": `{"version":1,"auto":{},"records":[{"accountId":"acc-1","kind":"raid",` +
			`"date":"2026-01-01","result":"success","at":"2026-01-01T00:00:00Z"}]}`,
		"duplicate key": `{"version":1,"auto":{},"records":[` +
			`{"accountId":"acc-1","kind":"game","date":"2026-01-01","result":"success","at":"2026-01-01T00:00:00Z"},` +
			`{"accountId":"acc-1","kind":"game","date":"2026-01-01","result":"failed","at":"2026-01-02T00:00:00Z"}]}`,
	}
	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			s, dir := openTest(t)
			if err := s.Put(ctx, gameRecord(testDay(0))); err != nil {
				t.Fatal(err)
			}
			before := dumpLedger(t, s)
			path := filepath.Join(dir, fileName)
			if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			err := s.ImportLegacy(ctx)
			if !errors.Is(err, ErrCorrupt) {
				t.Fatalf("err = %v, want ErrCorrupt", err)
			}
			if !strings.Contains(err.Error(), path) {
				t.Fatalf("err = %v, want it to name the file", err)
			}
			raw, readErr := os.ReadFile(path)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if string(raw) != body {
				t.Fatal("the importer rewrote a file it could not read")
			}
			if after := dumpLedger(t, s); after != before {
				t.Fatalf("rows changed:\n%s\nwant\n%s", after, before)
			}
		})
	}
}

func TestImportRejectsOversizedLedger(t *testing.T) {
	ctx := context.Background()
	s, dir := openTest(t)
	detail := strings.Repeat("签", maxDetailLen)
	records := make([]Record, 0, 1200)
	for i := 0; i < 1200; i++ {
		records = append(records, Record{
			AccountID: fmt.Sprintf("acc-%04d", i),
			RoleID:    "1",
			Kind:      Game,
			Date:      "2026-01-01",
			Result:    Success,
			Detail:    detail,
			At:        time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		})
	}
	writeLegacy(t, dir, records, nil)
	path := filepath.Join(dir, fileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) <= maxFileBytes {
		t.Fatalf("fixture is only %d bytes", len(raw))
	}
	if err := s.ImportLegacy(ctx); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v, want ErrCorrupt", err)
	}
	if again, err := os.ReadFile(path); err != nil || string(again) != string(raw) {
		t.Fatalf("the oversized file must be left alone: %v", err)
	}
	if n := countRows(t, s); n != 0 {
		t.Fatalf("rows = %d", n)
	}
}

func TestLockSignKeepsASecondAttemptOut(t *testing.T) {
	s, _ := openTest(t)
	other := reopen(t, s)
	release, err := s.LockSign()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := other.LockSign(); !errors.Is(err, ErrBusy) {
		t.Fatalf("second lease err = %v, want ErrBusy", err)
	}
	release()
	again, err := other.LockSign()
	if err != nil {
		t.Fatalf("a released lease must be free: %v", err)
	}
	again()
}

func TestTwoStoresShareOneLedger(t *testing.T) {
	ctx := context.Background()
	s, _ := openTest(t)
	other := reopen(t, s)
	rec := gameRecord(testDay(0))
	if err := s.Put(ctx, rec); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := other.Lookup(ctx, rec.key()); err != nil || !ok {
		t.Fatalf("a second store must see committed rows: ok=%v err=%v", ok, err)
	}
	if err := other.SetAuto(ctx, "acc-1", Community, true); err != nil {
		t.Fatal(err)
	}
	if got, err := s.Auto(ctx, "acc-1"); err != nil || got != [kinds]bool{false, true} {
		t.Fatalf("auto = %v, %v", got, err)
	}
}

func TestNoTempFilesRemain(t *testing.T) {
	ctx := context.Background()
	s, dir := openTest(t)
	if err := s.Put(ctx, gameRecord(testDay(0))); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{fileName: true, ledgerLockName: true, signLockName: true}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			t.Fatalf("left %s behind in %s", entry.Name(), dir)
		}
	}
}

func TestOpenNeedsAbsoluteDirectory(t *testing.T) {
	if _, err := Open(dbtest.Open(t), "records"); err == nil {
		t.Fatal("relative directory accepted")
	}
	if _, err := Open(nil, filepath.Join(t.TempDir(), "checkin")); err == nil {
		t.Fatal("missing database accepted")
	}
}

func TestLookupRejectsBadKey(t *testing.T) {
	s, _ := openTest(t)
	bad := Key{AccountID: "acc 1", Kind: Game, Date: testDay(0)}
	if _, _, err := s.Lookup(context.Background(), bad); err == nil {
		t.Fatal("bad key accepted")
	}
}

func TestDayUsesLocalCalendar(t *testing.T) {
	zone := time.FixedZone("UTC+8", 8*60*60)
	at := time.Date(2026, 3, 1, 0, 30, 0, 0, zone)
	if got := Day(at); got != "2026-03-01" {
		t.Fatalf("Day = %s", got)
	}
}

func TestDayUsesTheMomentsOwnZone(t *testing.T) {
	// The same instant is a different day in two zones; the record must follow
	// the zone the moment was reported in.
	zone := time.FixedZone("UTC+9", 9*60*60)
	at := time.Date(2026, 3, 1, 0, 30, 0, 0, zone)
	if got := Day(at.UTC()); got != "2026-02-28" {
		t.Fatalf("Day = %s", got)
	}
}
