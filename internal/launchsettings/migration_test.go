// SPDX-License-Identifier: GPL-3.0-or-later

package launchsettings_test

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nerv-base/Oddward/internal/database"
	"github.com/nerv-base/Oddward/internal/launchsettings"
)

func TestLegacyDocumentsImportWithParity(t *testing.T) {
	for _, fixture := range legacyFixtures {
		t.Run(fmt.Sprintf("v%d", fixture.version), func(t *testing.T) {
			store, db, file := newStore(t)
			root := filepath.Join(t.TempDir(), "游戏")
			document := fixtureDocument(t, fixture.version, root)
			writeLegacy(t, file, document)
			if ran := importLegacy(t, db, store); !ran {
				t.Fatal("the first import did not run")
			}
			requireSettings(t, fixtureSettings(t, fixture.version, root), mustLoad(t, store))
			// An import only reads: the document stays for the rolled-back build that
			// may still have to run against it.
			requireFile(t, file, document)
			if count := settingsRows(t, db); count != 1 {
				t.Fatalf("launch_settings rows = %d, want 1", count)
			}
			if version := sourceVersion(t, db); version != fixture.version {
				t.Fatalf("source_version = %d, want the document version %d", version, fixture.version)
			}
			if mirrorStale(t, db) {
				t.Error("the import recorded a digest that does not match the file it read")
			}
			state, found, err := db.MigrationState(context.Background(), launchsettings.Domain)
			if err != nil {
				t.Fatal(err)
			}
			if !found || state.Status != database.MigrationDone || state.Source != file {
				t.Errorf("migration state = %+v, found %v; want done for %s", state, found, file)
			}
		})
	}
}

func TestImportIsIdempotent(t *testing.T) {
	store, db, file := newStore(t)
	root := filepath.Join(t.TempDir(), "game")
	writeLegacy(t, file, fixtureDocument(t, 6, root))
	if ran := importLegacy(t, db, store); !ran {
		t.Fatal("the first import did not run")
	}
	first := mustLoad(t, store)
	if ran := importLegacy(t, db, store); ran {
		t.Fatal("a completed import ran again although the mirror did not change")
	}
	// Running the importer directly, as a reconcile would, must converge to the
	// same row instead of adding or duplicating one.
	if _, detail, err := store.ImportLegacy(context.Background()); err != nil {
		t.Fatalf("repeat import: %v", err)
	} else if !strings.Contains(detail, "version 6") {
		t.Errorf("repeat import detail = %q, want the migrated document version", detail)
	}
	requireSettings(t, first, mustLoad(t, store))
	if count := settingsRows(t, db); count != 1 {
		t.Fatalf("launch_settings rows = %d after a repeat import, want 1", count)
	}
}

func TestRollbackReconcileImportsChangedLegacyFile(t *testing.T) {
	store, db, file := newStore(t)
	root := filepath.Join(t.TempDir(), "game")
	writeLegacy(t, file, fixtureDocument(t, 6, root))
	if ran := importLegacy(t, db, store); !ran {
		t.Fatal("the first import did not run")
	}
	// The stub rolled the launcher back: the older build accepted different choices
	// and wrote its own document over the mirror.
	other := filepath.Join(t.TempDir(), "other-game")
	writeLegacy(t, file, fixtureDocument(t, 6, other))
	if !mirrorStale(t, db) {
		t.Fatal("a mirror changed by another build must report stale")
	}
	if ran := importLegacy(t, db, store); !ran {
		t.Fatal("a changed mirror did not trigger a reconcile")
	}
	requireSettings(t, fixtureSettings(t, 6, other), mustLoad(t, store))
	if mirrorStale(t, db) {
		t.Error("the reconcile left the mirror stale")
	}
	if count := settingsRows(t, db); count != 1 {
		t.Fatalf("launch_settings rows = %d after a reconcile, want 1", count)
	}
}

func TestCorruptLegacyFileLeavesSettingsAndFileUntouched(t *testing.T) {
	store, db, file := newStore(t)
	writeLegacy(t, file, fixtureDocument(t, 6, filepath.Join(t.TempDir(), "game")))
	if ran := importLegacy(t, db, store); !ran {
		t.Fatal("the first import did not run")
	}
	previous := mustLoad(t, store)
	corrupt := []byte(`{"version":6,"gameRoot":`)
	writeLegacy(t, file, corrupt)
	_, err := db.RunImport(context.Background(), launchsettings.Domain, launchsettings.ImportVersion, store.ImportLegacy)
	if err == nil {
		t.Fatal("a corrupt legacy document imported")
	}
	if !strings.Contains(err.Error(), file) {
		t.Errorf("import error must name the file a rollback build changed: %v", err)
	}
	requireFile(t, file, corrupt)
	requireSettings(t, previous, mustLoad(t, store))
	if count := settingsRows(t, db); count != 1 {
		t.Fatalf("a failed import changed the row count to %d", count)
	}
	// The failure is recorded, so the next start retries instead of treating the
	// domain as migrated.
	state, found, err := db.MigrationState(context.Background(), launchsettings.Domain)
	if err != nil {
		t.Fatal(err)
	}
	if !found || state.Status != database.MigrationFailed {
		t.Errorf("failed import state = %+v, found %v", state, found)
	}
}

