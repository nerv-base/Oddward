// SPDX-License-Identifier: GPL-3.0-or-later

package gacha

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/database"
)

func catalogFixture() Catalog {
	return Catalog{FetchedAt: time.Date(2026, 11, 16, 1, 2, 3, 456000000, time.UTC), Items: map[string]Item{
		"11001":  {Kind: Character, Name: "合成角色甲"},
		"fork_a": {Kind: Arc, Name: "合成弧盘"},
	}}
}

func TestCatalogRoundTrip(t *testing.T) {
	ctx := context.Background()
	s, _, dir := newTestStore(t)
	empty, err := s.LoadCatalog(ctx)
	if err != nil || len(empty.Items) != 0 || !empty.FetchedAt.IsZero() {
		t.Fatalf("empty catalog = %+v, %v", empty, err)
	}
	want := catalogFixture()
	if err = s.SaveCatalog(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !got.FetchedAt.Equal(want.FetchedAt) {
		t.Fatalf("catalog time = %v, want %v", got.FetchedAt, want.FetchedAt)
	}
	if !reflect.DeepEqual(got.Items, want.Items) {
		t.Fatalf("catalog items = %+v, want %+v", got.Items, want.Items)
	}

	// The legacy file is the mirror a rolled-back build reads.
	mirror, err := decodeCatalog(mustRead(t, filepath.Join(dir, catalogFile)))
	if err != nil {
		t.Fatalf("mirror decode: %v", err)
	}
	if !mirror.FetchedAt.Equal(want.FetchedAt) || !reflect.DeepEqual(mirror.Items, want.Items) {
		t.Fatalf("mirror = %+v, want %+v", mirror, want)
	}
	stages, err := filepath.Glob(filepath.Join(dir, ".mirror-*.tmp"))
	if err != nil || len(stages) != 0 {
		t.Fatalf("catalog left a stage: %v %v", stages, err)
	}
}

func TestCatalogKeepsOnlyRecognizedNames(t *testing.T) {
	ctx := context.Background()
	s, _, _ := newTestStore(t)
	c := catalogFixture()
	c.Items["11002"] = Item{Kind: Character, Name: " 空格 "}
	if err := s.SaveCatalog(ctx, c); err == nil {
		t.Fatal("saved a name with surrounding whitespace")
	}

	c = catalogFixture()
	c.Items["mystery"] = Item{Kind: Unknown, Name: "未识别"}
	c.Items["11003"] = Item{Kind: Character, Name: ""}
	c.Items[strings.Repeat("1", catalogMaxID+1)] = Item{Kind: Character, Name: "超长标识"}
	if err := s.SaveCatalog(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("catalog kept %d items, want 2: %+v", len(got.Items), got.Items)
	}
	if got.Items["11001"].Kind != Character || got.Items["fork_a"].Kind != Arc {
		t.Fatalf("catalog kinds = %+v", got.Items)
	}
}

func TestCatalogLookupPrefersOfficialNames(t *testing.T) {
	c := catalogFixture()
	if got, ok := c.Lookup("11001"); !ok || got.Name != "合成角色甲" || got.Kind != Character {
		t.Fatalf("official character = %+v, %v", got, ok)
	}
	if got, ok := c.Lookup("fork_a"); !ok || got.Name != "合成弧盘" || got.Kind != Arc {
		t.Fatalf("official arc = %+v, %v", got, ok)
	}
	if _, ok := c.Lookup("99999"); ok {
		t.Fatal("looked up a missing identifier")
	}
}

func TestCatalogImportReadsTheLegacyFile(t *testing.T) {
	ctx := context.Background()
	s, db, dir := newTestStore(t)
	want := catalogFixture()
	items, err := catalogItems(want)
	if err != nil {
		t.Fatal(err)
	}
	data, err := encodeCatalog(want.FetchedAt, items)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, catalogFile)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	if err := s.ImportLegacy(ctx); err != nil {
		t.Fatalf("ImportLegacy: %v", err)
	}
	got, err := s.LoadCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !got.FetchedAt.Equal(want.FetchedAt) || !reflect.DeepEqual(got.Items, want.Items) {
		t.Fatalf("imported catalog = %+v, want %+v", got, want)
	}
	if after := mustRead(t, path); !reflect.DeepEqual(after, data) {
		t.Fatal("the catalog import rewrote the legacy file")
	}
	var digest string
	if err := db.QueryRow(ctx, `SELECT digest FROM compat_mirrors WHERE domain = ?`, catalogDomain).Scan(&digest); err != nil {
		t.Fatalf("catalog mirror: %v", err)
	}
	if wantDigest := database.Digest(data); digest != wantDigest {
		t.Fatalf("catalog digest = %q, want %q", digest, wantDigest)
	}
	ran, err := db.RunImport(ctx, catalogDomain, catalogImportVersion, s.importCatalog)
	if err != nil || ran {
		t.Fatalf("second catalog import = %v, %v, want skipped", ran, err)
	}
}

func TestCatalogImportRejectsCorruptFile(t *testing.T) {
	ctx := context.Background()
	s, _, dir := newTestStore(t)
	want := catalogFixture()
	if err := s.SaveCatalog(ctx, want); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, catalogFile)
	broken := []byte(`{"format":"oddward-gacha-catalog" broken`)
	if err := os.WriteFile(path, broken, 0o600); err != nil {
		t.Fatal(err)
	}

	err := s.ImportLegacy(ctx)
	if err == nil || !strings.Contains(err.Error(), path) {
		t.Fatalf("ImportLegacy = %v, want an error naming %s", err, path)
	}
	if after := mustRead(t, path); !reflect.DeepEqual(after, broken) {
		t.Fatal("a failed catalog import rewrote the file")
	}
	got, err := s.LoadCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !got.FetchedAt.Equal(want.FetchedAt) || !reflect.DeepEqual(got.Items, want.Items) {
		t.Fatalf("rows after a failed import = %+v, want %+v", got, want)
	}
}
