// SPDX-License-Identifier: GPL-3.0-or-later

package launchsettings_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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
	if err != nil || got.Downloads != launchsettings.DefaultDownloads() {
		t.Fatalf("v1 = %+v, %v", got, err)
	}
	got.Downloads = launchsettings.Downloads{}
	got.UI = launchsettings.UI{}
	if got != (launchsettings.Settings{}) {
		t.Fatalf("v1 changed unrelated settings: %+v", got)
	}
}

func TestDownloadsMigrationAndValidation(t *testing.T) {
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "launch.json")
	store, _ := launchsettings.NewStore(file)
	if got, err := store.Load(ctx); err != nil || got != (launchsettings.Settings{Downloads: launchsettings.DefaultDownloads(), UI: launchsettings.DefaultUI()}) {
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
	// A v3 document still loads: the removed transfer knobs are dropped and the
	// user's rate limit survives the migration.
	write(`{"version":3` + prefix + `,"downloads":{"filesInFlight":2,"connectionsPerFile":3,"rateLimitKiB":10,"cacheLimitGiB":40,"autoResume":false,"patchApply":false}}`)
	if got, err := store.Load(ctx); err != nil || got.Downloads.RateLimitKiB != 10 {
		t.Fatalf("v3 = %+v, %v", got, err)
	}
	want := launchsettings.Downloads{RateLimitKiB: 128}
	if err := store.Update(ctx, func(s *launchsettings.Settings) { s.Downloads = want }); err != nil {
		t.Fatal(err)
	}
	if got, err := store.Load(ctx); err != nil || got.Downloads != want || got.SkippedVersion != "v2.0.0" {
		t.Fatalf("v4 = %+v, %v", got, err)
	}
	raw, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"version":6`) || strings.Contains(string(raw), "filesInFlight") {
		t.Fatalf("saved document still carries removed transfer settings: %s", raw)
	}
	for _, suffix := range []string{
		`}`, `,"downloads":null}`, `,"downloads":{}}`,
		`,"downloads":{"rateLimitKiB":1048577}}`,
		`,"downloads":{"rateLimitKiB":0,"unknown":true}}`,
		`,"downloads":{"rateLimitKiB":0,"rateLimitKiB":1}}`,
	} {
		write(`{"version":4` + prefix + suffix)
		if _, err := store.Load(ctx); err == nil {
			t.Errorf("accepted %s", suffix)
		}
	}
	write(`{"version":3` + prefix + `,"downloads":{"filesInFlight":9,"connectionsPerFile":1,"rateLimitKiB":0}}`)
	if _, err := store.Load(ctx); err == nil {
		t.Error("accepted a v3 document with an out-of-range transfer count")
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
