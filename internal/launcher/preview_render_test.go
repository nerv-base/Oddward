// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// TestRenderPreviewFrames is a manual visual-review harness, skipped unless
// ODDWARD_PREVIEW_OUT names an output directory (keep it under ignored
// artifacts/). Artwork is read only from the optional ODDWARD_* paths; nothing
// official is bundled, and the frames are review material, not evidence of
// real-window rendering, video decode or game compatibility.
func TestRenderPreviewFrames(t *testing.T) {
	out := os.Getenv("ODDWARD_PREVIEW_OUT")
	if out == "" {
		t.Skip("set ODDWARD_PREVIEW_OUT to render review frames")
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		t.Fatal(err)
	}
	load := func(s *Shell) {
		if art, err := LoadPoster("", os.Getenv("ODDWARD_BACKGROUND_STILL")); err == nil {
			s.Poster = art
			if tone, ok := PosterAccent("", os.Getenv("ODDWARD_BACKGROUND_STILL")); ok {
				s.Accent = tone
			}
			s.News = art
		}
		if art, err := LoadArtwork(os.Getenv("ODDWARD_LOGO")); err == nil {
			s.Logo = art
		}
		if art, err := LoadArtwork(os.Getenv("ODDWARD_SLOGAN")); err == nil {
			s.Slogan = art
		}
	}
	type frame struct {
		name  string
		w, h  int
		setup func(*Shell, *ui.Tester)
	}
	loaded := func(s *Shell) {
		s.Dispatch(state.Event{Kind: state.NewsLoaded, Token: s.Model.NewsRequest, Rows: state.ProjectNews(officialFeed()), Banners: projectedBannerFixture()})
	}
	frames := []frame{
		{"home-1280", 1280, 720, func(s *Shell, tt *ui.Tester) { loaded(s) }},
		{"home-1024", 1024, 631, func(s *Shell, tt *ui.Tester) { loaded(s) }},
		{"home-1600", 1600, 900, func(s *Shell, tt *ui.Tester) { loaded(s) }},
		{"home-pending", 1280, 720, nil},
		{"home-download", 1280, 720, func(s *Shell, tt *ui.Tester) {
			loaded(s)
			s.Model.Flow, s.Model.Progress = state.Paused, .42
		}},
		{"home-failed", 1280, 720, func(s *Shell, tt *ui.Tester) {
			s.Dispatch(state.Event{Kind: state.NewsFailed, Token: s.Model.NewsRequest, Message: "网络不可用"})
			s.Model.Flow = state.Failed
		}},
		{"home-hover", 1280, 720, func(s *Shell, tt *ui.Tester) {
			loaded(s)
			if r, ok := tt.Find("启动游戏"); ok {
				tt.Move(r.X+r.W/2, r.Y+r.H/2)
			}
		}},
		{"home-stamina", 1280, 720, func(s *Shell, tt *ui.Tester) {
			loaded(s)
			s.Model.CheckIn.Linked = true
			s.Model.CheckIn.Loaded = true
			s.Model.CheckIn.StatusPhase = state.CheckStatusReady
			s.Model.CheckIn.Role = state.CheckInRole{RoleID: "218020980063", Name: "小小绫波丽", Stamina: 187, StaminaMax: 240, FetchedAt: time.Now().Add(-2 * time.Minute)}
		}},
		{"media-bar", 1280, 720, func(s *Shell, tt *ui.Tester) {
			loaded(s)
			tt.Key(0, ui.KeyF6)
		}},
		{"game-home", 1024, 631, func(s *Shell, tt *ui.Tester) {
			s.Model.NormalMode = true
			s.Model.Managed = normalFixture(state.ManagedReady).Managed
			loaded(s)
		}},
		{"launcher-settings", 1024, 631, func(s *Shell, tt *ui.Tester) {
			s.Model.NormalMode = true
			s.Model.Managed = normalFixture(state.ManagedReady).Managed
			loaded(s)
			if err := tt.Click("启动器设置"); err != nil {
				t.Fatal(err)
			}
		}},
		{"game-settings", 1024, 631, func(s *Shell, tt *ui.Tester) {
			s.Model.NormalMode = true
			s.Model.Managed = normalFixture(state.ManagedReady).Managed
			loaded(s)
			if err := tt.Click("游戏设置"); err != nil {
				t.Fatal(err)
			}
		}},
		{"analysis", 1280, 720, func(s *Shell, tt *ui.Tester) { _ = tt.Click("抽卡分析") }},
		{"game-data", 1280, 720, func(s *Shell, tt *ui.Tester) {
			s.Model.CheckIn = state.CheckInState{
				AccountID: "acct", RoleID: "218020980063", Loaded: true, Linked: true,
				StatusPhase: state.CheckStatusReady,
				Role: state.CheckInRole{
					RoleID: "218020980063", Name: "小小绫波丽", Server: "天空之城",
					Level: 61, WorldLevel: 12, Stamina: 187, StaminaMax: 240,
					CityStamina: 92, CityStaminaMax: 120, DayValue: 100, WeekRemain: 2,
					LoginDays: 128, CharCount: 24, Achievement: 512, AchievementTotal: 900,
					FetchedAt: time.Now(),
					Details: &state.CheckInDetails{
						Areas: []state.CheckInArea{{ID: "area-1", Name: "新港", Progress: 12, Total: 30}},
						Characters: []state.CheckInCharacter{{
							ID: "1019", Name: "合成角色", ElementType: "CHARACTER_ELEMENT_TYPE_PSYCHE",
							Quality: "ITEM_QUALITY_ORANGE", Level: 40, MixLevel: 3, Likeability: 7, Awaken: 2,
						}},
						RealEstate: state.CheckInCollection{Own: 3, Total: 9, ShowName: "小屋"},
						Vehicle:    state.CheckInCollection{Own: 2, Total: 5, ShowName: "载具"},
					},
				},
			}
			if err := tt.Click("游戏数据"); err != nil {
				t.Fatal(err)
			}
		}},
		{"no-art", 1280, 720, func(s *Shell, tt *ui.Tester) {
			s.Poster, s.Logo, s.Slogan, s.News, s.Accent = nil, nil, nil, nil, ui.Color{}
			loaded(s)
		}},
	}
	for _, f := range frames {
		m := settled(state.Ready)
		m.ReducedMotion = true
		s := NewShell(m)
		load(s)
		tt := ui.NewTester(s.View, f.w, f.h)
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
