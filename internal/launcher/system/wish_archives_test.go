// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/gacha"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// newTestWishArchives builds a WishArchives over a fresh absolute data
// directory and returns it together with the concrete gacha archive directory.
func newTestWishArchives(t *testing.T) (*WishArchives, string) {
	t.Helper()
	dataDir := t.TempDir()
	w, err := NewWishArchives(dataDir)
	if err != nil {
		t.Fatalf("NewWishArchives: %v", err)
	}
	return w, filepath.Join(dataDir, "gacha")
}

// sampleArchive builds a deterministic, valid archive. Records share the same
// pool, item and pity for the same index, so archives built with the same role
// and a shared prefix merge as exact duplicates.
func sampleArchive(roleID string, n int) gacha.Archive {
	base := time.UnixMilli(1700000000000).UTC()
	a := gacha.Archive{RoleID: roleID}
	for i := 0; i < n; i++ {
		a.Records = append(a.Records, gacha.Record{
			Pool:   gacha.Limited,
			ItemID: fmt.Sprintf("item-%02d", i),
			At:     base.Add(time.Duration(i) * time.Minute),
			Pity:   i%90 + 1,
			Luck:   gacha.LuckUnknown,
		})
	}
	return a
}

func mustWriteFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// exportImportFile writes an oddward-gacha document for archive into a fresh
// temp directory and returns its absolute path.
func exportImportFile(t *testing.T, a gacha.Archive) string {
	t.Helper()
	data, err := gacha.ExportOddward(a, time.UnixMilli(1700000000000).UTC())
	if err != nil {
		t.Fatalf("ExportOddward: %v", err)
	}
	return mustWriteFile(t, t.TempDir(), "import.json", data)
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func containsID(ids []string, want string) bool {
	for _, id := range ids {
		if id == want {
			return true
		}
	}
	return false
}

func TestNewWishArchivesRejectsRelativePath(t *testing.T) {
	if _, err := NewWishArchives("relative/data"); err == nil {
		t.Fatal("NewWishArchives accepted a relative data directory")
	}
}

func TestWishArchivesListStartsEmptyThenSaveLoad(t *testing.T) {
	ctx := context.Background()
	w, _ := newTestWishArchives(t)

	ids, err := w.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ids) != 0 {
		t.Fatalf("empty directory List = %v, want empty", ids)
	}

	archive := sampleArchive("100000001", 3)
	if err := w.store.Save(ctx, archive); err != nil {
		t.Fatalf("Save: %v", err)
	}

	ids, err = w.List(ctx)
	if err != nil {
		t.Fatalf("List after save: %v", err)
	}
	if len(ids) != 1 || ids[0] != "100000001" {
		t.Fatalf("List after save = %v, want [100000001]", ids)
	}

	got, err := w.Load(ctx, "100000001")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.RoleID != "100000001" || len(got.Records) != 3 {
		t.Fatalf("Load = role %q records %d, want role 100000001 records 3", got.RoleID, len(got.Records))
	}
}

