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
	start := downloadFixture(state.DownloadRunning, state.StageNetwork)
	start.Download.Downloaded, start.Download.Total = 0, 972<<20
	paused := progress
	paused.Download.Phase = state.DownloadSuspended
	pausedWarn := paused
	pausedWarn.Download.Note = "disk-full"
	verify := downloadFixture(state.DownloadVerifying, state.StageVerify)
	verify.Download.Downloaded, verify.Download.Total = 14<<30, 34<<30
	verify.Download.Rate, verify.Download.ETA = 512<<20, 38
	verify.Download.FilesDone, verify.Download.FilesTotal = 12, 48
	rebuild := downloadFixture(state.DownloadRebuilding, state.StageApply)
	rebuild.Download.Downloaded, rebuild.Download.Total = 3<<30, 4<<30
	rebuild.Download.Rate, rebuild.Download.ETA = 248<<20, 12
	rebuild.Download.FilesDone, rebuild.Download.FilesTotal = 6, 8
	commit := downloadFixture(state.DownloadCommitting, state.StageCommit)
	commit.Download.Downloaded, commit.Download.Total = 20<<30, 48<<30
	commit.Download.Rate, commit.Download.ETA = 1536<<20, 18
	commit.Download.FilesDone, commit.Download.FilesTotal = 14, 31
	for _, tc := range []struct {
		name      string
		model     state.Model
		peekLabel string
	}{
		{"01-first-install", first, ""},
		{"02-start-game", ready, ""},
		{"03-update-game", update, ""},
		{"04-downloading", progress, ""},
		{"05-details", progress, "暂停下载"},
		{"05b-details-start", start, "暂停下载"},
		{"06-paused", paused, ""},
		{"06b-paused-warn-details", pausedWarn, "继续下载"},
		{"07-verifying", verify, ""},
		{"08-verify-details", verify, "检查游戏文件"},
		{"09-rebuild-details", rebuild, "正在更新"},
		{"10-commit-details", commit, "正在更新"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := tc.model
			m.ReducedMotion = true
			s := NewShell(m)
			defer s.Dispose()
			s.Poster, s.News, s.Accent = poster, poster, accent
			s.Dispatch(state.Event{Kind: state.NewsLoaded, Token: s.Model.NewsRequest, Rows: state.ProjectNews(officialFeed()), Banners: projectedBannerFixture()})
			tt := ui.NewTester(s.View, 1280, 720)
			if tc.peekLabel != "" {
				button, ok := tt.AccessByRole(ui.RoleButton, tc.peekLabel)
				if !ok {
					t.Fatalf("missing download action %q", tc.peekLabel)
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

func TestHomeTransferSummaryShowsVerifyAndRebuildThroughput(t *testing.T) {
	t.Run("verify", func(t *testing.T) {
		m := downloadFixture(state.DownloadVerifying, state.StageVerify)
		m.Download.Downloaded, m.Download.Total = 14<<30, 34<<30
		m.Download.Rate, m.Download.ETA = 512<<20, 38
		m.Download.FilesDone, m.Download.FilesTotal = 12, 48
		s := NewShell(m)
		defer s.Dispose()
		tt := ui.NewTester(s.View, 1280, 720)
		primary, ok := tt.AccessByRole(ui.RoleButton, "检查游戏文件")
		if !ok {
			t.Fatalf("missing verify capsule: texts=%q", tt.Texts())
		}
		tt.Move(primary.Bounds.X+primary.Bounds.W/2, primary.Bounds.Y+primary.Bounds.H/2)
		tt.Frame()
		popup, ok := tt.AccessByRole(ui.RoleGroup, "下载详情")
		if !ok || popup.Bounds.W > 240 || popup.Bounds.H > 144 || popup.Bounds.Y+popup.Bounds.H >= primary.Bounds.Y {
			t.Fatalf("verify hover summary out of compact bounds: ok=%v bounds=%+v primary=%+v", ok, popup.Bounds, primary.Bounds)
		}
		for _, want := range []string{"正在校验", "512.0 MB/s", "00:38", "14.0 / 34.0 GB", "12 / 48 文件", "取消任务"} {
			if !tt.HasText(want) {
				t.Errorf("verify summary missing %q; texts=%q", want, tt.Texts())
			}
		}
		if prog, ok := tt.AccessByRole(ui.RoleProgress, "当前阶段进度"); !ok || prog.Now <= 0 {
			t.Fatalf("verify summary missing accessible progress bar: ok=%v prog=%+v", ok, prog)
		}
	})
	t.Run("rebuild", func(t *testing.T) {
		m := downloadFixture(state.DownloadRebuilding, state.StageApply)
		m.Download.Downloaded, m.Download.Total = 3<<30, 4<<30
		m.Download.Rate, m.Download.ETA = 248<<20, 12
		m.Download.FilesDone, m.Download.FilesTotal = 6, 8
		s := NewShell(m)
		defer s.Dispose()
		tt := ui.NewTester(s.View, 1280, 720)
		primary, ok := tt.AccessByRole(ui.RoleButton, "正在更新")
		if !ok {
			t.Fatalf("missing rebuild capsule: texts=%q", tt.Texts())
		}
		tt.Move(primary.Bounds.X+primary.Bounds.W/2, primary.Bounds.Y+primary.Bounds.H/2)
		tt.Frame()
		popup, ok := tt.AccessByRole(ui.RoleGroup, "下载详情")
		if !ok || popup.Bounds.W > 240 || popup.Bounds.H > 144 {
			t.Fatalf("rebuild hover summary out of compact bounds: ok=%v bounds=%+v", ok, popup.Bounds)
		}
		for _, want := range []string{"正在重建文件", "248.0 MB/s", "00:12", "3.0 / 4.0 GB", "6 / 8 文件"} {
			if !tt.HasText(want) {
				t.Errorf("rebuild summary missing %q; texts=%q", want, tt.Texts())
			}
		}
		if _, ok := tt.AccessByRole(ui.RoleButton, "取消任务"); ok {
			t.Error("non-cancellable rebuild stage must hide cancel action")
		}
	})
	t.Run("commit", func(t *testing.T) {
		m := downloadFixture(state.DownloadCommitting, state.StageCommit)
		m.Download.Downloaded, m.Download.Total = 20<<30, 48<<30
		m.Download.Rate, m.Download.ETA = 1536<<20, 18
		m.Download.FilesDone, m.Download.FilesTotal = 14, 31
		s := NewShell(m)
		defer s.Dispose()
		tt := ui.NewTester(s.View, 1280, 720)
		primary, ok := tt.AccessByRole(ui.RoleButton, "正在更新")
		if !ok {
			t.Fatalf("missing commit capsule: texts=%q", tt.Texts())
		}
		tt.Move(primary.Bounds.X+primary.Bounds.W/2, primary.Bounds.Y+primary.Bounds.H/2)
		tt.Frame()
		popup, ok := tt.AccessByRole(ui.RoleGroup, "下载详情")
		if !ok || popup.Bounds.W > 240 || popup.Bounds.H > 144 {
			t.Fatalf("commit hover summary out of compact bounds: ok=%v bounds=%+v", ok, popup.Bounds)
		}
		for _, want := range []string{"正在提交更新", "1.5 GB/s", "00:18", "20.0 / 48.0 GB", "14 / 31 文件"} {
			if !tt.HasText(want) {
				t.Errorf("commit summary missing %q; texts=%q", want, tt.Texts())
			}
		}
		if _, ok := tt.AccessByRole(ui.RoleButton, "取消任务"); ok {
			t.Error("non-cancellable commit stage must hide cancel action")
		}
	})
}

func TestHomeTransferPeekReplacesAccountSlotWithoutBlockingAccounts(t *testing.T) {
	m := downloadFixture(state.DownloadRunning, state.StageNetwork)
	m.Download.Rate, m.Download.ETA = 16<<20, 21
	s := NewShell(m)
	defer s.Dispose()
	s.Dispatch(state.Event{Kind: state.HomeAccountsLoaded, Path: "a", Accounts: []state.SavedAccount{
		{ID: "a", Label: "138****0001"},
		{ID: "b", Label: "夜航星"},
	}})
	tt := ui.NewTester(s.View, 1280, 720)
	acct, ok := tt.AccessByRole(ui.RoleButton, "当前账号 138****0001")
	if !ok {
		t.Fatal("account row missing at rest")
	}
	// Approaching the account row directly must keep it clickable without opening transfer details.
	tt.Move(acct.Bounds.X+acct.Bounds.W/2, acct.Bounds.Y+acct.Bounds.H/2)
	tt.Frame()
	if _, ok := tt.AccessByRole(ui.RoleGroup, "下载详情"); ok {
		t.Fatal("hovering account row directly must not hijack into transfer details")
	}
	if err := tt.Click("当前账号 138****0001"); err != nil {
		t.Fatalf("account row not clickable while transfer runs: %v", err)
	}
	if !tt.HasText("下次使用的账号") {
		t.Fatal("account popup did not open")
	}
	tt.Key(0, ui.KeyEscape)
	tt.Frame()

	// Hovering the bottom capsule opens transfer details directly above it and hides the account row.
	pause, ok := tt.AccessByRole(ui.RoleButton, "暂停下载")
	if !ok {
		t.Fatal("missing pause button")
	}
	tt.Move(pause.Bounds.X+pause.Bounds.W/2, pause.Bounds.Y+pause.Bounds.H/2)
	tt.Frame()
	popup, ok := tt.AccessByRole(ui.RoleGroup, "下载详情")
	if !ok {
		t.Fatal("hovering capsule did not open transfer details")
	}
	if _, acctVisible := tt.AccessByRole(ui.RoleButton, "当前账号 138****0001"); acctVisible {
		t.Fatal("account row must yield to transfer details while peeking")
	}
	if gap := pause.Bounds.Y - (popup.Bounds.Y + popup.Bounds.H); gap < 4 || gap > 16 {
		t.Fatalf("transfer card must sit directly above capsule, gap=%v popup=%+v pause=%+v", gap, popup.Bounds, pause.Bounds)
	}
	// Moving into the left side of the 236px summary card must keep it open.
	tt.Move(popup.Bounds.X+16, popup.Bounds.Y+popup.Bounds.H/2)
	tt.Frame()
	if _, ok := tt.AccessByRole(ui.RoleGroup, "下载详情"); !ok {
		t.Fatal("pointer on left side of transfer summary dismissed the card")
	}
	// Leaving the dock restores the account row and dismisses the card.
	tt.Move(500, 280)
	tt.Frame()
	if _, ok := tt.AccessByRole(ui.RoleGroup, "下载详情"); ok {
		t.Fatal("transfer summary stayed open after leaving dock")
	}
	if _, ok := tt.AccessByRole(ui.RoleButton, "当前账号 138****0001"); !ok {
		t.Fatal("account row did not return after leaving dock")
	}
}
