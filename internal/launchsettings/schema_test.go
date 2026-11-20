// SPDX-License-Identifier: GPL-3.0-or-later

package launchsettings_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nerv-base/Oddward/internal/launchsettings"
)

func TestVersionOneDocumentLeavesLaterFieldsAtDefaults(t *testing.T) {
	store, db, file := newStore(t)
	writeLegacy(t, file, []byte(`{"version":1,"gameRoot":"","riskVersion":""}`))
	if ran := importLegacy(t, db, store); !ran {
		t.Fatal("the first import did not run")
	}
	// A v1 build knew only two fields; the download and UI preferences it never
	// wrote must come back as the profile defaults, not as zeros.
	requireSettings(t, freshProfile(), mustLoad(t, store))
}

func TestDownloadsMigrationAndValidation(t *testing.T) {
	ctx := context.Background()
	store, db, file := newStore(t)
	prefix := `,"gameRoot":` + quoteGameRoot(t, t.TempDir()) + `,"riskVersion":"","skippedVersion":"v2.0.0","network":{}`
	importDocument := func(document string) {
		t.Helper()
		writeLegacy(t, file, []byte(document))
		if ran := importLegacy(t, db, store); !ran {
			t.Fatalf("import of %s did not run", document)
		}
	}
	importDocument(`{"version":2` + prefix + `}`)
	if got := mustLoad(t, store); got.Downloads != launchsettings.DefaultDownloads() {
		t.Fatalf("v2 downloads = %+v, want defaults", got.Downloads)
	}
	// A v3 document still loads: the removed transfer knobs are dropped and the
	// user's rate limit survives the migration.
	importDocument(`{"version":3` + prefix + `,"downloads":{"filesInFlight":2,"connectionsPerFile":3,"rateLimitKiB":10,"cacheLimitGiB":40,"autoResume":false,"patchApply":false}}`)
	if got := mustLoad(t, store); got.Downloads.RateLimitKiB != 10 {
		t.Fatalf("v3 rate limit = %d, want 10", got.Downloads.RateLimitKiB)
	}
	if err := store.Update(ctx, func(s *launchsettings.Settings) { s.Downloads = launchsettings.Downloads{RateLimitKiB: 128} }); err != nil {
		t.Fatal(err)
	}
	if got := mustLoad(t, store); got.Downloads.RateLimitKiB != 128 || got.SkippedVersion != "v2.0.0" {
		t.Fatalf("v4 = %+v, want the new rate limit and the skipped version", got)
	}
	raw := string(readFile(t, file))
	if !strings.Contains(raw, `"version":6`) || strings.Contains(raw, "filesInFlight") {
		t.Fatalf("the mirror still carries removed transfer settings: %s", raw)
	}
	for _, suffix := range []string{
		`}`, `,"downloads":null}`, `,"downloads":{}}`,
		`,"downloads":{"rateLimitKiB":1048577}}`,
		`,"downloads":{"rateLimitKiB":0,"unknown":true}}`,
		`,"downloads":{"rateLimitKiB":0,"rateLimitKiB":1}}`,
	} {
		writeLegacy(t, file, []byte(`{"version":4`+prefix+suffix))
		if _, err := db.RunImport(ctx, launchsettings.Domain, launchsettings.ImportVersion, store.ImportLegacy); err == nil {
			t.Errorf("accepted %s", suffix)
		}
	}
	writeLegacy(t, file, []byte(`{"version":3`+prefix+`,"downloads":{"filesInFlight":9,"connectionsPerFile":1,"rateLimitKiB":0}}`))
	if _, err := db.RunImport(ctx, launchsettings.Domain, launchsettings.ImportVersion, store.ImportLegacy); err == nil {
		t.Error("accepted a v3 document with an out-of-range transfer count")
	}
}

func TestMirrorIsAReimportableVersionSixDocument(t *testing.T) {
	store, _, file := newStore(t)
	root := filepath.Join(t.TempDir(), "game")
	want := launchsettings.Settings{
		GameRoot: root, RiskVersion: "v1", SkippedVersion: "0.2.0",
		Network:   launchsettings.Network{Proxy: "direct"},
		Downloads: launchsettings.DefaultDownloads(),
		UI:        launchsettings.DefaultUI(),
	}
	if err := store.Save(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	// The mirror must be exactly the document a build that cannot read the database
	// knows how to load, and re-importing it must reproduce the settings unchanged.
	document := string(readFile(t, file))
	if !strings.Contains(document, `"version":6`) {
		t.Fatalf("mirror version is not 6: %s", document)
	}
	if _, detail, err := store.ImportLegacy(context.Background()); err != nil {
		t.Fatalf("a mirror written by this build must import unchanged: %v", err)
	} else if !strings.Contains(detail, "version 6") {
		t.Errorf("mirror import detail = %q, want the version it read", detail)
	}
	requireSettings(t, want, mustLoad(t, store))
}
