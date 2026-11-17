// SPDX-License-Identifier: GPL-3.0-or-later

package gacha

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func catalogFixture() Catalog {
	return Catalog{
		FetchedAt: time.Date(2026, 11, 16, 1, 2, 3, 456000000, time.UTC),
		Items: map[string]Item{
			"11001":  {Kind: Character, Name: "合成角色甲"},
			"fork_a": {Kind: Arc, Name: "合成弧盘"},
		},
	}
}

func TestCatalogRoundTrip(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "gacha")
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	empty, err := s.LoadCatalog(ctx)
	if err != nil || len(empty.Items) != 0 || !empty.FetchedAt.IsZero() {
		t.Fatalf("missing catalog: %+v %v", empty, err)
	}
	want := catalogFixture()
	if err = s.SaveCatalog(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadCatalog(ctx)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("load %+v %v", got, err)
	}
	if files, _ := filepath.Glob(filepath.Join(dir, ".catalog-*.tmp")); len(files) != 0 {
		t.Fatal("stages left behind", files)
	}
}

func TestCatalogKeepsOnlyRecognizedNames(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	c := Catalog{FetchedAt: time.Date(2026, 11, 16, 1, 2, 3, 0, time.UTC), Items: map[string]Item{
		"11001":     {Name: "有名字"},
		"weird":     {Name: "形状不认识"},
		"11002":     {Name: "  首尾空白  "},
		"11003":     {Name: ""},
		"fork_a":    {Name: "弧盘"},
		"fork_bad!": {Name: "非法弧盘"},
	}}
	if err = s.SaveCatalog(ctx, c); err == nil {
		t.Fatal("a padded name must be rejected instead of silently trimmed")
	}
	delete(c.Items, "11002")
	if err = s.SaveCatalog(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadCatalog(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("unrecognized identifiers must be dropped: %+v", got.Items)
	}
	if item, ok := got.Items["11001"]; !ok || item.Kind != Character || item.Name != "有名字" {
		t.Fatalf("character name: %+v", got.Items)
	}
	if item, ok := got.Items["fork_a"]; !ok || item.Kind != Arc {
		t.Fatalf("arc name: %+v", got.Items)
	}
}

func TestCatalogLookupPrefersOfficialNames(t *testing.T) {
	c := Catalog{Items: map[string]Item{"11001": {Kind: Character, Name: "官方名"}}}
	if item, ok := c.Lookup("11001"); !ok || item.Name != "官方名" {
		t.Fatalf("official name: %+v %v", item, ok)
	}
	if _, ok := c.Lookup("not-an-id"); ok {
		t.Fatal("an unrecognized identifier must never resolve")
	}
	if _, ok := (Catalog{}).Lookup("11001"); ok {
		t.Fatal("an empty catalog and empty built-in table resolve nothing")
	}
}

func TestCatalogCorruptionIsReportedWithoutTouchingTheFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	name := filepath.Join(dir, catalogFile)
	if err = os.WriteFile(name, []byte("{not json"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.LoadCatalog(ctx); err == nil {
		t.Fatal("corrupt catalog must report an error")
	}
	if data, readErr := os.ReadFile(name); readErr != nil || !strings.Contains(string(data), "not json") {
		t.Fatalf("corrupt catalog must be left untouched: %q %v", data, readErr)
	}
	bad := []byte(`{"format":"oddward-gacha-catalog","version":9,"fetched_at":0,"items":{}}`)
	if err = os.WriteFile(name, bad, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.LoadCatalog(ctx); err == nil {
		t.Fatal("unknown schema must report an error")
	}
	if err = os.WriteFile(name, []byte(strings.Repeat("x", catalogMaxBytes+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = s.LoadCatalog(ctx); err == nil {
		t.Fatal("oversized catalog must report an error")
	}
	if _, err = s.LoadCatalog(context.Background()); !errors.Is(err, os.ErrNotExist) && err == nil {
		t.Fatal("unreachable")
	}
}
