// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/gacha"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// previewUpdate is a hand-written launcher release for review frames only.
func previewUpdate(phase state.UpdatePhase, downloaded int64) state.UpdateState {
	return state.UpdateState{
		Phase:      phase,
		Current:    "0.1.0",
		Version:    "0.2.0",
		Notes:      "· 启动器支持自更新：新版本在独立目录中校验后切换\n· 新增网络设置：系统代理、自定义代理与 GitHub 加速\n· 修复公告卡在小窗口下的截断",
		Size:       17 << 20,
		Downloaded: downloaded,
	}
}

// TestRenderMockStateFrames renders one review frame per mock state (every
// launch flow, the announcement board states, overlays and the in-memory demo
// pages). Skipped unless ODDWARD_STATES_OUT names an output directory under
// ignored artifacts/. Frames are review material only.
// previewCheckIn is a fully populated, linked check-in state with the numbers
// the status panel draws. Day is left empty so the first tick adopts the
// tester's day instead of rolling these fixtures away.
func previewCheckIn() state.CheckInState {
	stamp := time.Date(2026, 11, 17, 15, 27, 0, 0, time.Local)
	c := state.CheckInState{
		AccountID: "preview-account", RoleID: "100012345",
		Linked: true, Loaded: true, StatusPhase: state.CheckStatusReady,
		Role: state.CheckInRole{
			RoleID: "100012345", Name: "合成测试角色", Server: "天空之城",
			Level: 61, WorldLevel: 12,
			Stamina: 187, StaminaMax: 240,
			CityStamina: 92, CityStaminaMax: 120,
			DayValue: 100, WeekRemain: 2,
			LoginDays: 128, CharCount: 24,
			Achievement: 512, AchievementTotal: 900,
			FetchedAt: stamp,
		},
	}
	for k := 0; k < state.CheckInKinds; k++ {
		c.Known[k], c.Phase[k] = true, state.CheckInReady
	}
	return c
}

