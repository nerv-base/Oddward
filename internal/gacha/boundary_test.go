// SPDX-License-Identifier: GPL-3.0-or-later

package gacha

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStoreFailedReplacementCleansStage(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := fixtureArchive()
	if err := s.Save(ctx, a); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, a.RoleID+".json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0444); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(path, 0600); err != nil {
			t.Error(err)
		}
	})
	a.Records[0].Pity = 20
	if err := s.Save(ctx, a); err == nil {
		t.Fatal("replaced a read-only target")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("failed replacement changed archive", err)
	}
	stages, err := filepath.Glob(filepath.Join(dir, ".gacha-*.tmp"))
	if err != nil || len(stages) != 0 {
		t.Fatal("failed replacement left stages", stages, err)
	}
}

func TestStoredSchemaAndSizeBoundaries(t *testing.T) {
	good, err := ExportOddward(fixtureArchive(), fixtureTime())
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"wrong role":    bytes.Replace(good, []byte(`"role_id":"123456"`), []byte(`"role_id":"9"`), 1),
		"trailing data": append(append([]byte(nil), good...), []byte(` true`)...),
		"duplicate":     bytes.Replace(good, []byte(`"version":1`), []byte(`"version":1,"version":1`), 1),
		"invalid UTF-8": append(append([]byte(nil), good...), 0xff),
		"oversized":     bytes.Repeat([]byte(" "), MaxDocumentBytes+1),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			s, err := NewStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "123456.json")
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Load(context.Background(), "123456"); !errors.Is(err, ErrCorrupt) {
				t.Fatal("corrupt load", err)
			}
			if err := s.Save(context.Background(), fixtureArchive()); !errors.Is(err, ErrCorrupt) {
				t.Fatal("corrupt save", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(data, after) {
				t.Fatal("corrupt bytes changed", err)
			}
		})
	}
}

func TestStoreRejectsSymlinkAncestors(t *testing.T) {
	real := t.TempDir()
	parent := t.TempDir()
	link := filepath.Join(parent, "linked")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("Windows symlink privilege unavailable: %v", err)
	}
	s, err := NewStore(filepath.Join(link, "gacha"))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Save(context.Background(), fixtureArchive()); err == nil {
		t.Fatal("followed symlink ancestor")
	}
	if _, err := os.Stat(filepath.Join(real, "gacha")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created directory behind symlink", err)
	}
}

func TestStrictNestedSchemasAndExactLimit(t *testing.T) {
	a, _, err := Merge(Archive{}, fixtureSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	good, err := ExportOddward(a, fixtureTime())
	if err != nil {
		t.Fatal(err)
	}
	bad := [][]byte{
		bytes.Replace(good, []byte(`"m":90`), []byte(`"m":90,"unexpected":0`), 1),
		bytes.Replace(good, []byte(`"m":90`), []byte(`"m":90,"m":90`), 1),
		bytes.Replace(good, []byte(`"version":1`), []byte(`"VERSION":1`), 1),
		bytes.Replace(good, []byte(`"version":1`), []byte(`"version":null`), 1),
		bytes.Replace(good, []byte(`"luck":2`), []byte(`"luck":null`), 1),
		bytes.Replace(good, []byte(`"luck_title":`), []byte(`"Luck_Title":`), 1),
	}
	for i, data := range bad {
		if bytes.Equal(data, good) {
			t.Fatalf("bad fixture %d did not mutate", i)
		}
		if _, err := DecodeImport(data); err == nil {
			t.Fatalf("accepted bad nested schema %d", i)
		}
	}
	if _, err := DecodeImport([]byte(`{"format":"other"}`)); !errors.Is(err, ErrUnknownFormat) {
		t.Fatal("format error must be identifiable", err)
	}
	atLimit := make([]byte, MaxDocumentBytes)
	copy(atLimit, good)
	for i := len(good); i < len(atLimit); i++ {
		atLimit[i] = ' '
	}
	if _, err := DecodeImport(atLimit); err != nil {
		t.Fatal("rejected exact size limit", err)
	}
	if _, err := DecodeImport([]byte(`{"format":"nteuid-gacha","version":1,"uid":"123456","source":"tajiduo","updated_at":0,"pools":{}}`)); err != nil {
		t.Fatal("pools may all be absent", err)
	}
}

func TestExportSizeAndPityRepresentability(t *testing.T) {
	a := fixtureArchive()
	a.Records[0].Pity = 0
	full, err := ExportOddward(a, fixtureTime())
	if err != nil {
		t.Fatal("full schema must retain anomalies", err)
	}
	imp, err := DecodeImport(full)
	if err != nil || imp.Archive.Records[0].Pity != 0 {
		t.Fatal("lost anomaly", err)
	}
	if _, err := ExportNTEUID(a, fixtureTime()); err == nil {
		t.Fatal("NTEUID exported unrepresentable pity")
	}
	s := fixtureSnapshot()
	s.LuckTitle = strings.Repeat("a", MaxDocumentBytes)
	a, _, err = Merge(Archive{}, s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ExportOddward(a, fixtureTime()); err == nil {
		t.Fatal("export exceeded maximum")
	}
	dir := filepath.Join(t.TempDir(), "not-created")
	store, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(context.Background(), a); err == nil {
		t.Fatal("saved oversized archive")
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("invalid save created directory", err)
	}
}

func TestMergeDuplicateKeysAndIndependentFields(t *testing.T) {
	a := fixtureArchive()
	a.Records = a.Records[:1]
	s := fixtureSnapshot()
	s.Pools = s.Pools[:1]
	original := s.Pools[0].Records[0]
	unknown := original
	unknown.Luck = LuckUnknown
	conflict := original
	conflict.Luck = 3
	later := original
	later.At = later.At.Add(time.Millisecond)
	otherItem := original
	otherItem.ItemID = "other"
	otherPity := original
	otherPity.Pity++
	s.Pools[0].Records = []Record{conflict, otherPity, unknown, later, original, otherItem}
	first, report, err := Merge(a, s)
	if err != nil || report.Added != 3 || report.Conflicts != 1 || len(first.Records) != 4 {
		t.Fatal("distinct merge keys", report, err)
	}
	for l, r := 0, len(s.Pools[0].Records)-1; l < r; l, r = l+1, r-1 {
		s.Pools[0].Records[l], s.Pools[0].Records[r] = s.Pools[0].Records[r], s.Pools[0].Records[l]
	}
	second, report2, err := Merge(a, s)
	if err != nil || !reflect.DeepEqual(first, second) || report != report2 {
		t.Fatal("duplicate input order changed merge", report2, err)
	}
	if a.Records[0].Luck != 2 || original.Luck != 2 {
		t.Fatal("input mutated")
	}
	empty := Statistics(Archive{})
	if empty.Pools[Limited].AveragePity != 0 || empty.Pools[Limited].MinPity != 0 || empty.Tajiduo != nil {
		t.Fatal("invented empty statistics")
	}
}
