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

func TestHomeTransferUsesCompactDockAndTemporaryDetails(t *testing.T) {
	for _, size := range [][2]int{{1024, 631}, {1280, 720}, {1600, 900}} {
		m := downloadFixture(state.DownloadRunning, state.StageNetwork)
		m.Download.Rate, m.Download.ETA = 3<<20, 442
		s := NewShell(m)
		tt := ui.NewTester(s.View, size[0], size[1])
		pause, pauseOK := tt.AccessByRole(ui.RoleButton, "暂停下载")
		gear, gearOK := tt.AccessByRole(ui.RoleButton, "游戏设置")
		if !pauseOK || !gearOK || pause.Bounds.H != homeDockHeight || gear.Bounds.H != homeDockHeight {
			t.Fatalf("%v compact actions must share one 48 DIP row: %+v %+v", size, pause.Bounds, gear.Bounds)
		}
		if _, ok := tt.AccessByRole(ui.RoleButton, "查看下载详情"); ok {
			t.Fatal("a separate download details button must not exist")
		}
		if tt.HasText("MB/s") || !tt.HasText("07:22") {
			t.Fatal("only reported remaining time belongs in the compact dock")
		}
		tt.Move(pause.Bounds.X+pause.Bounds.W/2, pause.Bounds.Y+pause.Bounds.H/2)
		tt.Frame()
		popup, ok := tt.AccessByRole(ui.RoleGroup, "下载详情")
		if !ok || popup.Bounds.W > 240 || popup.Bounds.H > 144 || popup.Bounds.Y+popup.Bounds.H >= pause.Bounds.Y || !tt.HasText("3.0 MB/s") || tt.HasText("本地复用") {
			t.Fatalf("hover should reveal only a compact transfer summary above the dock: %+v", popup.Bounds)
		}
		reached := false
		for range 24 {
			tt.Key(0, ui.KeyTab)
			if node, ok := tt.AccessByRole(ui.RoleButton, "暂停下载"); ok && node.Focused {
				reached = true
				break
			}
		}
		if !reached {
			t.Fatal("the transfer action is not keyboard reachable")
		}
		tt.Move(500, 280)
		tt.Frame()
		if _, ok := tt.AccessByRole(ui.RoleGroup, "下载详情"); ok {
			t.Fatal("hover details stayed open after leaving the dock")
		}
		s.Dispose()
	}
}

func TestRenderCompactHomeFrames(t *testing.T) {
	out := os.Getenv("ODDWARD_COMPACT_HOME_OUT")
	if out == "" {
		t.Skip("set ODDWARD_COMPACT_HOME_OUT to render production home with synthetic states")
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	still := os.Getenv("ODDWARD_BACKGROUND_STILL")
	poster, err := LoadPoster("", still)
	if err != nil {
		t.Fatal(err)
	}
	accent, _ := PosterAccent("", still)
	ready := normalFixture(state.ManagedReady)
	first := normalFixture(state.ManagedUnlocated)
	first.Managed.Root = ""
	update := ready
	update.Download.Located, update.Download.Verified = true, true
	update, _ = state.Reduce(update, state.Event{Kind: state.DownloadOfferPollRequest})
	update, _ = state.Reduce(update, state.Event{Kind: state.DownloadOfferPolled, Token: update.Download.OfferRequest, Path: update.Managed.Root, Message: "1.1.0"})
	progress := downloadFixture(state.DownloadRunning, state.StageNetwork)
	progress.Download.Downloaded, progress.Download.Total = 12<<30, 18<<30
	progress.Download.Rate, progress.Download.ETA = 16<<20, 384
	paused := progress
	paused.Download.Phase = state.DownloadSuspended
	verify := downloadFixture(state.DownloadVerifying, state.StageVerify)
	for _, tc := range []struct {
		name  string
		model state.Model
		peek  bool
	}{
		{"01-first-install", first, false},
		{"02-start-game", ready, false},
		{"03-update-game", update, false},
		{"04-downloading", progress, false},
		{"05-details", progress, true},
		{"06-paused", paused, false},
		{"07-verifying", verify, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.model
			m.ReducedMotion = true
			s := NewShell(m)
			defer s.Dispose()
			s.Poster, s.News, s.Accent = poster, poster, accent
			s.Dispatch(state.Event{Kind: state.NewsLoaded, Token: s.Model.NewsRequest, Rows: state.ProjectNews(officialFeed()), Banners: projectedBannerFixture()})
			tt := ui.NewTester(s.View, 1280, 720)
			if tc.peek {
				button, ok := tt.AccessByRole(ui.RoleButton, "暂停下载")
				if !ok {
					t.Fatal("missing download action")
				}
				tt.Move(button.Bounds.X+button.Bounds.W/2, button.Bounds.Y+button.Bounds.H/2)
				tt.Frame()
			}
			file, err := os.Create(filepath.Join(out, tc.name+".png"))
			if err != nil {
				t.Fatal(err)
			}
			err = png.Encode(file, tt.Image())
			closeErr := file.Close()
			if err != nil {
				t.Fatal(err)
			}
			if closeErr != nil {
				t.Fatal(closeErr)
			}
		})
	}
}

func TestHomeCancelRequiresConfirmation(t *testing.T) {
	m := downloadFixture(state.DownloadRunning, state.StageNetwork)
	s := NewShell(m)
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1280, 720)
	pause, ok := tt.AccessByRole(ui.RoleButton, "暂停下载")
	if !ok {
		t.Fatal("missing download action")
	}
	tt.Move(pause.Bounds.X+pause.Bounds.W/2, pause.Bounds.Y+pause.Bounds.H/2)
	tt.Frame()
	if err := tt.Click("取消任务"); err != nil {
		t.Fatal(err)
	}
	if _, ok := tt.AccessByRole(ui.RoleDialog, "取消当前任务？"); !ok || s.Model.Download.Request != m.Download.Request {
		t.Fatal("opening confirmation must leave the running task alone")
	}
	tt.Key(0, ui.KeyEscape)
	if s.Model.Overlay != state.NoOverlay || s.Model.Download.Request != m.Download.Request {
		t.Fatal("dismissing confirmation must leave the running task alone")
	}
}