func TestWishArchivesImportNewRoleNeedsConfirmationAndIsIdempotent(t *testing.T) {
	ctx := context.Background()
	w, gachaDir := newTestWishArchives(t)
	const role = "100000002"
	archivePath := exportImportFile(t, sampleArchive(role, 4))

	report, err := w.Import(ctx, archivePath, "", false)
	if err != nil {
		t.Fatalf("Import (unconfirmed): %v", err)
	}
	if !report.NeedsConfirmation {
		t.Fatal("unconfirmed new role must set NeedsConfirmation")
	}
	if report.Archive.RoleID != role || len(report.Archive.Records) != 4 {
		t.Fatalf("unconfirmed report archive = %+v, want role %s with 4 records", report.Archive, role)
	}
	if report.Report.Added != 4 {
		t.Fatalf("unconfirmed report Added = %d, want 4", report.Report.Added)
	}
	if _, err := os.Stat(filepath.Join(gachaDir, role+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unconfirmed import wrote a file: %v", err)
	}
	if ids, _ := w.List(ctx); len(ids) != 0 {
		t.Fatalf("unconfirmed import listed %v", ids)
	}

	report, err = w.Import(ctx, archivePath, role, true)
	if err != nil {
		t.Fatalf("Import (confirmed): %v", err)
	}
	if report.NeedsConfirmation {
		t.Fatal("confirmed import must not need confirmation")
	}
	if len(report.Archive.Records) != 4 {
		t.Fatalf("confirmed archive has %d records, want 4", len(report.Archive.Records))
	}
	if _, err := os.Stat(filepath.Join(gachaDir, role+".json")); err != nil {
		t.Fatalf("confirmed import did not write a file: %v", err)
	}
	if ids, _ := w.List(ctx); !containsID(ids, role) {
		t.Fatalf("List after confirmed import = %v, want %s", ids, role)
	}

	report, err = w.Import(ctx, archivePath, "", false)
	if err != nil {
		t.Fatalf("Import (idempotent repeat): %v", err)
	}
	if report.Report.Added != 0 {
		t.Fatalf("re-importing the same file Added = %d, want 0", report.Report.Added)
	}
}

func TestWishArchivesImportExistingMergesAddedCount(t *testing.T) {
	ctx := context.Background()
	w, _ := newTestWishArchives(t)
	const role = "100000003"
	if err := w.store.Save(ctx, sampleArchive(role, 3)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	archivePath := exportImportFile(t, sampleArchive(role, 5))

	report, err := w.Import(ctx, archivePath, "", false)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if report.NeedsConfirmation {
		t.Fatal("existing archive import must not need confirmation")
	}
	if report.Report.Added != 2 {
		t.Fatalf("Added = %d, want 2", report.Report.Added)
	}
	got, err := w.Load(ctx, role)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(got.Records) != 5 {
		t.Fatalf("merged archive has %d records, want 5", len(got.Records))
	}
}

func TestWishArchivesImportExistingTargetMismatchWritesNothing(t *testing.T) {
	ctx := context.Background()
	w, gachaDir := newTestWishArchives(t)
	const role = "100000004"
	if err := w.store.Save(ctx, sampleArchive(role, 2)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	target := filepath.Join(gachaDir, role+".json")
	before := mustReadFile(t, target)
	archivePath := exportImportFile(t, sampleArchive(role, 3))

	_, err := w.Import(ctx, archivePath, "999999999", false)
	if !errors.Is(err, gacha.ErrRoleMismatch) {
		t.Fatalf("Import target mismatch err = %v, want ErrRoleMismatch", err)
	}
	if after := mustReadFile(t, target); !bytes.Equal(before, after) {
		t.Fatal("target mismatch import rewrote the existing archive")
	}
}

func TestWishArchivesImportConfirmedRoleMismatch(t *testing.T) {
	ctx := context.Background()
	w, gachaDir := newTestWishArchives(t)
	const role = "100000005"
	archivePath := exportImportFile(t, sampleArchive(role, 2))

	for _, target := range []string{"", "100000006"} {
		_, err := w.Import(ctx, archivePath, target, true)
		if !errors.Is(err, gacha.ErrRoleMismatch) {
			t.Fatalf("confirmed import target %q err = %v, want ErrRoleMismatch", target, err)
		}
	}
	if _, err := os.Stat(filepath.Join(gachaDir, role+".json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("mismatched confirmed import wrote a file: %v", err)
	}
}

func TestWishArchivesLoadQuarantinesCorruptArchive(t *testing.T) {
	w, dir := newTestWishArchives(t)
	ctx := context.Background()
	mustWriteFile(t, dir, "100000001.json", []byte("{not a valid archive"))
	if _, err := w.Load(ctx, "100000001"); !errors.Is(err, gacha.ErrCorrupt) {
		t.Fatalf("Load corrupt = %v, want ErrCorrupt", err)
	}
	matches, err := filepath.Glob(filepath.Join(dir, "100000001.json.corrupt-*"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("corrupt archive was not quarantined: %v %v", matches, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "100000001.json")); !os.IsNotExist(err) {
		t.Fatal("quarantine must move the corrupt archive away")
	}
}

func TestWishArchivesImportCorruptExistingPreserved(t *testing.T) {
	ctx := context.Background()
	w, gachaDir := newTestWishArchives(t)
	const role = "100000007"
	garbage := []byte("this is not a gacha archive")
	mustWriteFile(t, gachaDir, role+".json", garbage)
	archivePath := exportImportFile(t, sampleArchive(role, 2))

	_, err := w.Import(ctx, archivePath, "", false)
	if !errors.Is(err, gacha.ErrCorrupt) {
		t.Fatalf("Import over corrupt archive err = %v, want ErrCorrupt", err)
	}
	if after := mustReadFile(t, filepath.Join(gachaDir, role+".json")); !bytes.Equal(after, garbage) {
		t.Fatal("corrupt archive was overwritten by import")
	}
}

func TestWishArchivesImportUnknownFormatAndOversize(t *testing.T) {
	ctx := context.Background()
	w, _ := newTestWishArchives(t)

	unknown := [][]byte{
		[]byte("not json at all"),
		[]byte(`{"format":"mystery"}`),
		[]byte(`{"foo":1}`),
		[]byte(`[]`),
	}
	for i, data := range unknown {
		path := mustWriteFile(t, t.TempDir(), fmt.Sprintf("bad-%d.json", i), data)
		if _, err := w.Import(ctx, path, "", false); !errors.Is(err, gacha.ErrUnknownFormat) {
			t.Fatalf("case %d err = %v, want ErrUnknownFormat", i, err)
		}
	}

	big := mustWriteFile(t, t.TempDir(), "big.json", make([]byte, gacha.MaxDocumentBytes+1))
	_, err := w.Import(ctx, big, "", false)
	if err == nil {
		t.Fatal("Import accepted a file larger than MaxDocumentBytes")
	}
	if errors.Is(err, gacha.ErrUnknownFormat) {
		t.Fatalf("oversize file was misclassified as unknown format: %v", err)
	}
}

func TestWishArchivesImportRejectsNonRegularFile(t *testing.T) {
	ctx := context.Background()
	w, _ := newTestWishArchives(t)
	if _, err := w.Import(ctx, t.TempDir(), "", false); err == nil {
		t.Fatal("Import accepted a directory as the import path")
	}
}

func TestWishArchivesExportBothFormatsAndOverwriteNoTemp(t *testing.T) {
	ctx := context.Background()
	w, _ := newTestWishArchives(t)
	const role = "100000008"
	if err := w.store.Save(ctx, sampleArchive(role, 4)); err != nil {
		t.Fatalf("Save: %v", err)
	}
	w.now = func() time.Time { return time.UnixMilli(1700000123000).UTC() }

	exportDir := t.TempDir()
	for _, tc := range []struct {
		name   string
		format state.WishFormat
	}{
		{"oddward", state.WishOddward},
		{"nteuid", state.WishNTEUID},
	} {
		path := filepath.Join(exportDir, tc.name+".json")
		if err := w.Export(ctx, path, role, tc.format); err != nil {
			t.Fatalf("%s Export: %v", tc.name, err)
		}
		imported, err := gacha.DecodeImport(mustReadFile(t, path))
		if err != nil {
			t.Fatalf("%s DecodeImport: %v", tc.name, err)
		}
		if imported.Archive.RoleID != role {
			t.Fatalf("%s round-trip role = %q, want %s", tc.name, imported.Archive.RoleID, role)
		}
		if len(imported.Archive.Records) != 4 {
			t.Fatalf("%s round-trip records = %d, want 4", tc.name, len(imported.Archive.Records))
		}
	}

	overwrite := filepath.Join(exportDir, "oddward.json")
	mustWriteFile(t, exportDir, "oddward.json", []byte("stale junk"))
	if err := w.Export(ctx, overwrite, role, state.WishOddward); err != nil {
		t.Fatalf("overwrite Export: %v", err)
	}
	if _, err := gacha.DecodeImport(mustReadFile(t, overwrite)); err != nil {
		t.Fatalf("overwrite DecodeImport: %v", err)
	}

	entries, err := os.ReadDir(exportDir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".oddward-export-") {
			t.Fatalf("export left a temporary file: %s", entry.Name())
		}
	}
}

func TestWishArchivesExportRejectsRelativeAndUnknownFormat(t *testing.T) {
	ctx := context.Background()
	w, _ := newTestWishArchives(t)
	const role = "100000009"
	if err := w.store.Save(ctx, sampleArchive(role, 2)); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := w.Export(ctx, "relative.json", role, state.WishOddward); err == nil {
		t.Fatal("Export accepted a relative path")
	}
	abs := filepath.Join(t.TempDir(), "out.json")
	if err := w.Export(ctx, abs, role, state.WishFormat(99)); err == nil {
		t.Fatal("Export accepted an unknown format")
	}
}

func TestWishArchivesDeleteRemovesAndMissingIsSuccess(t *testing.T) {
	ctx := context.Background()
	w, _ := newTestWishArchives(t)
	const role = "100000010"
	if err := w.store.Save(ctx, sampleArchive(role, 2)); err != nil {
		t.Fatalf("Save: %v", err)
	}

	if err := w.Delete(ctx, role); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if ids, _ := w.List(ctx); containsID(ids, role) {
		t.Fatalf("List still contains deleted role: %v", ids)
	}
	if err := w.Delete(ctx, role); err != nil {
		t.Fatalf("Delete missing role = %v, want nil", err)
	}
}

func TestWishArchivesCanceledContext(t *testing.T) {
	w, _ := newTestWishArchives(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := w.List(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("List err = %v, want context.Canceled", err)
	}
	if _, err := w.Load(ctx, "100000001"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Load err = %v, want context.Canceled", err)
	}
	if err := w.Delete(ctx, "100000001"); !errors.Is(err, context.Canceled) {
		t.Fatalf("Delete err = %v, want context.Canceled", err)
	}
	if _, err := w.Import(ctx, "unused.json", "", false); !errors.Is(err, context.Canceled) {
		t.Fatalf("Import err = %v, want context.Canceled", err)
	}
	if err := w.Export(ctx, filepath.Join(t.TempDir(), "x.json"), "100000001", state.WishOddward); !errors.Is(err, context.Canceled) {
		t.Fatalf("Export err = %v, want context.Canceled", err)
	}
}