func TestCorruptLegacyFileOnEmptyDatabaseKeepsDefaults(t *testing.T) {
	store, db, file := newStore(t)
	writeLegacy(t, file, []byte(`not json`))
	if _, err := db.RunImport(context.Background(), launchsettings.Domain, launchsettings.ImportVersion, store.ImportLegacy); err == nil {
		t.Fatal("a corrupt legacy document imported")
	}
	requireFile(t, file, []byte(`not json`))
	requireSettings(t, freshProfile(), mustLoad(t, store))
	if count := settingsRows(t, db); count != 0 {
		t.Fatalf("a failed import wrote %d rows", count)
	}
}

func TestRejectedLegacyDocumentsLeaveFileAndRowsUntouched(t *testing.T) {
	cases := map[string]string{
		"malformed":            `{`,
		"not json":             `not json`,
		"unknown version":      `{"version":4,"gameRoot":"","riskVersion":""}`,
		"v1 with v2 field":     `{"version":1,"gameRoot":"","riskVersion":"","skippedVersion":""}`,
		"v2 missing network":   `{"version":2,"gameRoot":"","riskVersion":"","skippedVersion":""}`,
		"v2 network secret":    `{"version":2,"gameRoot":"","riskVersion":"","skippedVersion":"","network":{"proxy":"","proxyURL":"","mirror":false,"mirrorPrefix":"","password":"x"}}`,
		"v2 control chars":     "{\"version\":2,\"gameRoot\":\"\",\"riskVersion\":\"\",\"skippedVersion\":\"\\u0007\",\"network\":{\"proxy\":\"\",\"proxyURL\":\"\",\"mirror\":false,\"mirrorPrefix\":\"\"}}",
		"missing version":      `{"gameRoot":"","riskVersion":""}`,
		"unknown secret field": `{"version":1,"gameRoot":"","riskVersion":"","token":"not-a-real-token"}`,
		"duplicate field":      `{"version":1,"version":1,"gameRoot":"","riskVersion":""}`,
		"null document":        `null`,
		"null choice":          `{"version":1,"gameRoot":null,"riskVersion":""}`,
		"missing choice":       `{"version":1,"gameRoot":""}`,
		"wrong type":           `{"version":1,"gameRoot":12,"riskVersion":""}`,
		"trailing document":    `{"version":1,"gameRoot":"","riskVersion":""} {}`,
		"invalid game root":    `{"version":1,"gameRoot":"relative","riskVersion":""}`,
		"oversized":            strings.Repeat(" ", 16*1024+1),
	}
	for name, fixture := range cases {
		t.Run(name, func(t *testing.T) {
			store, db, file := newStore(t)
			writeLegacy(t, file, fixtureDocument(t, 6, filepath.Join(t.TempDir(), "game")))
			if ran := importLegacy(t, db, store); !ran {
				t.Fatal("the first import did not run")
			}
			previous := mustLoad(t, store)
			bad := []byte(fixture)
			writeLegacy(t, file, bad)
			_, err := db.RunImport(context.Background(), launchsettings.Domain, launchsettings.ImportVersion, store.ImportLegacy)
			if err == nil {
				t.Fatal("an invalid legacy document imported")
			}
			if !strings.Contains(err.Error(), file) {
				t.Errorf("import error must name the file: %v", err)
			}
			requireFile(t, file, bad)
			requireSettings(t, previous, mustLoad(t, store))
			if count := settingsRows(t, db); count != 1 {
				t.Fatalf("a rejected import changed the row count to %d", count)
			}
		})
	}
}

func TestMissingLegacyFileLeavesRowsAlone(t *testing.T) {
	store, db, file := newStore(t)
	root := filepath.Join(t.TempDir(), "game")
	writeLegacy(t, file, fixtureDocument(t, 6, root))
	if ran := importLegacy(t, db, store); !ran {
		t.Fatal("the first import did not run")
	}
	previous := mustLoad(t, store)
	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	// A missing file is not an instruction to delete the settings the user has.
	if _, detail, err := store.ImportLegacy(context.Background()); err != nil {
		t.Fatalf("import without a legacy file: %v", err)
	} else if detail == "" {
		t.Error("a skipped import should say why in the migration detail")
	}
	requireSettings(t, previous, mustLoad(t, store))
	if count := settingsRows(t, db); count != 1 {
		t.Fatalf("a skipped import changed the row count to %d", count)
	}
}

func TestAbsentLegacyFileImportsNothing(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "never-created")
	store, db := newStoreFor(t, filepath.Join(parent, "launcher-v1.json"))
	if ran := importLegacy(t, db, store); !ran {
		t.Fatal("an absent file should still let the import run and record completion")
	}
	requireSettings(t, freshProfile(), mustLoad(t, store))
	if count := settingsRows(t, db); count != 0 {
		t.Fatalf("an absent file wrote %d rows", count)
	}
	if _, err := os.Stat(parent); !os.IsNotExist(err) {
		t.Fatalf("an absent file must not create the mirror parent: %v", err)
	}
}

func TestExactLimitDocumentImports(t *testing.T) {
	store, db, file := newStore(t)
	payload := []byte(`{"version":1,"gameRoot":"","riskVersion":""}`)
	padded := bytes.Repeat([]byte(" "), 16*1024-len(payload))
	document := append(payload, padded...)
	writeLegacy(t, file, document)
	if ran := importLegacy(t, db, store); !ran {
		t.Fatal("the first import did not run")
	}
	requireSettings(t, freshProfile(), mustLoad(t, store))
	// One byte more is over the bound the file build wrote within.
	writeLegacy(t, file, append(document, ' '))
	if _, err := db.RunImport(context.Background(), launchsettings.Domain, launchsettings.ImportVersion, store.ImportLegacy); err == nil {
		t.Fatal("a document above the size limit imported")
	}
}
