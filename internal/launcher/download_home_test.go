// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// downloadFixture builds a home model mid-transfer. Home carries the whole
// transfer state on the capsule, so nothing here opens a panel.
func downloadFixture(phase state.DownloadPhase, stage state.DownloadStage) state.Model {
	m := normalFixture(state.ManagedReady)
	m.ReducedMotion = true
	m.Download = state.DownloadState{Phase: phase, Stage: stage, Kind: state.DownloadUpdate, Route: state.RoutePatch, Request: 9, Current: "1.0.0", Version: "1.1.0", Downloaded: 2 << 20, Total: 8 << 20, PatchBytes: 6 << 20, Reused: 20 << 20, FilesDone: 2, FilesTotal: 8, Located: true}
	return m
}

func offeredUpdate(t *testing.T, offer string) state.Model {
	t.Helper()
	m := normalFixture(state.ManagedReady)
	m.Download.Located, m.Download.Verified = true, true
	m, _ = state.Reduce(m, state.Event{Kind: state.DownloadOfferPollRequest})
	m, _ = state.Reduce(m, state.Event{Kind: state.DownloadOfferPolled, Token: m.Download.OfferRequest, Path: m.Managed.Root, Message: offer})
	return m
}

func TestHomeUpdateCapsuleStartsWithoutDialog(t *testing.T) {
	m := offeredUpdate(t, "1.1.0")
	s := NewShell(m)
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1280, 720)
	if _, ok := tt.AccessByRole(ui.RoleButton, "更新游戏"); !ok {
		t.Fatalf("an offered update must own the capsule: present=%+v download=%+v texts=%q", state.Present(s.Model), s.Model.Download, tt.Texts())
	}
	if err := tt.Click("更新游戏"); err != nil {
		t.Fatal(err)
	}
	if s.Model.Overlay != state.NoOverlay || s.Model.Download.Kind != state.DownloadUpdate || s.Model.Download.AwaitingConfirmation {
		t.Fatalf("clicking update must plan at once without a dialog: %+v", s.Model.Download)
	}
	if s.Model.Download.Request == 0 {
		t.Fatal("clicking update must ask the backend to plan")
	}
}

func TestHomePreloadChipSitsLeftOfTheCapsule(t *testing.T) {
	m := normalFixture(state.ManagedReady)
	m.Download.Located, m.Download.Verified = true, true
	m, _ = state.Reduce(m, state.Event{Kind: state.DownloadOfferPollRequest})
	m, _ = state.Reduce(m, state.Event{Kind: state.DownloadOfferPolled, Token: m.Download.OfferRequest, Path: m.Managed.Root, Body: "1.2.0", Enabled: true})
	s := NewShell(m)
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1280, 720)
	chip, ok := tt.AccessByRole(ui.RoleButton, "预下载")
	if !ok {
		t.Fatal("a preload offer must add a chip")
	}
	launch, ok := tt.AccessByRole(ui.RoleButton, "启动游戏")
	if !ok {
		t.Fatal("a preload offer must leave the game launch available")
	}
	if chip.Bounds.X+chip.Bounds.W > launch.Bounds.X {
		t.Fatalf("the preload chip must sit left of the capsule: chip=%+v launch=%+v", chip.Bounds, launch.Bounds)
	}
	if err := tt.Click("预下载"); err != nil {
		t.Fatal(err)
	}
	if s.Model.Download.Kind != state.DownloadPreload {
		t.Fatalf("the chip must ask for a preload, got %+v", s.Model.Download)
	}
}

func TestHomeShowsSpaceShortOnTheCapsule(t *testing.T) {
	m := normalFixture(state.ManagedReady)
	m.Download.Located, m.Download.Verified = true, true
	m, _ = state.Reduce(m, state.Event{Kind: state.DownloadRequest})
	m, _ = state.Reduce(m, state.Event{Kind: state.DownloadPlanned, Token: m.Download.Request, Message: "1.1.0", Total: 8 << 20, Needed: 64 << 20, Free: 1 << 20, Enabled: true})
	s := NewShell(m)
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1280, 720)
	if _, ok := tt.AccessByRole(ui.RoleButton, "空间不足"); !ok {
		t.Fatalf("a short plan must say so on the capsule: %+v", state.Present(s.Model))
	}
	if s.Model.Overlay != state.NoOverlay || s.Model.Download.Phase == state.DownloadRunning {
		t.Fatal("a short plan must not start")
	}
}

