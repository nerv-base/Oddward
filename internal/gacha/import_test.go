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

	"github.com/nerv-base/Oddward/internal/database"
)

func TestImportLegacyReadsEveryArchiveExactly(t *testing.T) {
	ctx := context.Background()
	s, db, dir := newTestStore(t)
	corpus := legacyCorpus()
	files := writeLegacyCorpus(t, dir, corpus)

	if err := s.ImportLegacy(ctx); err != nil {
		t.Fatalf("ImportLegacy: %v", err)
	}

	ids, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(ids) != len(corpus) {
		t.Fatalf("List = %v, want %d roles", ids, len(corpus))
	}
	for i, want := range corpus {
		if ids[i] != want.RoleID {
			t.Fatalf("List[%d] = %s, want %s", i, ids[i], want.RoleID)
		}
		canonical, err := normalizeArchive(want, false)
		if err != nil {
			t.Fatalf("normalize %s: %v", want.RoleID, err)
		}
		got, err := s.Load(ctx, want.RoleID)
		if err != nil {
			t.Fatalf("Load %s: %v", want.RoleID, err)
		}
		if !reflect.DeepEqual(got, canonical) {
			t.Fatalf("Load %s = %+v, want %+v", want.RoleID, got, canonical)
		}
		// The identity, order, luck, snapshot and per-pool fields are compared
		// field by field as well, so a lossy column cannot hide behind an equal
		// count.
		if got.RoleID != canonical.RoleID || len(got.Records) != len(canonical.Records) {
			t.Fatalf("%s: role %q with %d records, want %q with %d", want.RoleID, got.RoleID, len(got.Records), canonical.RoleID, len(canonical.Records))
		}
		for j, record := range got.Records {
			if record != canonical.Records[j] {
				t.Fatalf("%s record %d = %+v, want %+v", want.RoleID, j, record, canonical.Records[j])
			}
		}
		if len(got.Snapshots) != len(canonical.Snapshots) {
			t.Fatalf("%s has %d snapshots, want %d", want.RoleID, len(got.Snapshots), len(canonical.Snapshots))
		}
		for j, meta := range got.Snapshots {
			wantMeta := canonical.Snapshots[j]
			if !meta.FetchedAt.Equal(wantMeta.FetchedAt) || meta.LuckType != wantMeta.LuckType || meta.LuckTitle != wantMeta.LuckTitle {
				t.Fatalf("%s snapshot %d = %+v, want %+v", want.RoleID, j, meta, wantMeta)
			}
			if len(meta.Pools) != len(wantMeta.Pools) {
				t.Fatalf("%s snapshot %d has %d pools, want %d", want.RoleID, j, len(meta.Pools), len(wantMeta.Pools))
			}
			for k, pool := range meta.Pools {
				if pool != wantMeta.Pools[k] {
					t.Fatalf("%s snapshot %d pool %d = %+v, want %+v", want.RoleID, j, k, pool, wantMeta.Pools[k])
				}
			}
		}
		var source string
		if err := db.QueryRow(ctx, `SELECT source FROM gacha_archives WHERE role_id = ?`, want.RoleID).Scan(&source); err != nil {
			t.Fatalf("source %s: %v", want.RoleID, err)
		}
		if source != sourceLegacy {
			t.Fatalf("%s source = %q, want %q", want.RoleID, source, sourceLegacy)
		}
		path := filepath.Join(dir, want.RoleID+".json")
		var digest string
		if err := db.QueryRow(ctx, `SELECT digest FROM compat_mirrors WHERE domain = ?`, archiveDomain(want.RoleID)).Scan(&digest); err != nil {
			t.Fatalf("mirror %s: %v", want.RoleID, err)
		}
		if wantDigest := database.Digest(files[path]); digest != wantDigest {
			t.Fatalf("%s mirror digest = %q, want %q", want.RoleID, digest, wantDigest)
		}
	}
	state, found, err := db.MigrationState(ctx, Domain)
	if err != nil || !found {
		t.Fatalf("MigrationState = %+v, found %v, %v", state, found, err)
	}
	if state.Status != database.MigrationDone || state.Version != ImportVersion {
		t.Fatalf("migration = %+v, want done version %d", state, ImportVersion)
	}
}

