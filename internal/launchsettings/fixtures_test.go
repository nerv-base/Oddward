// SPDX-License-Identifier: GPL-3.0-or-later

package launchsettings_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nerv-base/Oddward/internal/database"
	"github.com/nerv-base/Oddward/internal/database/dbtest"
	"github.com/nerv-base/Oddward/internal/launchsettings"
)

// legacyFixture is one synthetic legacy document and the settings the file-based
// build produced from it.
type legacyFixture struct {
	version  int
	document string
	want     launchsettings.Settings
}

// legacyFixtures are one synthetic document per version the file-based build
// wrote. Document holds one %s for the quoted game root, so no fixture depends on
// what exists on the machine running the test. Between them the fixtures set every
// persisted field to a non-default value at least once.
var legacyFixtures = []legacyFixture{
	{
		version:  1,
		document: `{"version":1,"gameRoot":%s,"riskVersion":"standalone-login-v1"}`,
		want: launchsettings.Settings{
			RiskVersion: "standalone-login-v1",
			Downloads:   launchsettings.DefaultDownloads(),
			UI:          launchsettings.DefaultUI(),
		},
	},
	{
		version: 2,
		document: `{"version":2,"gameRoot":%s,"riskVersion":"consent-v2","skippedVersion":"0.1.0",` +
			`"network":{"proxy":"direct","proxyURL":"","mirror":true,"mirrorPrefix":"https://mirror.example/"}}`,
		want: launchsettings.Settings{
			RiskVersion:    "consent-v2",
			SkippedVersion: "0.1.0",
			Network:        launchsettings.Network{Proxy: "direct", Mirror: true, MirrorPrefix: "https://mirror.example/"},
			Downloads:      launchsettings.DefaultDownloads(),
			UI:             launchsettings.DefaultUI(),
		},
	},
	{
		version: 3,
		document: `{"version":3,"gameRoot":%s,"riskVersion":"consent-v3","skippedVersion":"0.1.1",` +
			`"network":{"proxy":"custom","proxyURL":"socks5://127.0.0.1:7891","mirror":false,"mirrorPrefix":""},` +
			`"downloads":{"filesInFlight":2,"connectionsPerFile":3,"rateLimitKiB":256,"cacheLimitGiB":40,"autoResume":false,"patchApply":true}}`,
		want: launchsettings.Settings{
			RiskVersion:    "consent-v3",
			SkippedVersion: "0.1.1",
			Network:        launchsettings.Network{Proxy: "custom", ProxyURL: "socks5://127.0.0.1:7891"},
			Downloads:      launchsettings.Downloads{RateLimitKiB: 256},
			UI:             launchsettings.DefaultUI(),
		},
	},
	{
		version: 4,
		document: `{"version":4,"gameRoot":%s,"riskVersion":"consent-v4","skippedVersion":"0.2.0",` +
			`"network":{"proxy":"system","proxyURL":"","mirror":true,"mirrorPrefix":"https://gh-proxy.com/"},` +
			`"downloads":{"rateLimitKiB":512}}`,
		want: launchsettings.Settings{
			RiskVersion:    "consent-v4",
			SkippedVersion: "0.2.0",
			Network:        launchsettings.Network{Proxy: "system", Mirror: true, MirrorPrefix: "https://gh-proxy.com/"},
			Downloads:      launchsettings.Downloads{RateLimitKiB: 512},
			UI:             launchsettings.DefaultUI(),
		},
	},
	{
		version: 5,
		document: `{"version":5,"gameRoot":%s,"riskVersion":"consent-v5","skippedVersion":"0.2.1",` +
			`"network":{"proxy":"","proxyURL":"","mirror":false,"mirrorPrefix":""},` +
			`"downloads":{"rateLimitKiB":0},"ui":{"closeToTray":false,"accentFollow":true}}`,
		want: launchsettings.Settings{
			RiskVersion:    "consent-v5",
			SkippedVersion: "0.2.1",
			Downloads:      launchsettings.DefaultDownloads(),
			UI:             launchsettings.UI{CloseToTray: false, AccentFollow: true, CheckUpdates: true},
		},
	},
	{
		version: 6,
		document: `{"version":6,"gameRoot":%s,"riskVersion":"consent-v6","skippedVersion":"0.2.2",` +
			`"network":{"proxy":"custom","proxyURL":"http://127.0.0.1:1080","mirror":false,"mirrorPrefix":""},` +
			`"downloads":{"rateLimitKiB":1048576},"ui":{"closeToTray":true,"accentFollow":false,"checkUpdates":false}}`,
		want: launchsettings.Settings{
			RiskVersion:    "consent-v6",
			SkippedVersion: "0.2.2",
			Network:        launchsettings.Network{Proxy: "custom", ProxyURL: "http://127.0.0.1:1080"},
			Downloads:      launchsettings.Downloads{RateLimitKiB: 1048576},
			UI:             launchsettings.UI{CloseToTray: true, AccentFollow: false, CheckUpdates: false},
		},
	},
}