func TestDownloadSettingsExposeMaintenanceActions(t *testing.T) {
	m := normalFixture(state.ManagedReady)
	m.ReducedMotion = true
	m.Download.Located, m.Download.Verified = true, true
	m, _ = state.Reduce(m, state.Event{Kind: state.DownloadOfferPollRequest})
	m, _ = state.Reduce(m, state.Event{Kind: state.DownloadOfferPolled, Token: m.Download.OfferRequest, Path: m.Managed.Root, Body: "1.2.0", Enabled: true})
	s := NewShell(m)
	defer s.Dispose()
	s.Dispatch(state.Event{Kind: state.Navigate, Page: state.LauncherSettings})
	s.selectSettingsSection(state.SettingsNetwork)
	tt := ui.NewTester(s.View, 1280, 720)
	for _, label := range []string{"检查游戏更新", "检查预下载"} {
		if _, ok := tt.AccessByRole(ui.RoleButton, label); !ok {
			t.Errorf("settings lost the %q action", label)
		}
	}

	running := downloadFixture(state.DownloadRunning, state.StageNetwork)
	s2 := NewShell(running)
	defer s2.Dispose()
	s2.Dispatch(state.Event{Kind: state.Navigate, Page: state.LauncherSettings})
	s2.selectSettingsSection(state.SettingsNetwork)
	tt2 := ui.NewTester(s2.View, 1280, 720)
	if _, ok := tt2.AccessByRole(ui.RoleButton, "取消任务"); !ok {
		t.Error("a running transfer must stay cancellable from settings")
	}
	if _, ok := tt2.AccessByRole(ui.RoleButton, "详细信息"); ok {
		t.Error("the retired details panel must not come back")
	}
}

func TestRenderDownloadHomeFrames(t *testing.T) {
	out := os.Getenv("ODDWARD_DOWNLOAD_PREVIEW_OUT")
	if out == "" {
		t.Skip("set ODDWARD_DOWNLOAD_PREVIEW_OUT to render synthetic download home states")
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	progress := downloadFixture(state.DownloadRunning, state.StageNetwork)
	progress.Download.Downloaded, progress.Download.Rate, progress.Download.ETA = 3<<30, 16<<20, 442
	preload, _ := state.Reduce(func() state.Model {
		m := normalFixture(state.ManagedReady)
		m.Download.Located, m.Download.Verified = true, true
		return m
	}(), state.Event{Kind: state.DownloadOfferPollRequest})
	preload, _ = state.Reduce(preload, state.Event{Kind: state.DownloadOfferPolled, Token: preload.Download.OfferRequest, Path: preload.Managed.Root, Body: "1.2.0", Enabled: true})
	m, _ := state.Reduce(normalFixture(state.ManagedReady), state.Event{Kind: state.DownloadRequest})
	m, _ = state.Reduce(m, state.Event{Kind: state.DownloadPlanned, Token: m.Download.Request, Message: "1.1.0", Total: 8 << 20, Needed: 64 << 20, Free: 1 << 20, Enabled: true})
	settings := progress
	settings.Page, settings.SettingsSection = state.LauncherSettings, state.SettingsNetwork
	for _, tc := range []struct {
		name string
		m    state.Model
		h    int
	}{
		{"01-update-offer", offeredUpdate(t, "1.1.0"), 720},
		{"02-downloading", progress, 720},
		{"03-preload-chip", preload, 720},
		{"04-space-short", m, 720},
		{"05-settings-maintenance", settings, 1200},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := NewShell(tc.m)
			defer s.Dispose()
			tt := ui.NewTester(s.View, 1280, tc.h)
			tt.Frame()
			file, err := os.Create(filepath.Join(out, tc.name+".png"))
			if err != nil {
				t.Fatal(err)
			}
			encodeErr := png.Encode(file, tt.Image())
			closeErr := file.Close()
			if encodeErr != nil {
				t.Fatal(encodeErr)
			}
			if closeErr != nil {
				t.Fatal(closeErr)
			}
		})
	}
}
