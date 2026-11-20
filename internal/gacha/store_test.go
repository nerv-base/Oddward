// SPDX-License-Identifier: GPL-3.0-or-later

package gacha

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/database/dbtest"
)

func TestStoreLifecycle(t *testing.T) {
	ctx := context.Background()
	s, _, dir := newTestStore(t)

	if _, err := s.Load(ctx, "123456"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load missing = %v, want os.ErrNotExist", err)
	}

	a := fixtureArchive()
	if err := s.Save(ctx, a); err != nil {
		t.Fatalf("Save: %v", err)
	}
	want, err := normalizeArchive(a, false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(ctx, a.RoleID)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Load = %+v, want %+v", got, want)
	}
	ids, err := s.List(ctx)
	if err != nil || !reflect.DeepEqual(ids, []string{a.RoleID}) {
		t.Fatalf("List = %v, %v, want [%s]", ids, err, a.RoleID)
	}

	mirror := filepath.Join(dir, a.RoleID+".json")
	imported, err := DecodeImport(mustRead(t, mirror))
	if err != nil {
		t.Fatalf("mirror decode: %v", err)
	}
	if !reflect.DeepEqual(imported.Archive, want) {
		t.Fatalf("mirror archive = %+v, want %+v", imported.Archive, want)
	}

	if err := s.Delete(ctx, a.RoleID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Load(ctx, a.RoleID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load after delete = %v, want os.ErrNotExist", err)
	}
	if _, err := os.Stat(mirror); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("delete kept the mirror: %v", err)
	}
	if err := s.Delete(ctx, a.RoleID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Delete missing = %v, want os.ErrNotExist", err)
	}
	stages, err := filepath.Glob(filepath.Join(dir, ".mirror-*.tmp"))
	if err != nil || len(stages) != 0 {
		t.Fatalf("mirror left a stage: %v %v", stages, err)
	}
}

func TestStoreWritesOnlyItsOwnMirror(t *testing.T) {
	ctx := context.Background()
	s, _, dir := newTestStore(t)
	first := fixtureArchive()
	if err := s.Save(ctx, first); err != nil {
		t.Fatalf("Save first: %v", err)
	}
	firstMirror := filepath.Join(dir, first.RoleID+".json")
	// A valid but different file stands in for a mirror an older build rewrote.
	different, err := ExportOddward(Archive{RoleID: first.RoleID, Records: first.Records[:1]}, fixtureTime().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(firstMirror, different, 0o600); err != nil {
		t.Fatal(err)
	}

	other := fixtureArchive()
	other.RoleID = "654321"
	if err := s.Save(ctx, other); err != nil {
		t.Fatalf("Save other: %v", err)
	}
	if after := mustRead(t, firstMirror); !reflect.DeepEqual(after, different) {
		t.Fatal("saving one role rewrote another role's mirror")
	}
}

func TestStoreCorruptionAndQuarantine(t *testing.T) {
	ctx := context.Background()
	s, _, dir := newTestStore(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	role := fixtureArchive().RoleID
	path := filepath.Join(dir, role+".json")
	original := []byte(`{"format":"oddward-gacha","version":99}`)
	if err := os.WriteFile(path, original, 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := s.Load(ctx, role); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Load corrupt = %v, want ErrCorrupt", err)
	}
	if err := s.Save(ctx, fixtureArchive()); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("Save over corrupt = %v, want ErrCorrupt", err)
	}
	if after := mustRead(t, path); !reflect.DeepEqual(after, original) {
		t.Fatal("a refused save changed the corrupt bytes")
	}

	quarantined, err := s.Quarantine(ctx, role)
	if err != nil {
		t.Fatalf("Quarantine: %v", err)
	}
	if after := mustRead(t, quarantined); !reflect.DeepEqual(after, original) {
		t.Fatal("quarantine did not preserve the bytes")
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("quarantine left the original: %v", err)
	}

	if err := s.Save(ctx, fixtureArchive()); err != nil {
		t.Fatalf("Save after quarantine: %v", err)
	}
	if _, err := s.Quarantine(ctx, role); err == nil {
		t.Fatal("quarantined a healthy archive")
	}
}

func TestStoreCanceledAndLockCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s, _, _ := newTestStore(t)

	if _, err := s.List(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("List err = %v", err)
	}
	if _, err := s.Load(ctx, "123456"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Load err = %v", err)
	}
	if err := s.Save(ctx, fixtureArchive()); !errors.Is(err, context.Canceled) {
		t.Fatalf("Save err = %v", err)
	}
	if err := s.Delete(ctx, "123456"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Delete err = %v", err)
	}
	if _, err := s.Quarantine(ctx, "123456"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Quarantine err = %v", err)
	}

	release, err := s.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	waitCtx, cancelWait := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancelWait()
	if _, err := s.List(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("blocked List err = %v, want deadline", err)
	}
}

func TestStoreRejectsUnsafePathsAndLinks(t *testing.T) {
	db := dbtest.Open(t)
	for _, dir := range []string{
		"relative",
		`C:\`,
		`\\server\share\gacha`,
		`C:\x\NUL.json`,
		`C:\x\COM1`,
		`C:\x\CONIN$`,
		`C:\x\bad.`,
		`C:\x\bad `,
		`C:\x\a:b`,
		`C:\x\a?b`,
		"C:\\x\\a\nb",
	} {
		if _, err := NewStore(db, dir); err == nil {
			t.Errorf("NewStore accepted %q", dir)
		}
	}
	if _, err := NewStore(nil, `C:\x\gacha`); err == nil {
		t.Fatal("NewStore accepted a nil database")
	}

	dir := filepath.Join(t.TempDir(), "gacha")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(source, mustReadExample(t), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(source, filepath.Join(dir, "123456.json")); err != nil {
		t.Skipf("hard links unavailable: %v", err)
	}
	s, err := NewStore(db, dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := s.Load(ctx, "123456"); err == nil {
		t.Fatal("Load followed a multi-link archive")
	}
	if err := s.Save(ctx, fixtureArchive()); err == nil {
		t.Fatal("Save replaced a multi-link archive")
	}
	if err := s.Delete(ctx, "123456"); err == nil {
		t.Fatal("Delete removed a multi-link archive")
	}
	if _, err := s.Quarantine(ctx, "123456"); err == nil {
		t.Fatal("Quarantine moved a multi-link archive")
	}
}

// mustReadExample is a valid archive document used where the content itself does
// not matter, only that the path is reachable.
func mustReadExample(t *testing.T) []byte {
	t.Helper()
	data, err := ExportOddward(fixtureArchive(), fixtureTime())
	if err != nil {
		t.Fatal(err)
	}
	return data
}