func TestRenderMockStateFrames(t *testing.T) {
	out := os.Getenv("ODDWARD_STATES_OUT")
	if out == "" {
		t.Skip("set ODDWARD_STATES_OUT to render mock state frames")
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	still := os.Getenv("ODDWARD_BACKGROUND_STILL")
	load := func(s *Shell) {
		if art, err := LoadPoster("", still); err == nil {
			s.Poster, s.News = art, art
			if tone, ok := PosterAccent("", still); ok {
				s.Accent = tone
			}
		}
		if art, err := LoadArtwork(os.Getenv("ODDWARD_LOGO")); err == nil {
			s.Logo = art
		}
		if art, err := LoadArtwork(os.Getenv("ODDWARD_SLOGAN")); err == nil {
			s.Slogan = art
		}
	}
	loaded := func(s *Shell) {
		s.Dispatch(state.Event{Kind: state.NewsLoaded, Token: s.Model.NewsRequest, Rows: state.ProjectNews(officialFeed()), Banners: projectedBannerFixture()})
	}
	flow := func(f state.Flow, progress float64) func(*Shell, *ui.Tester) {
		return func(s *Shell, _ *ui.Tester) {
			loaded(s)
			s.Model.Flow, s.Model.Progress = f, progress
			if f == state.Failed {
				s.Model.Error = "演示失败：模拟连接中断；未发起网络请求或修改游戏文件。"
			}
		}
	}
	type frame struct {
		name  string
		setup func(*Shell, *ui.Tester)
	}
	frames := []frame{
		{"01-checking", flow(state.Checking, 0)},
		{"02-not-installed", flow(state.NotInstalled, 0)},
		{"03-update-available", flow(state.UpdateAvailable, 0)},
		{"04-downloading", func(s *Shell, tt *ui.Tester) {
			flow(state.Downloading, .43)(s, tt)
			s.Model.Transfer = state.Transfer{Downloaded: 6 << 30, Total: 14 << 30, Rate: 18 << 20}
		}},
		{"05-paused", func(s *Shell, tt *ui.Tester) {
			flow(state.Paused, .43)(s, tt)
			s.Model.Transfer = state.Transfer{Downloaded: 6 << 30, Total: 14 << 30}
		}},
		{"06-verifying", func(s *Shell, tt *ui.Tester) {
			flow(state.Verifying, .88)(s, tt)
			s.Model.Transfer = state.Transfer{Downloaded: 12 << 30, Total: 14 << 30, Rate: 420 << 20}
		}},
		{"06b-downloading-no-figures", flow(state.Downloading, .63)},
		{"07-ready", flow(state.Ready, 0)},
		{"08-launching", flow(state.Launching, 0)},
		{"09-running", flow(state.Running, 0)},
		{"10-failed", flow(state.Failed, 0)},
		{"11-news-pending", nil},
		{"12-news-unavailable", func(s *Shell, _ *ui.Tester) {
			s.Dispatch(state.Event{Kind: state.NewsFailed, Token: s.Model.NewsRequest, Message: "网络不可用"})
		}},
		{"13-news-activity-tab", func(s *Shell, tt *ui.Tester) {
			loaded(s)
			s.Dispatch(state.Event{Kind: state.SelectNewsTab, Index: 1})
		}},
		{"14-cta-hover", func(s *Shell, tt *ui.Tester) {
			loaded(s)
			tt.Frame()
			if r, ok := tt.Find("启动游戏"); ok {
				tt.Move(r.X+r.W/2, r.Y+r.H/2)
			}
		}},
		{"15-media-console", func(s *Shell, tt *ui.Tester) {
			loaded(s)
			tt.Frame()
			tt.Key(0, ui.KeyF6)
		}},
		{"16-settings", func(s *Shell, tt *ui.Tester) {
			loaded(s)
			s.Dispatch(state.Event{Kind: state.OpenOverlay, Overlay: state.Settings})
		}},
		{"17-scenarios", func(s *Shell, tt *ui.Tester) {
			loaded(s)
			s.Dispatch(state.Event{Kind: state.OpenOverlay, Overlay: state.Scenarios})
		}},
		{"18-about", func(s *Shell, tt *ui.Tester) {
			loaded(s)
			s.Dispatch(state.Event{Kind: state.OpenOverlay, Overlay: state.About})
		}},
		{"19-confirm-quit", func(s *Shell, tt *ui.Tester) {
			loaded(s)
			s.Model.Flow, s.Model.Progress = state.Downloading, .63
			s.Dispatch(state.Event{Kind: state.RequestQuit})
		}},
		{"20-wish-empty", func(s *Shell, _ *ui.Tester) {
			s.Dispatch(state.Event{Kind: state.Navigate, Page: state.WishAnalysis})
		}},
		{"21-wish-loading", func(s *Shell, _ *ui.Tester) {
			s.Dispatch(state.Event{Kind: state.Navigate, Page: state.WishAnalysis})
			s.Model.Wish = state.WishState{Phase: state.WishLoading}
		}},
		{"22-wish-ready", func(s *Shell, _ *ui.Tester) {
			s.Dispatch(state.Event{Kind: state.Navigate, Page: state.WishAnalysis})
			a := wishArchiveFixture()
			s.wish.archive, s.wish.stats, s.wish.roles = a, gacha.Statistics(a), []string{a.RoleID}
			s.WishLocation = time.FixedZone("preview-local", 8*60*60)
			s.Model.Wish = state.WishState{RoleID: a.RoleID, Phase: state.WishReady, NewCount: 6, ImportReported: true}
		}},
		{"23-wish-failed", func(s *Shell, _ *ui.Tester) {
			s.Dispatch(state.Event{Kind: state.Navigate, Page: state.WishAnalysis})
			s.Model.Wish = state.WishState{Phase: state.WishFailed, Failure: state.WishFailureIO}
		}},
		{"23b-wish-delete-confirm", func(s *Shell, _ *ui.Tester) {
			s.WishLocation = time.FixedZone("preview-local", 8*60*60)
			s.Model.Page = state.WishAnalysis
			a := wishArchiveFixture()
			s.wish.archive, s.wish.stats, s.wish.roles = a, gacha.Statistics(a), []string{a.RoleID}
			s.Model.Wish = state.WishState{RoleID: a.RoleID, Phase: state.WishReady, Confirm: state.WishConfirmation{Kind: state.WishConfirmDelete, RoleID: a.RoleID, Count: len(a.Records)}}
		}},
		{"22b-wish-linked", func(s *Shell, _ *ui.Tester) {
			s.Model.Page = state.WishAnalysis
			a := wishFeaturedArchiveFixture()
			s.wish.archive, s.wish.stats, s.wish.roles = a, gacha.Statistics(a), []string{a.RoleID}
			s.Model.Wish = state.WishState{RoleID: a.RoleID, Phase: state.WishReady, Link: state.WishLinkState{AccountID: "preview-account", Phase: state.WishLinked}}
			s.wishLink.status = "已关联塔吉多，点击更新记录获取 S 记录"
		}},
		{"35-wish-link-notice", func(s *Shell, _ *ui.Tester) {
			s.Model.Page = state.WishAnalysis
			s.Model.Wish.Link = state.WishLinkState{AccountID: "preview-account", Phase: state.WishLinking, Step: state.WishLinkNotice}
		}},
		{"36-wish-link-phone", func(s *Shell, _ *ui.Tester) {
			s.Model.Page = state.WishAnalysis
			s.Model.Wish.Link = state.WishLinkState{AccountID: "preview-account", Phase: state.WishLinking, Step: state.WishLinkPhone, Agreed: true}
		}},
		{"37-wish-link-role", func(s *Shell, _ *ui.Tester) {
			s.Model.Page = state.WishAnalysis
			s.Model.Wish.Link = state.WishLinkState{AccountID: "preview-account", Phase: state.WishLinking, Step: state.WishLinkRole, Agreed: true}
			s.wishLink.roles = []WishLinkRole{{ID: "123456", Name: "合成测试角色", Bound: true}, {ID: "654321", Name: "另一个合成角色"}}
		}},
		{"24-checkin-unlinked", func(s *Shell, _ *ui.Tester) {
			s.Dispatch(state.Event{Kind: state.Navigate, Page: state.CheckIn})
			c := previewCheckIn()
			c.Linked, c.RoleID, c.StatusPhase = false, "", state.CheckStatusIdle
			for k := 0; k < state.CheckInKinds; k++ {
				c.Known[k], c.Phase[k] = false, state.CheckInUnlinked
			}
			s.Model.CheckIn = c
		}},
		{"24b-checkin-ready", func(s *Shell, _ *ui.Tester) {
			s.Dispatch(state.Event{Kind: state.Navigate, Page: state.CheckIn})
			c := previewCheckIn()
			c.Auto[0] = true
			s.Model.CheckIn = c
		}},
		{"24c-checkin-done", func(s *Shell, _ *ui.Tester) {
			s.Dispatch(state.Event{Kind: state.Navigate, Page: state.CheckIn})
			c := previewCheckIn()
			c.Auto[0], c.Auto[1] = true, true
			for k := 0; k < state.CheckInKinds; k++ {
				c.Signed[k], c.Phase[k] = true, state.CheckInDone
				c.At[k] = time.Date(2026, 11, 17, 15, 28, 0, 0, time.Local)
			}
			c.Result[0], c.Detail[0] = state.CheckResultSuccess, "本性像素 ×60"
			c.Result[1], c.Detail[1] = state.CheckResultAlready, "服务器显示今日已签到"
			s.Model.CheckIn = c
		}},
		{"24d-checkin-partial", func(s *Shell, _ *ui.Tester) {
			s.Dispatch(state.Event{Kind: state.Navigate, Page: state.CheckIn})
			c := previewCheckIn()
			c.Auto[0] = true
			c.Signed[0], c.Phase[0] = true, state.CheckInDone
			c.Result[0], c.Detail[0] = state.CheckResultSuccess, "本性像素 ×60"
			c.At[0] = time.Date(2026, 11, 17, 15, 28, 0, 0, time.Local)
			c.Phase[1], c.Result[1], c.Failure[1] = state.CheckInFailed, state.CheckResultFailed, state.CheckInFailureOffline
			c.Detail[1] = "网络连接失败，请稍后再试"
			s.Model.CheckIn = c
		}},
		{"24e-checkin-unknown", func(s *Shell, _ *ui.Tester) {
			s.Dispatch(state.Event{Kind: state.Navigate, Page: state.CheckIn})
			c := previewCheckIn()
			c.Phase[1], c.Result[1] = state.CheckInUnknown, state.CheckResultUnverified
			c.Detail[1] = "服务器称已签到，但状态未确认"
			s.Model.CheckIn = c
		}},
		{"24f-checkin-status-failed", func(s *Shell, _ *ui.Tester) {
			s.Dispatch(state.Event{Kind: state.Navigate, Page: state.CheckIn})
			c := previewCheckIn()
			c.StatusPhase, c.StatusFailure, c.Role = state.CheckStatusFailed, state.CheckInFailureOffline, state.CheckInRole{}
			s.Model.CheckIn = c
		}},
		{"26-update-pill", func(s *Shell, _ *ui.Tester) {
			loaded(s)
			s.Model.Update = previewUpdate(state.SelfUpdateAvailable, 0)
		}},
		{"27-update-panel", func(s *Shell, _ *ui.Tester) {
			loaded(s)
			s.Model.Update = previewUpdate(state.SelfUpdateAvailable, 0)
			s.Dispatch(state.Event{Kind: state.OpenOverlay, Overlay: state.UpdateDetails})
		}},
		{"28-update-downloading", func(s *Shell, _ *ui.Tester) {
			loaded(s)
			s.Model.Update = previewUpdate(state.SelfUpdateDownloading, 7<<20)
			s.Dispatch(state.Event{Kind: state.OpenOverlay, Overlay: state.UpdateDetails})
		}},
		{"29-update-ready-blocked", func(s *Shell, _ *ui.Tester) {
			loaded(s)
			s.Model.Flow = state.Running
			s.Model.Update = previewUpdate(state.SelfUpdateReady, 17<<20)
			s.Dispatch(state.Event{Kind: state.OpenOverlay, Overlay: state.UpdateDetails})
		}},
		{"30-settings-version", func(s *Shell, _ *ui.Tester) {
			loaded(s)
			s.Model.Update = previewUpdate(state.SelfUpdateAvailable, 0)
			s.Dispatch(state.Event{Kind: state.OpenOverlay, Overlay: state.Settings})
		}},
		{"31-network", func(s *Shell, _ *ui.Tester) {
			loaded(s)
			s.Model.Network = state.NetworkState{Proxy: state.ProxyCustom, ProxyURL: "socks5://127.0.0.1:7890", Mirror: true, MirrorPrefix: "https://gh-proxy.com/"}
			s.openNetworkSettings()
		}},
		{"32-launch-notice", func(s *Shell, _ *ui.Tester) {
			loaded(s)
			s.Model.NormalMode, s.Model.Managed = true, normalFixture(state.ManagedReady).Managed
			s.Model.Overlay = state.LaunchNotice
		}},
		{"33-component-preparing", func(s *Shell, _ *ui.Tester) {
			loaded(s)
			s.Model.NormalMode, s.Model.Managed = true, normalFixture(state.ManagedPreparing).Managed
			s.Dispatch(state.Event{Kind: state.ManagedProgress, Token: s.Model.Managed.Request, Downloaded: 31 << 20, Total: 72 << 20, Message: "准备本地登录窗口"})
		}},
		{"34-component-running", func(s *Shell, _ *ui.Tester) {
			loaded(s)
			s.Model.NormalMode, s.Model.Managed = true, normalFixture(state.ManagedRunning).Managed
			s.Model.Managed.Message = "登录组件运行中；请在组件窗口完成登录。游戏状态尚未确认。"
		}},
		{"25-no-art", func(s *Shell, _ *ui.Tester) {
			s.Poster, s.Logo, s.Slogan, s.News, s.Accent = nil, nil, nil, nil, ui.Color{}
			loaded(s)
		}},
	}
	only := os.Getenv("ODDWARD_STATES_ONLY")
	for _, f := range frames {
		if only != "" && !strings.HasPrefix(f.name, only) {
			continue
		}
		m := settled(state.Ready)
		m.ReducedMotion = true
		s := NewShell(m)
		load(s)
		tt := ui.NewTester(s.View, 1280, 720)
		if f.setup != nil {
			f.setup(s, tt)
		}
		tt.Advance(400 * time.Millisecond)
		tt.Frame()
		file, err := os.Create(filepath.Join(out, f.name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(file, tt.Image()); err != nil {
			t.Fatal(err)
		}
		file.Close()
		s.Dispose()
	}
}
