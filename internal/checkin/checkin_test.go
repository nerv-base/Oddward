// SPDX-License-Identifier: GPL-3.0-or-later
package checkin

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testDay(offset int) string {
	return time.Now().AddDate(0, 0, offset).In(time.Local).Format(dateLayout)
}

func openTest(t *testing.T) (*Store, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "checkin")
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func gameRecord(day string) Record {
	return Record{AccountID: "acc-1", RoleID: "123", Kind: Game, Date: day, Result: Success, At: time.Now()}
}

func TestPutLookupSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	s, dir := openTest(t)
	day := testDay(0)
	if err := s.Put(ctx, gameRecord(day)); err != nil {
		t.Fatal(err)
	}
	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok, err := again.Lookup(ctx, Key{AccountID: "acc-1", RoleID: "123", Kind: Game, Date: day})
	if err != nil || !ok {
		t.Fatalf("lookup ok=%v err=%v", ok, err)
	}
	if got.Result != Success || got.Kind != Game {
		t.Fatalf("record = %+v", got)
	}
	if _, ok, _ := again.Lookup(ctx, Key{AccountID: "acc-1", RoleID: "999", Kind: Game, Date: day}); ok {
		t.Fatal("unexpected record for another role")
	}
}
func TestPutReplacesSameDay(t *testing.T) {
	ctx := context.Background()
	s, _ := openTest(t)
	day := testDay(0)
	if err := s.Put(ctx, Record{AccountID: "acc-1", RoleID: "123", Kind: Game, Date: day, Result: Pending, At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := s.Put(ctx, Record{AccountID: "acc-1", RoleID: "123", Kind: Game, Date: day, Result: Unverified, Detail: "timeout", At: time.Now()}); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Lookup(ctx, Key{AccountID: "acc-1", RoleID: "123", Kind: Game, Date: day})
	if err != nil || !ok || got.Result != Unverified || got.Detail != "timeout" {
		t.Fatalf("got = %+v ok = %v err = %v", got, ok, err)
	}
	raw, err := os.ReadFile(filepath.Join(s.dir, fileName))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Records []struct{ Result string } `json:"records"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Records) != 1 {
		t.Fatalf("records = %d", len(doc.Records))
	}
}
func TestAutoPerAccount(t *testing.T) {
	ctx := context.Background()
	s, dir := openTest(t)
	if err := s.SetAuto(ctx, "acc-1", Game, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetAuto(ctx, "acc-1", Community, false); err != nil {
		t.Fatal(err)
	}
	again, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := again.Auto(ctx, "acc-1")
	if err != nil {
		t.Fatal(err)
	}
	if !got[Game] || got[Community] {
		t.Fatalf("auto = %v", got)
	}
	other, err := again.Auto(ctx, "acc-2")
	if err != nil {
		t.Fatal(err)
	}
	if other[Game] || other[Community] {
		t.Fatalf("another account inherited switches: %v", other)
	}
}
func TestTrimDropsOldDays(t *testing.T) {
	ctx := context.Background()
	s, _ := openTest(t)
	if err := s.Put(ctx, gameRecord(testDay(-keepDays-1))); err != nil {
		t.Fatal(err)
	}
	fresh := testDay(0)
	if err := s.Put(ctx, gameRecord(fresh)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Lookup(ctx, Key{AccountID: "acc-1", RoleID: "123", Kind: Game, Date: testDay(-keepDays - 1)}); err != nil || ok {
		t.Fatalf("old record survived ok=%v err=%v", ok, err)
	}
	if _, ok, _ := s.Lookup(ctx, Key{AccountID: "acc-1", RoleID: "123", Kind: Game, Date: fresh}); !ok {
		t.Fatal("fresh record lost")
	}
}
func TestLedgerStaysBounded(t *testing.T) {
	ctx := context.Background()
	s, _ := openTest(t)
	day := testDay(0)
	for i := 0; i < maxRecords+20; i++ {
		r := Record{AccountID: "acc-1", RoleID: string(rune('a'+i%26)) + string(rune('a'+i/26)), Kind: Game, Date: day, Result: Failed, At: time.Now()}
		if err := s.Put(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	raw, err := os.ReadFile(filepath.Join(s.dir, fileName))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Records []json.RawMessage `json:"records"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	if len(doc.Records) > maxRecords {
		t.Fatalf("records = %d", len(doc.Records))
	}
}
func TestRejectsBadRecords(t *testing.T) {
	cases := map[string]Record{
		"no account":   {RoleID: "123", Kind: Game, Date: testDay(0), At: time.Now()},
		"bad date":     {AccountID: "acc-1", RoleID: "123", Kind: Game, Date: "2026-2-3", At: time.Now()},
		"bad kind":     {AccountID: "acc-1", RoleID: "123", Kind: Kind(9), Date: testDay(0), At: time.Now()},
		"bad result":   {AccountID: "acc-1", RoleID: "123", Kind: Game, Date: testDay(0), Result: Result(9), At: time.Now()},
		"no time":      {AccountID: "acc-1", RoleID: "123", Kind: Game, Date: testDay(0)},
		"long detail":  {AccountID: "acc-1", RoleID: "123", Kind: Game, Date: testDay(0), Detail: string(make([]byte, 200)), At: time.Now()},
		"space id":     {AccountID: "acc 1", RoleID: "123", Kind: Game, Date: testDay(0), At: time.Now()},
		"control text": {AccountID: "acc-1", RoleID: "123", Kind: Game, Date: testDay(0), Detail: "bad\nline", At: time.Now()},
	}
	for name, r := range cases {
		t.Run(name, func(t *testing.T) {
			s, _ := openTest(t)
			if err := s.Put(context.Background(), r); err == nil {
				t.Fatal("accepted invalid record")
			}
		})
	}
}
func TestCorruptLedgerIsReported(t *testing.T) {
	cases := map[string]string{
		"not json":      "{",
		"wrong version": `{"version":2,"auto":{},"records":[]}`,
		"unknown field": `{"version":1,"auto":{},"records":[],"extra":1}`,
		"bad kind":      `{"version":1,"auto":{},"records":[{"accountId":"acc-1","kind":"raid","date":"2026-01-01","result":"success","at":"2026-01-01T00:00:00Z"}]}`,
		"duplicates":    `{"version":1,"auto":{},"records":[{"accountId":"acc-1","kind":"game","date":"2026-01-01","result":"success","at":"2026-01-01T00:00:00Z"},{"accountId":"acc-1","kind":"game","date":"2026-01-01","result":"already","at":"2026-01-01T00:00:00Z"}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "checkin")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, fileName), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Open(dir); !errors.Is(err, ErrCorrupt) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}
func TestOversizedLedgerIsRejected(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "checkin")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	big := make([]byte, maxFileBytes+1)
	for i := range big {
		big[i] = ' '
	}
	if err := os.WriteFile(filepath.Join(dir, fileName), big, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dir); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("err = %v", err)
	}
}
func TestBusyLedgerIsDistinct(t *testing.T) {
	s, dir := openTest(t)
	lock, err := lockFile(filepath.Join(dir, "lock"))
	if err != nil {
		t.Fatal(err)
	}
	defer unlock(lock)
	err = s.Put(context.Background(), gameRecord(testDay(0)))
	if !errors.Is(err, ErrBusy) {
		t.Fatalf("err = %v", err)
	}
}
func TestTwoStoresShareOneLedger(t *testing.T) {
	ctx := context.Background()
	_, dir := openTest(t)
	first, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	day := testDay(0)
	if err := first.Put(ctx, gameRecord(day)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := second.Lookup(ctx, Key{AccountID: "acc-1", RoleID: "123", Kind: Game, Date: day}); err != nil || !ok {
		t.Fatalf("second process missed the record ok=%v err=%v", ok, err)
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
	for _, e := range entries {
		name := e.Name()
		if name == fileName || name == ledgerLockName || name == signLockName {
			continue
		}
		t.Fatalf("leftover file %q", name)
	}
}
func TestLockSignKeepsASecondAttemptOut(t *testing.T) {
	_, dir := openTest(t)
	first, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	release, err := first.LockSign()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := second.LockSign(); !errors.Is(err, ErrBusy) {
		t.Fatalf("second attempt err = %v, want ErrBusy", err)
	}
	release()
	again, err := second.LockSign()
	if err != nil {
		t.Fatalf("lease after release: %v", err)
	}
	again()
}

func TestOpenNeedsAbsoluteDirectory(t *testing.T) {
	if _, err := Open("relative"); err == nil {
		t.Fatal("relative directory accepted")
	}
}
func TestDayUsesLocalCalendar(t *testing.T) {
	// 00:30 in a +08:00 zone is still the previous day in UTC.
	loc := time.FixedZone("UTC+8", 8*3600)
	instant := time.Date(2026, 3, 2, 0, 30, 0, 0, loc)
	got := Day(instant)
	if got != "2026-03-02" {
		t.Fatalf("Day = %q", got)
	}
}
