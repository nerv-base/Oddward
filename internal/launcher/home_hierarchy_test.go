// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"image"
	"math"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func TestNativeNewsBalancesPrimaryOnSharedBaseline(t *testing.T) {
	for _, size := range [][2]int{{1024, 631}, {1280, 720}, {1600, 900}} {
		for _, flow := range []state.Flow{state.Ready, state.Paused, state.Failed, state.Running} {
			s := NewShell(settled(flow))
			t.Cleanup(s.Dispose)
			tt := ui.NewTester(s.View, size[0], size[1])
			news, ok := tt.Find("官方公告")
			if !ok {
				t.Fatal("missing connected cover and announcements")
			}
			cta, _ := tt.Find(state.Present(s.Model).Primary)
			if news.W < 480 || news.W > newsWidth || news.H != newsHeight {
				t.Fatalf("dispatch card must preserve its horizontal reading footprint: %+v", news)
			}
			if math.Abs(float64(news.Y+news.H-cta.Y-cta.H)) > 1 {
				t.Fatalf("news and action need one bottom baseline: %+v %+v", news, cta)
			}
			if news.X+news.W+40 > cta.X || news.Y < float32(size[1])-260 {
				t.Fatalf("news crowds the stage or primary: %+v %+v", news, cta)
			}
			if !tt.HasText("交互演示") {
				t.Fatal("layout must retain honest demo boundary")
			}
		}
	}
}

func TestNativeNavigationDoesNotCompeteWithPrimary(t *testing.T) {
	s := NewShell(settled(state.Ready))
	t.Cleanup(s.Dispose)
	tt := ui.NewTester(s.View, 1280, 720)
	r, _ := tt.Find("游戏主页")
	red, green, blue, _ := tt.Image().At(int(r.X+5), int(r.Y+r.H/2)).RGBA()
	if (uint64(red)+uint64(green)+uint64(blue))/3 > 30000 {
		t.Fatal("selected navigation still competes as a large bright primary surface")
	}
}

func TestNativeBrandUsesVersionNeutralContainment(t *testing.T) {
	s := NewShell(settled(state.Ready))
	t.Cleanup(s.Dispose)
	s.Logo = ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 277, 195)))
	s.Slogan = ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 1097, 500)))
	tt := ui.NewTester(s.View, 1280, 720)
	logo, _ := tt.Find("异环官方标志")
	slogan, _ := tt.Find("异环官网活动标语")
	if math.Abs(float64(logo.X-slogan.X)) > 1 {
		t.Fatalf("version-neutral safe-area edges drift: logo=%+v slogan=%+v", logo, slogan)
	}
	if gap := slogan.Y - logo.Y - logo.H; gap < 12 || gap > 20 {
		t.Fatalf("full artwork needs a positive safe gap, got %v", gap)
	}
}

func TestOfficialNewsIntentIsGuardedAndNeverOpensExternalSite(t *testing.T) {
	m := settled(state.Ready)
	next, fx := state.Reduce(m, state.Event{Kind: state.OpenOfficial, Index: 1})
	if next.Overlay != state.NewsDetails || next.DetailItem != 1 || next.Flow != m.Flow || next.Token != m.Token {
		t.Fatal("a story must open the reading panel without touching the game flow")
	}
	if len(fx) != 1 || fx[0].Kind != state.FetchDetail || fx[0].Token != next.DetailRequest {
		t.Fatalf("a story must ask for its own official text only: %+v", fx)
	}
	for _, overlay := range []state.Overlay{state.Settings, state.Scenarios, state.About, state.ConfirmQuit, state.NewsDetails} {
		m.Overlay = overlay
		next, fx = state.Reduce(m, state.Event{Kind: state.OpenOfficial})
		if next != m || len(fx) != 0 {
			t.Fatal("modal must guard background news")
		}
	}
	m = settled(state.Ready)
	m.Page = state.WishAnalysis
	next, fx = state.Reduce(m, state.Event{Kind: state.OpenOfficial})
	if next != m || len(fx) != 0 {
		t.Fatal("hidden news must not execute off home")
	}
	s := NewShell(settled(state.Ready))
	t.Cleanup(s.Dispose)
	s.Dispatch(state.Event{Kind: state.NewsLoaded, Token: s.Model.NewsRequest, Rows: state.ProjectNews(officialFeed()), Banners: projectedBannerFixture()})
	tt := ui.NewTester(s.View, 1280, 720)
	if clickFailed(tt, "查看官方公告") || s.Model.Overlay != state.NewsDetails {
		t.Fatal("the official cover is inaccessible")
	}
	if !tt.HasText("OFFICIAL · 官网公告") || tt.HasText("MOCK · 本地资讯示例") {
		t.Fatal("the announcement must be identified as the official page, not as a mock")
	}
	if urls := tt.OpenedURLs(); len(urls) != 0 {
		t.Fatalf("news escaped to external URLs: %v", urls)
	}
}
