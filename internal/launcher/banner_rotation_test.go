// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func rotationBoard() *Shell {
	m := settled(state.Ready)
	m.News = state.NewsPresent
	m.NewsBanners = state.BannerRow{
		Count:  2,
		Titles: [8]string{"官方推荐甲", "官方推荐乙"},
		URLs:   [8]string{"https://yh.wanmei.com/news/a", "https://yh.wanmei.com/news/b"},
	}
	return NewShell(m)
}

func TestOfficialCoverRotatesAndWrapsWithoutChangingCategory(t *testing.T) {
	s := rotationBoard()
	defer s.Dispose()
	s.Model.NewsTab = 1
	tt := ui.NewTester(s.View, 1280, 720)
	tt.Advance(5 * time.Second)
	if s.Model.NewsIndex != 0 {
		t.Fatal("cover changed before its viewing interval ended")
	}
	tt.Advance(time.Second + time.Millisecond)
	if s.Model.NewsIndex != 1 || s.Model.NewsTab != 1 || !tt.HasText("官方推荐乙") {
		t.Fatalf("expected next cover and unchanged category, index=%d tab=%d frameLag=%v rotation=%v motion=%v", s.Model.NewsIndex, s.Model.NewsTab, tt.Now().Sub(s.now), s.bannerRotation.deadline.Sub(tt.Now()), s.motion())
	}
	tt.Advance(6*time.Second + time.Millisecond)
	if s.Model.NewsIndex != 0 || !tt.HasText("官方推荐甲") {
		t.Fatal("carousel did not wrap to the first official cover")
	}
}

func TestOfficialCoverWheelStepsAndDebounces(t *testing.T) {
	s := rotationBoard()
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1280, 720)
	r, ok := tt.Find("查看官方公告")
	if !ok {
		t.Fatal("missing cover")
	}
	x, y := r.X+r.W/2, r.Y+r.H/2
	tt.Scroll(x, y, 0, 1)
	tt.Frame()
	if s.Model.NewsIndex != 1 {
		t.Fatalf("wheel down did not select the next cover: index=%d", s.Model.NewsIndex)
	}
	tt.Scroll(x, y, 0, 1)
	tt.Frame()
	if s.Model.NewsIndex != 1 {
		t.Fatal("a second wheel event inside the debounce window switched twice")
	}
	tt.Advance(200 * time.Millisecond)
	tt.Scroll(x, y, 0, -1)
	tt.Frame()
	if s.Model.NewsIndex != 0 {
		t.Fatalf("wheel up did not select the previous cover: index=%d", s.Model.NewsIndex)
	}
}

func TestOfficialCoverRotationPausesWhileInteractingOrReducingMotion(t *testing.T) {
	s := rotationBoard()
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1280, 720)
	r, ok := tt.Find("查看官方公告")
	if !ok {
		t.Fatal("missing cover")
	}
	tt.Move(r.X+r.W/2, r.Y+r.H/2)
	tt.Advance(12 * time.Second)
	if s.Model.NewsIndex != 0 {
		t.Fatal("cover advanced under the pointer")
	}
	tt.Move(1100, 200)
	tt.Advance(6*time.Second + time.Millisecond)
	if s.Model.NewsIndex != 1 {
		t.Fatal("cover did not resume after leaving")
	}
	s.Dispatch(state.Event{Kind: state.Navigate, Page: state.LauncherSettings})
	tt.Advance(12 * time.Second)
	if s.Model.NewsIndex != 1 {
		t.Fatal("cover advanced behind settings")
	}
	s.Dispatch(state.Event{Kind: state.ToggleReducedMotion})
	s.Dispatch(state.Event{Kind: state.Dismiss})
	tt.Frame()
	tt.Advance(12 * time.Second)
	if s.Model.NewsIndex != 1 {
		t.Fatal("cover advanced with reduced motion set")
	}
	if err := tt.Click("官方轮播封面 1"); err != nil || s.Model.NewsIndex != 0 {
		t.Fatalf("manual selection must remain available, index=%d err=%v", s.Model.NewsIndex, err)
	}
}
