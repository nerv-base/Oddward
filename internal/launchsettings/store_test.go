// SPDX-License-Identifier: GPL-3.0-or-later

package launchsettings_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/nerv-base/Oddward/internal/database/dbtest"
	"github.com/nerv-base/Oddward/internal/launchsettings"
)

func TestSaveLoadNonSecretSettings(t *testing.T) {
	file := filepath.Join(t.TempDir(), "owned", "nested", "launcher-v1.json")
	store, db := newStoreFor(t, file)
	want := launchsettings.Settings{
		GameRoot:       filepath.Join(t.TempDir(), "游戏"),
		RiskVersion:    "standalone-login-v1",
		SkippedVersion: "0.2.0",
		Network:        launchsettings.Network{Proxy: "custom", ProxyURL: "socks5://127.0.0.1:7891", Mirror: true, MirrorPrefix: "https://gh-proxy.com/"},
		Downloads:      launchsettings.Downloads{RateLimitKiB: 256},
		UI:             launchsettings.UI{CloseToTray: false, AccentFollow: true, CheckUpdates: false},
	}
	if err := store.Save(context.Background(), want); err != nil {
		t.Fatalf("save non-secret launcher choices: %v", err)
	}
	// The row is the store: a second instance on the same database must read the
	// same values, so nothing is cached in memory or read from the mirror.
	reopened, err := launchsettings.NewStore(db, file)
	if err != nil {
		t.Fatal(err)
	}
	requireSettings(t, want, mustLoad(t, reopened))
	// The mirror is the canonical version 6 document the file-based build wrote.
	var document map[string]json.RawMessage
	if err := json.Unmarshal(readFile(t, file), &document); err != nil {
		t.Fatalf("mirror is not a JSON document: %v", err)
	}
	if len(document) != 7 || string(document["version"]) != "6" || document["gameRoot"] == nil || document["riskVersion"] == nil ||
		document["skippedVersion"] == nil || document["network"] == nil || document["downloads"] == nil || document["ui"] == nil {
		t.Fatalf("mirror must carry only the version 6 schema and non-secret choices: %s", readFile(t, file))
	}
	// A row this build wrote records the document version it can read, not the
	// version of whatever legacy file happened to be imported before.
	if version := sourceVersion(t, db); version != 6 {
		t.Fatalf("source_version = %d, want 6 for a row saved by this build", version)
	}
}

func TestSaveWritesMirrorWithoutStaleState(t *testing.T) {
	store, db, file := newStore(t)
	saved := launchsettings.Settings{GameRoot: filepath.Join(t.TempDir(), "game"), RiskVersion: "v1"}
	if err := store.Save(context.Background(), saved); err != nil {
		t.Fatal(err)
	}
	if mirrorStale(t, db) {
		t.Fatal("a mirror this build just wrote reported stale")
	}
	// A rolled-back build may change the file, but the row stays authoritative
	// until the importer reconciles it; the file is never read as a fallback.
	writeLegacy(t, file, []byte(`{"version":1,"gameRoot":"","riskVersion":""}`))
	if !mirrorStale(t, db) {
		t.Error("a mirror changed by another build must report stale")
	}
	requireSettings(t, saved, mustLoad(t, store))
}

