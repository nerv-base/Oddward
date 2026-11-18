// SPDX-License-Identifier: GPL-3.0-or-later

package launchsettings_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nerv-base/Oddward/internal/launchsettings"
)

func TestVersionOneDocumentsStillLoad(t *testing.T) {
	file := filepath.Join(t.TempDir(), "launch.json")
	if err := os.WriteFile(file, []byte(`{"version":1,"gameRoot":"","riskVersion":""}`), 0600); err != nil {
		t.Fatal(err)
	}
	store, _ := launchsettings.NewStore(file)
	got, err := store.Load(context.Background())
	if err != nil || got.Downloads != (launchsettings.Downloads{FilesInFlight: 4, ConnectionsPerFile: 4, AutoResume: true, PatchApply: true}) {
		t.Fatalf("v1 = %+v, %v", got, err)
	}
	got.Downloads = launchsettings.Downloads{}
	if got != (launchsettings.Settings{}) {
		t.Fatalf("v1 changed unrelated settings: %+v", got)
	}
}

func TestDownloadsMigrationAndValidation(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "launch.json")
	store, _ := launchsettings.NewStore(file)
	if got, err := store.Load(ctx); err != nil || got != (launchsettings.Settings{}) {
		t.Fatalf("missing settings = %+v, %v", got, err)
	}
	prefix := `,"gameRoot":"","riskVersion":"","skippedVersion":"v2.0.0","network":{}`
	write := func(s string) {
		t.Helper()
		if err := os.WriteFile(file, []byte(s), 0600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"version":2` + prefix + `}`)
	if got, err := store.Load(ctx); err != nil || got.Downloads != launchsettings.DefaultDownloads() {
		t.Fatalf("v2 = %+v, %v", got, err)
	}
	want := launchsettings.Downloads{FilesInFlight: 8, ConnectionsPerFile: 16, RateLimitKiB: 128}
	if err := store.Update(ctx, func(s *launchsettings.Settings) { s.Downloads = want }); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Load(ctx); err != nil || got.Downloads != want || got.SkippedVersion != "v2.0.0" {
		t.Fatalf("v3 = %+v, %v", got, err)
	}
	write(`{"version":3` + prefix + `,"downloads":{"filesInFlight":2,"connectionsPerFile":3,"rateLimitKiB":10}}`)
	if got, err := store.Load(ctx); err != nil || !got.Downloads.AutoResume {
		t.Fatalf("omitted autoResume: %+v, %v", got, err)
	}
	for _, suffix := range []string{
		`}`, `,"downloads":null}`, `,"downloads":{}}`,
		`,"downloads":{"filesInFlight":9,"connectionsPerFile":1,"rateLimitKiB":0}}`,
		`,"downloads":{"filesInFlight":1,"connectionsPerFile":1,"rateLimitKiB":0,"autoResume":null}}`,
		`,"downloads":{"filesInFlight":1,"connectionsPerFile":1,"rateLimitKiB":0,"unknown":true}}`,
		`,"downloads":{"filesInFlight":1,"filesInFlight":2,"connectionsPerFile":1,"rateLimitKiB":0}}`,
	} {
		write(`{"version":3` + prefix + suffix)
		if _, err := store.Load(ctx); err == nil {
			t.Errorf("accepted %s", suffix)
		}
	}
}

func TestNetworkAndSkippedVersionRoundTrip(t *testing.T) {
	store, _ := launchsettings.NewStore(filepath.Join(t.TempDir(), "launch.json"))
	want := launchsettings.Settings{
		SkippedVersion: "0.2.0",
		Network:        launchsettings.Network{Proxy: "custom", ProxyURL: "socks5://127.0.0.1:7891", Mirror: true, MirrorPrefix: "https://gh-proxy.com/"},
	}
	if err := store.Save(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(context.Background())
	want.Downloads = launchsettings.DefaultDownloads()
	if err != nil || got != want {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestUpdateChangesOnlyWhatTheCallerTouches(t *testing.T) {
	store, _ := launchsettings.NewStore(filepath.Join(t.TempDir(), "launch.json"))
	root := filepath.Join(t.TempDir(), "game")
	if err := store.Update(context.Background(), func(s *launchsettings.Settings) { s.GameRoot, s.RiskVersion = root, "r1" }); err != nil {
		t.Fatal(err)
	}
	if err := store.Update(context.Background(), func(s *launchsettings.Settings) { s.Network.Proxy = "direct" }); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(context.Background())
	if err != nil || got.GameRoot != root || got.RiskVersion != "r1" || got.Network.Proxy != "direct" {
		t.Fatalf("merge lost a field: %+v, %v", got, err)
	}
	if err := store.Update(context.Background(), func(s *launchsettings.Settings) { s.SkippedVersion = "bad\x00" }); err == nil {
		t.Fatal("invalid update committed")
	}
	if after, _ := store.Load(context.Background()); after != got {
		t.Fatalf("rejected update changed settings: %+v", after)
	}
}