// fixtureDocument returns the synthetic document for one version with the given
// game root.
func fixtureDocument(t *testing.T, version int, gameRoot string) []byte {
	t.Helper()
	return []byte(fmt.Sprintf(fixtureEntry(t, version).document, quoteGameRoot(t, gameRoot)))
}

// fixtureSettings returns the settings the file build produced for one version.
func fixtureSettings(t *testing.T, version int, gameRoot string) launchsettings.Settings {
	t.Helper()
	want := fixtureEntry(t, version).want
	want.GameRoot = gameRoot
	return want
}

func fixtureEntry(t *testing.T, version int) legacyFixture {
	t.Helper()
	for _, fixture := range legacyFixtures {
		if fixture.version == version {
			return fixture
		}
	}
	t.Fatalf("no legacy fixture for document version %d", version)
	return legacyFixtures[0]
}

// newStoreFor opens a store on a fresh migrated database with the given mirror
// path.
func newStoreFor(t *testing.T, file string) (*launchsettings.Store, *database.DB) {
	t.Helper()
	db := dbtest.Open(t)
	store, err := launchsettings.NewStore(db, file)
	if err != nil {
		t.Fatalf("open launch settings store: %v", err)
	}
	return store, db
}

// newStore keeps the mirror in its own directory, so mirror and directory
// assertions never see the database files.
func newStore(t *testing.T) (*launchsettings.Store, *database.DB, string) {
	t.Helper()
	file := filepath.Join(t.TempDir(), "launcher-v1.json")
	store, db := newStoreFor(t, file)
	return store, db, file
}

// freshProfile is the settings a database without a row yet stands for.
func freshProfile() launchsettings.Settings {
	return launchsettings.Settings{Downloads: launchsettings.DefaultDownloads(), UI: launchsettings.DefaultUI()}
}

// quoteGameRoot renders the game root the way a legacy document contains it.
func quoteGameRoot(t *testing.T, root string) string {
	t.Helper()
	quoted, err := json.Marshal(root)
	if err != nil {
		t.Fatalf("quote synthetic game root: %v", err)
	}
	return string(quoted)
}

func writeLegacy(t *testing.T, file string, document []byte) {
	t.Helper()
	if err := os.WriteFile(file, document, 0600); err != nil {
		t.Fatalf("write legacy fixture: %v", err)
	}
}

// importLegacy runs the importer the way startup does and fails the test on any
// error. Callers that expect a failure use db.RunImport directly.
func importLegacy(t *testing.T, db *database.DB, store *launchsettings.Store) bool {
	t.Helper()
	ran, err := db.RunImport(context.Background(), launchsettings.Domain, launchsettings.ImportVersion, store.ImportLegacy)
	if err != nil {
		t.Fatalf("import legacy launch settings: %v", err)
	}
	return ran
}

func mustLoad(t *testing.T, store *launchsettings.Store) launchsettings.Settings {
	t.Helper()
	got, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("load launch settings: %v", err)
	}
	return got
}

func readFile(t *testing.T, file string) []byte {
	t.Helper()
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	return data
}

