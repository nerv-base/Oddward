// SPDX-License-Identifier: GPL-3.0-or-later

package launchsettings_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nerv-base/Oddward/internal/launchsettings"
)

func TestLegacyUIChoicesDefaultOnAndImport(t *testing.T) {
	ctx := context.Background()
	store, db, file := newStore(t)
	prefix := `,"gameRoot":` + quoteGameRoot(t, filepath.Join(t.TempDir(), "game")) + `,"riskVersion":"","skippedVersion":"","network":{},"downloads":{"rateLimitKiB":0}`
	importDocument := func(document string) {
		t.Helper()
		writeLegacy(t, file, []byte(document))
		if ran := importLegacy(t, db, store); !ran {
			t.Fatalf("import of %s did not run", document)
		}
	}
	// A v4 document has no ui block at all, so every switch keeps its default.
	importDocument(`{"version":4` + prefix + `}`)
	if got := mustLoad(t, store).UI; got != launchsettings.DefaultUI() {
		t.Fatalf("v4 ui = %+v, want the launcher defaults", got)
	}
	// v5 knew two switches and defaulted the update check on.
	importDocument(`{"version":5` + prefix + `,"ui":{"closeToTray":false,"accentFollow":false}}`)
	if got := mustLoad(t, store).UI; got != (launchsettings.UI{CloseToTray: false, AccentFollow: false, CheckUpdates: true}) {
		t.Fatalf("v5 ui = %+v, want the update check defaulted on", got)
	}
	// A switch that the user turned off must stay off, and the mirror must be a
	// version 6 document that a rolled-back build can read.
	want := launchsettings.UI{CloseToTray: false, AccentFollow: false, CheckUpdates: false}
	if err := store.Update(ctx, func(s *launchsettings.Settings) { s.UI = want }); err != nil {
		t.Fatal(err)
	}
	if got := mustLoad(t, store).UI; got != want {
		t.Fatalf("ui choices = %+v, want %+v", got, want)
	}
	raw := string(readFile(t, file))
	if !strings.Contains(raw, `"version":6`) || !strings.Contains(raw, `"closeToTray":false`) ||
		!strings.Contains(raw, `"accentFollow":false`) || !strings.Contains(raw, `"checkUpdates":false`) {
		t.Fatalf("mirror must carry the version 6 ui choices: %s", raw)
	}
}

func TestMalformedUIBlocksAreRejected(t *testing.T) {
	ctx := context.Background()
	store, db, file := newStore(t)
	prefix := `,"gameRoot":` + quoteGameRoot(t, filepath.Join(t.TempDir(), "game")) + `,"riskVersion":"","skippedVersion":"","network":{},"downloads":{"rateLimitKiB":0}`
	writeLegacy(t, file, []byte(`{"version":6`+prefix+`,"ui":{"closeToTray":true,"accentFollow":true,"checkUpdates":true}}`))
	if ran := importLegacy(t, db, store); !ran {
		t.Fatal("the first import did not run")
	}
	previous := mustLoad(t, store)
	for _, block := range []string{
		`,"ui":null`, `,"ui":{}`, `,"ui":{"closeToTray":true,"accentFollow":true}`,
		`,"ui":{"closeToTray":true,"accentFollow":true,"checkUpdates":true,"unknown":true}`,
		`,"ui":{"closeToTray":true,"closeToTray":false,"accentFollow":true,"checkUpdates":true}`,
	} {
		document := []byte(`{"version":6` + prefix + block + `}`)
		writeLegacy(t, file, document)
		if _, err := db.RunImport(ctx, launchsettings.Domain, launchsettings.ImportVersion, store.ImportLegacy); err == nil {
			t.Errorf("accepted malformed ui block %s", block)
		}
		requireFile(t, file, document)
		requireSettings(t, previous, mustLoad(t, store))
	}
}