func TestZeroValuesSurviveRoundTrip(t *testing.T) {
	store, _, file := newStore(t)
	// No game root, no rate limit and every switch off must all persist: a fresh
	// profile is not a reason to substitute defaults.
	want := launchsettings.Settings{}
	if err := store.Save(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	requireSettings(t, want, mustLoad(t, store))
	mirror := string(readFile(t, file))
	for _, fragment := range []string{
		`"gameRoot":""`, `"riskVersion":""`, `"skippedVersion":""`,
		`"proxy":""`, `"proxyURL":""`, `"mirror":false`, `"mirrorPrefix":""`,
		`"rateLimitKiB":0`,
		`"closeToTray":false`, `"accentFollow":false`, `"checkUpdates":false`,
	} {
		if !strings.Contains(mirror, fragment) {
			t.Errorf("mirror lost the zero value %s: %s", fragment, mirror)
		}
	}
}

func TestInvalidChoicesCannotOverwrite(t *testing.T) {
	store, _, file := newStore(t)
	root := t.TempDir()
	previous := launchsettings.Settings{GameRoot: filepath.Join(root, "old"), RiskVersion: "v1"}
	if err := store.Save(context.Background(), previous); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, file)
	cases := map[string]launchsettings.Settings{
		"relative root":            {GameRoot: "relative"},
		"UNC root":                 {GameRoot: `\\server\share\game`},
		"verbatim root":            {GameRoot: `\\?\C:\game`},
		"alternate data stream":    {GameRoot: `C:\game:stream`},
		"root with trailing dot":   {GameRoot: `C:\bad.\game`},
		"root with trailing space": {GameRoot: `C:\bad \game`},
		"reserved device root":     {GameRoot: `C:\NUL\game`},
		"root with NUL":            {GameRoot: "C:\\bad\x00game"},
		"consent without root":     {RiskVersion: "v1"},
		"oversized consent":        {GameRoot: root, RiskVersion: strings.Repeat("r", 129)},
		"consent with newline":     {GameRoot: root, RiskVersion: "v1\nsecret"},
		"oversized multibyte":      {GameRoot: filepath.Join(root, "game"), RiskVersion: strings.Repeat("界", 10000)},
		"negative rate limit":      {Downloads: launchsettings.Downloads{RateLimitKiB: -1}},
		"rate limit above maximum": {Downloads: launchsettings.Downloads{RateLimitKiB: 1048577}},
		"oversized proxy name":     {Network: launchsettings.Network{Proxy: strings.Repeat("p", 17)}},
		"oversized proxy url":      {Network: launchsettings.Network{ProxyURL: "socks5://" + strings.Repeat("u", 512)}},
		"oversized mirror prefix":  {Network: launchsettings.Network{MirrorPrefix: strings.Repeat("m", 513)}},
		"oversized skipped":        {SkippedVersion: strings.Repeat("s", 65)},
	}
	for name, choice := range cases {
		t.Run(name, func(t *testing.T) {
			if err := store.Save(context.Background(), choice); err == nil {
				t.Fatal("invalid choice was saved")
			}
			requireFile(t, file, before)
			requireSettings(t, previous, mustLoad(t, store))
		})
	}
}

func TestCanceledOperationsRetainPreviousSettings(t *testing.T) {
	store, _, file := newStore(t)
	previous := launchsettings.Settings{GameRoot: filepath.Join(t.TempDir(), "old"), RiskVersion: "v1"}
	if err := store.Save(context.Background(), previous); err != nil {
		t.Fatal(err)
	}
	before := readFile(t, file)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got, err := store.Load(ctx); !errors.Is(err, context.Canceled) || got != (launchsettings.Settings{}) {
		t.Errorf("canceled Load = %+v, %v", got, err)
	}
	if err := store.Save(ctx, launchsettings.Settings{}); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled Save = %v", err)
	}
	if err := store.Update(ctx, func(*launchsettings.Settings) {}); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled Update = %v", err)
	}
	requireFile(t, file, before)
	requireSettings(t, previous, mustLoad(t, store))
}

func TestStoreRejectsUnsafePathForms(t *testing.T) {
	db := dbtest.Open(t)
	for _, path := range []string{"", ".", "launcher-v1.json", `C:relative`, `\root-relative`, `\\server\share\launcher-v1.json`, `//server/share/launcher-v1.json`, `\\?\C:\launcher-v1.json`, `\\.\C:\launcher-v1.json`, `C:\state:stream`, `C:\bad.\launcher-v1.json`, `C:\bad \launcher-v1.json`, `C:\NUL.json`, `C:\COM¹.json`, `C:\LPT²\launcher-v1.json`, "C:\\bad\x00path"} {
		if store, err := launchsettings.NewStore(db, path); err == nil || store != nil {
			t.Errorf("unsafe legacy path accepted: %q, %v", path, err)
		}
	}
	if store, err := launchsettings.NewStore(nil, `C:\state\launcher-v1.json`); err == nil || store != nil {
		t.Errorf("store without a database accepted: %v", err)
	}
}