// requireFile asserts a file still holds exactly these bytes.
func requireFile(t *testing.T, file string, want []byte) {
	t.Helper()
	if got := readFile(t, file); !bytes.Equal(got, want) {
		t.Fatalf("%s changed:\n got %q\nwant %q", file, got, want)
	}
}

// settingsDiff names every field that differs, so a migration parity failure says
// which field was lost instead of printing two structs.
func settingsDiff(want, got launchsettings.Settings) []string {
	var diff []string
	check := func(name string, want, got any) {
		if want != got {
			diff = append(diff, fmt.Sprintf("%s = %v, want %v", name, got, want))
		}
	}
	check("GameRoot", want.GameRoot, got.GameRoot)
	check("RiskVersion", want.RiskVersion, got.RiskVersion)
	check("SkippedVersion", want.SkippedVersion, got.SkippedVersion)
	check("Network.Proxy", want.Network.Proxy, got.Network.Proxy)
	check("Network.ProxyURL", want.Network.ProxyURL, got.Network.ProxyURL)
	check("Network.Mirror", want.Network.Mirror, got.Network.Mirror)
	check("Network.MirrorPrefix", want.Network.MirrorPrefix, got.Network.MirrorPrefix)
	check("Downloads.RateLimitKiB", want.Downloads.RateLimitKiB, got.Downloads.RateLimitKiB)
	check("UI.CloseToTray", want.UI.CloseToTray, got.UI.CloseToTray)
	check("UI.AccentFollow", want.UI.AccentFollow, got.UI.AccentFollow)
	check("UI.CheckUpdates", want.UI.CheckUpdates, got.UI.CheckUpdates)
	return diff
}

func requireSettings(t *testing.T, want, got launchsettings.Settings) {
	t.Helper()
	if diff := settingsDiff(want, got); len(diff) != 0 {
		t.Fatalf("settings differ:\n  %s", strings.Join(diff, "\n  "))
	}
}

// settingsRows counts the settings rows; anything but one means duplicates.
func settingsRows(t *testing.T, db *database.DB) int {
	t.Helper()
	var count int
	if err := db.QueryRow(context.Background(), `SELECT COUNT(*) FROM launch_settings`).Scan(&count); err != nil {
		t.Fatalf("count launch settings rows: %v", err)
	}
	return count
}

// sourceVersion reads the recorded legacy document version, which the API
// deliberately keeps internal.
func sourceVersion(t *testing.T, db *database.DB) int {
	t.Helper()
	var version int
	if err := db.QueryRow(context.Background(), `SELECT source_version FROM launch_settings WHERE id = 1`).Scan(&version); err != nil {
		t.Fatalf("read source_version: %v", err)
	}
	return version
}

// insertedGameRoot reads game_root straight from the row, bypassing validation.
func insertedGameRoot(t *testing.T, db *database.DB) string {
	t.Helper()
	var root string
	if err := db.QueryRow(context.Background(), `SELECT game_root FROM launch_settings WHERE id = 1`).Scan(&root); err != nil {
		t.Fatalf("read game_root: %v", err)
	}
	return root
}

// insertDamagedRow writes a row the schema accepts but validation rejects, the way
// a foreign or buggy build could have left one.
func insertDamagedRow(t *testing.T, db *database.DB, gameRoot string) {
	t.Helper()
	_, err := db.Exec(context.Background(), `
		INSERT INTO launch_settings(
			id, source_version, game_root, risk_version, skipped_version, proxy,
			proxy_url, mirror, mirror_prefix, rate_limit_kib,
			ui_close_to_tray, ui_accent_follow, ui_check_updates, updated_at)
		VALUES(1, 6, ?, '', '', '', '', 0, '', 0, 1, 1, 1, 0)`, gameRoot)
	if err != nil {
		t.Fatalf("insert damaged launch settings row: %v", err)
	}
}

func mirrorStale(t *testing.T, db *database.DB) bool {
	t.Helper()
	stale, err := db.MirrorStale(context.Background(), launchsettings.Domain)
	if err != nil {
		t.Fatalf("read mirror state: %v", err)
	}
	return stale
}
