// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/nerv-base/Oddward/internal/database/dbtest"
	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/launcher/state"
	"github.com/nerv-base/Oddward/internal/launchsettings"
	"github.com/nerv-base/Oddward/internal/netcfg"
)

func tempStore(t *testing.T) *launchsettings.Store {
	t.Helper()
	s, err := launchsettings.NewStore(dbtest.Open(t), filepath.Join(t.TempDir(), "launcher-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestEachAdapterSavesOnlyItsOwnFields(t *testing.T) {
	ctx := context.Background()
	store := tempStore(t)
	downloads := state.DownloadPreferences{RateLimitKiB: 512}
	backend := &GameDownloadBackend{Settings: store}
	if got := backend.HandleDownload(ctx, launcher.DownloadRequest{Effect: state.Effect{Kind: state.EffectSaveDownloads}, Settings: downloads}, nil); got.Kind != state.DownloadSettingsSaved {
		t.Fatalf("download settings save = %d %s", got.Kind, got.Message)
	}
	network := netcfg.New(netcfg.Default(), func() netcfg.SystemProxy { return netcfg.SystemProxy{} })
	if err := (SystemNetwork{Network: network, Store: store}).Apply(ctx, state.NetworkState{Proxy: state.ProxyDirect, Mirror: true}); err != nil {
		t.Fatal(err)
	}
	if err := (&SystemUpdater{Store: store}).SaveSkipped(ctx, "0.2.0"); err != nil {
		t.Fatal(err)
	}
	if err := (NativeSettingsStore{Store: store}).Save(ctx, launcher.ManagedPreferences{GameRoot: `C:\Games\NTE`, RiskVersion: "1"}); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := launchsettings.Settings{GameRoot: `C:\Games\NTE`, RiskVersion: "1", SkippedVersion: "0.2.0",
		Network:   launchsettings.Network{Proxy: "direct", Mirror: true, MirrorPrefix: netcfg.DefaultMirrorPrefix},
		Downloads: launchsettings.Downloads{RateLimitKiB: 512}, UI: launchsettings.DefaultUI()}
	if got != want {
		t.Fatalf("saving one panel must keep the others:\n got %+v\nwant %+v", got, want)
	}
	if s := network.Settings(); s.Proxy != netcfg.Direct || !s.Mirror {
		t.Fatalf("network not applied: %+v", s)
	}
}

func TestInvalidNetworkIsNeitherSavedNorApplied(t *testing.T) {
	ctx := context.Background()
	store := tempStore(t)
	network := netcfg.New(netcfg.Default(), func() netcfg.SystemProxy { return netcfg.SystemProxy{} })
	err := (SystemNetwork{Network: network, Store: store}).Apply(ctx, state.NetworkState{Proxy: state.ProxyCustom, ProxyURL: "ftp://example.com"})
	if err == nil {
		t.Fatal("accepted an unsupported proxy")
	}
	if got, _ := store.Load(ctx); got != (launchsettings.Settings{UI: launchsettings.DefaultUI()}) {
		t.Fatalf("saved %+v", got)
	}
	if network.Settings() != netcfg.Default() {
		t.Fatal("applied an invalid proxy")
	}
}

func TestSavedNetworkFallsBackToDefaults(t *testing.T) {
	s, v := SavedNetwork(launchsettings.Network{})
	if s != netcfg.Default() || v.Proxy != state.ProxySystem || v.MirrorPrefix != netcfg.DefaultMirrorPrefix {
		t.Fatalf("empty: %+v %+v", s, v)
	}
	s, v = SavedNetwork(launchsettings.Network{Proxy: "custom", ProxyURL: "socks5://127.0.0.1:1080", Mirror: true, MirrorPrefix: "https://mirror.example/"})
	if s.Proxy != netcfg.Custom || v.Proxy != state.ProxyCustom || v.ProxyURL != "socks5://127.0.0.1:1080" || !v.Mirror || v.MirrorPrefix != "https://mirror.example/" {
		t.Fatalf("custom: %+v %+v", s, v)
	}
	s, v = SavedNetwork(launchsettings.Network{Proxy: "custom", ProxyURL: "nonsense"})
	if s != netcfg.Default() || v.Proxy != state.ProxySystem {
		t.Fatalf("invalid must fall back: %+v %+v", s, v)
	}
}

func TestStageNeedsAVerifiedCheckFirst(t *testing.T) {
	if err := (&SystemUpdater{}).Stage(context.Background(), func(int64, int64) {}); err == nil {
		t.Fatal("staged without a verified manifest")
	}
}
