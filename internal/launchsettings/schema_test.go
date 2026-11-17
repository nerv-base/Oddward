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
	if err != nil || got != (launchsettings.Settings{}) {
		t.Fatalf("v1 = %+v, %v", got, err)
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