func TestMissingSettingsReturnDefaultsWithoutCreatingFiles(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "not-created")
	store, _ := newStoreFor(t, filepath.Join(parent, "launcher-v1.json"))
	requireSettings(t, freshProfile(), mustLoad(t, store))
	if _, err := os.Stat(parent); !os.IsNotExist(err) {
		t.Fatalf("reading an empty database must not create the mirror parent: %v", err)
	}
}

func TestUpdateChangesOnlyWhatTheCallerTouches(t *testing.T) {
	store, _, _ := newStore(t)
	root := filepath.Join(t.TempDir(), "game")
	if err := store.Update(context.Background(), func(s *launchsettings.Settings) { s.GameRoot, s.RiskVersion = root, "r1" }); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), func(s *launchsettings.Settings) { s.Network.Proxy = "direct" }); err != nil {
		t.Fatal(err)
	}
	got := mustLoad(t, store)
	if got.GameRoot != root || got.RiskVersion != "r1" || got.Network.Proxy != "direct" {
		t.Fatalf("merge lost a field: %+v", got)
	}
	if err := store.Update(context.Background(), func(s *launchsettings.Settings) { s.SkippedVersion = "bad\x00" }); err == nil {
		t.Fatal("invalid update committed")
	}
	requireSettings(t, got, mustLoad(t, store))
}

func TestConcurrentUpdatesKeepBothFields(t *testing.T) {
	store, _, _ := newStore(t)
	if err := store.Save(context.Background(), launchsettings.Settings{}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "game")
	changes := []func(*launchsettings.Settings){
		func(s *launchsettings.Settings) { s.GameRoot, s.RiskVersion = root, "r1" },
		func(s *launchsettings.Settings) { s.SkippedVersion, s.Network.Mirror = "0.2.0", true },
	}
	start := make(chan struct{})
	failures := make(chan error, len(changes))
	var group sync.WaitGroup
	for _, change := range changes {
		group.Add(1)
		go func(change func(*launchsettings.Settings)) {
			defer group.Done()
			<-start
			if err := store.Update(context.Background(), change); err != nil {
				failures <- err
			}
		}(change)
	}
	close(start)
	group.Wait()
	close(failures)
	for err := range failures {
		t.Errorf("concurrent update: %v", err)
	}
	got := mustLoad(t, store)
	if got.GameRoot != root || got.RiskVersion != "r1" || got.SkippedVersion != "0.2.0" || !got.Network.Mirror {
		t.Fatalf("concurrent updates lost a field: %+v", got)
	}
}

func TestDamagedStoredRowIsNotReplaced(t *testing.T) {
	store, db, file := newStore(t)
	insertDamagedRow(t, db, "relative")
	if got, err := store.Load(context.Background()); err == nil || got != (launchsettings.Settings{}) {
		t.Fatalf("damaged row loaded as %+v, %v", got, err)
	}
	attempts := map[string]func() error{
		"Save":   func() error { return store.Save(context.Background(), launchsettings.Settings{}) },
		"Update": func() error { return store.Update(context.Background(), func(*launchsettings.Settings) {}) },
	}
	for name, attempt := range attempts {
		err := attempt()
		// The error must say the old data was kept, because a caller that retries or
		// reports it must not imply the store was reset to defaults.
		if err == nil || !strings.Contains(err.Error(), "previous launch settings retained") {
			t.Errorf("%s did not refuse a damaged store: %v", name, err)
		}
	}
	if root := insertedGameRoot(t, db); root != "relative" {
		t.Errorf("damaged row was rewritten to %q", root)
	}
	if count := settingsRows(t, db); count != 1 {
		t.Errorf("launch_settings rows = %d, want the damaged row alone", count)
	}
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("refused Save wrote a mirror: %v", err)
	}
}
