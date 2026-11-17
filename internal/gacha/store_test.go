// SPDX-License-Identifier: GPL-3.0-or-later

package gacha

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestStoreLifecycle(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "gacha")
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Load(ctx, "123456"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing: %v", err)
	}
	a := fixtureArchive()
	if err = s.Save(ctx, a); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(ctx, a.RoleID)
	if err != nil || !reflect.DeepEqual(got, a) {
		t.Fatalf("load %+v %v", got, err)
	}
	if err = os.WriteFile(filepath.Join(dir, "other.json"), []byte("ignore"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(dir, "999.json.corrupt-old"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	ids, err := s.List(ctx)
	if err != nil || !reflect.DeepEqual(ids, []string{a.RoleID}) {
		t.Fatalf("list %v %v", ids, err)
	}
	if err = s.Delete(ctx, a.RoleID); err != nil {
		t.Fatal(err)
	}
	if err = s.Delete(ctx, a.RoleID); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("delete missing", err)
	}
	files, err := filepath.Glob(filepath.Join(dir, ".gacha-*.tmp"))
	if err != nil || len(files) != 0 {
		t.Fatal("stages", files, err)
	}
}

func TestStoreCorruptionAndQuarantine(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := fixtureArchive()
	path := filepath.Join(dir, a.RoleID+".json")
	original := []byte(`{"format":"oddward-gacha","version":99}`)
	if err = os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Load(ctx, a.RoleID); !errors.Is(err, ErrCorrupt) {
		t.Fatal("load corrupt", err)
	}
	if err = s.Save(ctx, a); !errors.Is(err, ErrCorrupt) {
		t.Fatal("save corrupt", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(data, original) {
		t.Fatal("corrupt file changed")
	}
	quarantined, err := s.Quarantine(ctx, a.RoleID)
	if err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(quarantined)
	if err != nil || !bytes.Equal(data, original) {
		t.Fatal("quarantine changed bytes")
	}
	if err = s.Save(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Quarantine(ctx, a.RoleID); err == nil {
		t.Fatal("quarantined healthy archive")
	}
}

func TestStoreCanceledAndLockCancellation(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = s.List(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("list cancellation", err)
	}
	if _, err = s.Load(ctx, "123456"); !errors.Is(err, context.Canceled) {
		t.Fatal("load cancellation", err)
	}
	if err = s.Save(ctx, fixtureArchive()); !errors.Is(err, context.Canceled) {
		t.Fatal("save cancellation", err)
	}
	if err = s.Delete(ctx, "123456"); !errors.Is(err, context.Canceled) {
		t.Fatal("delete cancellation", err)
	}
	if _, err = s.Quarantine(ctx, "123456"); !errors.Is(err, context.Canceled) {
		t.Fatal("quarantine cancellation", err)
	}
	release, err := s.acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err = s.List(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("waiting lock cancellation", err)
	}
}

func TestStoreRejectsUnsafePathsAndLinks(t *testing.T) {
	for _, bad := range []string{`relative`, `C:\`, `\\server\share\gacha`, `C:\x\NUL.json`, `C:\x\COM1`, `C:\x\CONIN$`, `C:\x\bad.`, `C:\x\bad `, `C:\x\a:b`, `C:\x\a?b`, "C:\\x\\a\nb"} {
		if _, err := NewStore(bad); err == nil {
			t.Errorf("accepted path %q", bad)
		}
	}
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := fixtureArchive()
	b, err := ExportOddward(a, fixtureTime())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "target.json")
	if err = os.WriteFile(target, b, 0600); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(dir, a.RoleID+".json")
	if err = os.Link(target, linked); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Load(context.Background(), a.RoleID); err == nil {
		t.Fatal("hard link load")
	}
	if err = s.Save(context.Background(), a); err == nil {
		t.Fatal("hard link save")
	}
	if err = s.Delete(context.Background(), a.RoleID); err == nil {
		t.Fatal("hard link delete")
	}
	if _, err = s.Quarantine(context.Background(), a.RoleID); err == nil {
		t.Fatal("hard link quarantine")
	}
}