func TestImportLegacyIsIdempotent(t *testing.T) {
	ctx := context.Background()
	s, db, dir := newTestStore(t)
	corpus := legacyCorpus()
	writeLegacyCorpus(t, dir, corpus)

	ran, err := db.RunImport(ctx, Domain, ImportVersion, s.importArchives)
	if err != nil || !ran {
		t.Fatalf("first RunImport = %v, %v, want ran", ran, err)
	}
	first := stableDump(t, db)

	ran, err = db.RunImport(ctx, Domain, ImportVersion, s.importArchives)
	if err != nil || ran {
		t.Fatalf("second RunImport = %v, %v, want skipped", ran, err)
	}
	if again := stableDump(t, db); !reflect.DeepEqual(first, again) {
		t.Fatal("a skipped import changed the database")
	}

	// A direct repeat has to converge to the same content as well.
	if _, _, err := s.importArchives(ctx); err != nil {
		t.Fatalf("repeat import: %v", err)
	}
	if third := stableDump(t, db); !reflect.DeepEqual(first, third) {
		t.Fatalf("repeat import changed the database:\n%v\nwant\n%v", third, first)
	}

	var records, snapshots int
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM gacha_records`).Scan(&records); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(ctx, `SELECT COUNT(*) FROM gacha_snapshots`).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	wantRecords, wantSnapshots := 0, 0
	for _, a := range corpus {
		wantRecords += len(a.Records)
		wantSnapshots += len(a.Snapshots)
	}
	if records != wantRecords || snapshots != wantSnapshots {
		t.Fatalf("stored %d records and %d snapshots, want %d and %d", records, snapshots, wantRecords, wantSnapshots)
	}
}

func TestImportLegacyReconcilesChangedFiles(t *testing.T) {
	ctx := context.Background()
	s, _, dir := newTestStore(t)
	// The first start has no legacy directory yet.
	if err := s.ImportLegacy(ctx); err != nil {
		t.Fatalf("ImportLegacy before files: %v", err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the import created the archive directory: %v", err)
	}

	// A build that knows only files writes a new archive after the migration.
	adopted := legacyCorpus()[1]
	writeLegacyCorpus(t, dir, []Archive{adopted})
	if err := s.ImportLegacy(ctx); err != nil {
		t.Fatalf("ImportLegacy after a new file: %v", err)
	}
	want, err := normalizeArchive(adopted, false)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(ctx, adopted.RoleID)
	if err != nil {
		t.Fatalf("Load adopted: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("adopted archive = %+v, want %+v", got, want)
	}

	// An older build replaces the mirror of an existing role: the digest no
	// longer matches, so the next start reconciles the rows from the file.
	replaced := adopted
	replaced.Records = append(append([]Record(nil), adopted.Records...), Record{Pool: Permanent, ItemID: "11009", At: fixtureTime().Add(5 * time.Minute), Pity: 1, Luck: LuckUnknown})
	data, err := ExportOddward(replaced, fixtureTime().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, replaced.RoleID+".json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.ImportLegacy(ctx); err != nil {
		t.Fatalf("ImportLegacy after a changed file: %v", err)
	}
	want, err = normalizeArchive(replaced, false)
	if err != nil {
		t.Fatal(err)
	}
	got, err = s.Load(ctx, replaced.RoleID)
	if err != nil {
		t.Fatalf("Load replaced: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("reconciled archive = %+v, want %+v", got, want)
	}
	// The reconciled file is now the recorded mirror, so the next start is a
	// no-op instead of importing again.
	if changed, err := s.mirrorsChanged(ctx); err != nil || changed {
		t.Fatalf("mirrorsChanged after reconcile = %v, %v, want false", changed, err)
	}
}

func TestImportLegacyCorruptFileChangesNothing(t *testing.T) {
	ctx := context.Background()
	s, db, dir := newTestStore(t)
	if err := s.Save(ctx, fixtureArchive()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	goodPath := filepath.Join(dir, fixtureArchive().RoleID+".json")
	goodBytes := mustRead(t, goodPath)
	corruptPath := filepath.Join(dir, "200000009.json")
	corruptBytes := []byte("{not a valid archive")
	if err := os.WriteFile(corruptPath, corruptBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	err := s.ImportLegacy(ctx)
	if err == nil || !strings.Contains(err.Error(), corruptPath) {
		t.Fatalf("ImportLegacy = %v, want an error naming %s", err, corruptPath)
	}
	if !reflect.DeepEqual(mustRead(t, goodPath), goodBytes) {
		t.Fatal("a failed import changed the readable archive file")
	}
	if !reflect.DeepEqual(mustRead(t, corruptPath), corruptBytes) {
		t.Fatal("a failed import moved or rewrote the corrupt file")
	}
	ids, err := s.List(ctx)
	if err != nil || !reflect.DeepEqual(ids, []string{fixtureArchive().RoleID}) {
		t.Fatalf("List after a failed import = %v, %v", ids, err)
	}
	if _, err := s.Load(ctx, fixtureArchive().RoleID); err != nil {
		t.Fatalf("Load after a failed import: %v", err)
	}
	state, found, err := db.MigrationState(ctx, Domain)
	if err != nil || !found || state.Status != database.MigrationFailed {
		t.Fatalf("migration = %+v, found %v, %v, want failed", state, found, err)
	}
}

func TestImportLegacyWithoutFilesKeepsArchives(t *testing.T) {
	ctx := context.Background()
	s, _, dir := newTestStore(t)
	if err := s.Save(ctx, fixtureArchive()); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// The archive has its mirror; removing the whole directory must not be read
	// as a request to drop the archive.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := s.ImportLegacy(ctx); err != nil {
		t.Fatalf("ImportLegacy: %v", err)
	}
	if _, err := s.Load(ctx, fixtureArchive().RoleID); err != nil {
		t.Fatalf("Load after a missing directory: %v", err)
	}
}

// Deleting the only archive in the previous build leaves an empty directory
// behind. That is a real deletion, not a missing profile: keeping the row would
// resurrect an archive the user removed.
func TestImportLegacyHonoursDeletingTheLastArchive(t *testing.T) {
	ctx := context.Background()
	s, _, dir := newTestStore(t)
	archive := fixtureArchive()
	if err := s.Save(ctx, archive); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, archive.RoleID+".json")); err != nil {
		t.Fatal(err)
	}
	if err := s.ImportLegacy(ctx); err != nil {
		t.Fatalf("ImportLegacy: %v", err)
	}
	if _, err := s.Load(ctx, archive.RoleID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("archive survived its deletion: %v", err)
	}
	roles, err := s.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(roles) != 0 {
		t.Fatalf("roles after deleting the only archive: %v", roles)
	}
}

// stableDump returns every row that an idempotent import must not change,
// excluding the bookkeeping timestamps and autoincrement identifiers.
func stableDump(t *testing.T, db *database.DB) []string {
	t.Helper()
	ctx := context.Background()
	queries := []string{
		`SELECT role_id || '|' || source FROM gacha_archives ORDER BY role_id`,
		`SELECT role_id || '|' || pool || '|' || item_id || '|' || at_ms || '|' || pity || '|' || luck FROM gacha_records ORDER BY role_id, pool, item_id, at_ms, pity`,
		`SELECT role_id || '|' || meta_key || '|' || fetched_at || '|' || luck_type || '|' || luck_title FROM gacha_snapshots ORDER BY role_id, meta_key`,
		`SELECT s.role_id || '|' || s.meta_key || '|' || p.pool || '|' || p.m || '|' || p.draw_count || '|' || p.rare_count || '|' || p.average || '|' || p.player_over || '|' || p.rule_mismatch
		 FROM gacha_snapshot_pools p JOIN gacha_snapshots s ON s.id = p.snapshot_id ORDER BY s.role_id, s.meta_key, p.pool`,
		`SELECT domain || '|' || path || '|' || digest FROM compat_mirrors WHERE domain LIKE 'gacha/%' ORDER BY domain`,
	}
	out := []string{}
	for _, query := range queries {
		rows, err := db.Query(ctx, query)
		if err != nil {
			t.Fatalf("dump: %v", err)
		}
		for rows.Next() {
			var line string
			if err := rows.Scan(&line); err != nil {
				rows.Close()
				t.Fatalf("dump scan: %v", err)
			}
			out = append(out, line)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("dump rows: %v", err)
		}
		rows.Close()
	}
	return out
}
