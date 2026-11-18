// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"image"
	"image/color"
	"math"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// The "frame" composition is version-neutral: the key art sits in a rounded
// canvas inside a dark bezel, the launch tone is derived from whatever art the
// current version ships, and the dock never depends on one slogan's shape.

var frameSizes = [][2]int{{1024, 631}, {1280, 720}, {1600, 900}}

func near(a, b, tolerance float32) bool { return math.Abs(float64(a-b)) <= float64(tolerance) }

func paintBands(w, h int, bands ...struct {
	share float64
	c     color.RGBA
}) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	x := 0
	for i, band := range bands {
		end := x + int(float64(w)*band.share)
		if i == len(bands)-1 {
			end = w
		}
		for ; x < end; x++ {
			for y := 0; y < h; y++ {
				img.SetRGBA(x, y, band.c)
			}
		}
	}
	return img
}

type band = struct {
	share float64
	c     color.RGBA
}

func relLuminance(c ui.Color) float64 {
	lin := func(v uint8) float64 {
		f := float64(v) / 255
		if f <= .04045 {
			return f / 12.92
		}
		return math.Pow((f+.055)/1.055, 2.4)
	}
	return .2126*lin(c.R) + .7152*lin(c.G) + .0722*lin(c.B)
}

func contrast(a, b ui.Color) float64 {
	la, lb := relLuminance(a), relLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + .05) / (lb + .05)
}

func TestLaunchButtonWearsTheArtTone(t *testing.T) {
	for _, tc := range []struct {
		accent ui.Color
		want   ui.Color
	}{{ui.Color{}, defaultAccent}, {ui.Hex("#FF8A66"), ui.Hex("#FF8A66")}} {
		s := NewShell(settled(state.Ready))
		s.Accent = tc.accent
		tt := ui.NewTester(s.View, 1280, 720)
		cta, _ := tt.Find("启动游戏")
		img := tt.Image()
		got := img.RGBAAt(int(cta.X+cta.W-30), int(cta.Y+cta.H/2))
		if !near(float32(got.R), float32(tc.want.R), 10) || !near(float32(got.G), float32(tc.want.G), 10) || !near(float32(got.B), float32(tc.want.B), 10) {
			t.Fatalf("launch pill must be filled with %+v, got %+v", tc.want, got)
		}
		corner := img.RGBAAt(int(cta.X+2), int(cta.Y+2))
		if near(float32(corner.R), float32(tc.want.R), 10) && near(float32(corner.G), float32(tc.want.G), 10) && near(float32(corner.B), float32(tc.want.B), 10) {
			t.Fatalf("launch button must be a pill, its corner is filled: %+v", corner)
		}
		s.Dispose()
	}
}

func TestFrameInsetsTheArtInARoundedCanvas(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	s.Poster = ui.NewBitmap(paintBands(32, 18, band{1, color.RGBA{230, 200, 170, 255}}))
	tt := ui.NewTester(s.View, 1280, 720)
	img := tt.Image()
	isBezel := func(x, y int) bool {
		c := img.RGBAAt(x, y)
		return near(float32(c.R), float32(bezel.R), 4) && near(float32(c.G), float32(bezel.G), 4) && near(float32(c.B), float32(bezel.B), 4)
	}
	for _, p := range [][2]int{{1280 - 3, 360}, {640, 2}, {640, 720 - 3}, {int(railWidth) + 1, int(bezelWidth) + 1}} {
		if !isBezel(p[0], p[1]) {
			t.Fatalf("bezel missing at %v: %+v", p, img.RGBAAt(p[0], p[1]))
		}
	}
	if isBezel(int(railWidth)+int(canvasRadius)+20, 300) {
		t.Fatal("the key art must fill the canvas")
	}
}

func TestDockSharesOneBaselineAcrossSizesAndFlows(t *testing.T) {
	for _, size := range frameSizes {
		for _, flow := range []state.Flow{state.Ready, state.Paused, state.Failed, state.Running} {
			s := NewShell(settled(flow))
			tt := ui.NewTester(s.View, size[0], size[1])
			w, h := float32(size[0]), float32(size[1])
			cta, ok := tt.AccessByRole(ui.RoleButton, state.Present(s.Model).Primary)
			menu, menuOK := tt.AccessByRole(ui.RoleButton, "游戏设置")
			news, newsOK := tt.Find("官方公告")
			if !ok || !menuOK || !newsOK {
				t.Fatal("dock needs the news card, the launch pill and its options button")
			}
			b, m := cta.Bounds, menu.Bounds
			if m.W != actionHeight || m.H != actionHeight || !near(m.X+m.W, w-dockRight, .5) || !near(m.Y+m.H, h-homeBottom, .5) {
				t.Fatalf("options button left the dock corner: size=%v %+v", size, m)
			}
			if b.W != actionWidth || b.H != actionHeight || !near(b.X+b.W, m.X-menuGap, .5) || !near(b.Y+b.H, h-homeBottom, .5) {
				t.Fatalf("launch pill left the dock: size=%v flow=%v %+v", size, flow, b)
			}
			if news.X != homeLeft || !near(news.Y+news.H, h-homeBottom, .5) || news.H != newsHeight {
				t.Fatalf("news card left the shared baseline: %+v", news)
			}
			if news.W < 480 || news.X+news.W+40 > b.X {
				t.Fatalf("news card must stay wide and clear of the launch pill: news=%+v cta=%+v", news, b)
			}
			s.Dispose()
		}
	}
}

func TestDemoGameSettingsButtonIsDisabled(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1280, 720)
	button, ok := tt.AccessByRole(ui.RoleButton, "游戏设置")
	if !ok || !button.Disabled {
		t.Fatalf("the demo must retain the dock layout without enabling real game settings: %+v", button)
	}
}

func TestNewsCardPutsCoverBesideReading(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1280, 720)
	s.Dispatch(state.Event{Kind: state.NewsLoaded, Token: s.Model.NewsRequest, Rows: state.ProjectNews(officialFeed()), Banners: projectedBannerFixture()})
	tt.Frame()
	news, _ := tt.Find("官方公告")
	cover, coverOK := tt.Find("查看官方公告")
	reading, readingOK := tt.Find("公告分类")
	if !coverOK || !readingOK {
		t.Fatal("news card needs a cover and a reading column")
	}
	if cover.X != news.X || cover.Y != news.Y || cover.H != news.H || cover.W != coverWidth {
		t.Fatalf("cover must fill the card's left edge: news=%+v cover=%+v", news, cover)
	}
	if !near(reading.X, cover.X+cover.W, .5) || !near(reading.X+reading.W, news.X+news.W, .5) {
		t.Fatalf("reading column must fill the rest of the card: %+v", reading)
	}
	if !tt.HasText("测试服维护公告") {
		t.Fatal("reading column lost the official rows")
	}
}

func TestHeroAcceptsAnySloganShape(t *testing.T) {
	logoBitmap := ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, 277, 195)))
	for _, sloganSize := range [][2]int{{1097, 500}, {600, 600}, {1600, 240}} {
		for _, size := range frameSizes {
			s := NewShell(settled(state.Ready))
			s.Logo = logoBitmap
			s.Slogan = ui.NewBitmap(image.NewRGBA(image.Rect(0, 0, sloganSize[0], sloganSize[1])))
			tt := ui.NewTester(s.View, size[0], size[1])
			logo, ok := tt.Find("异环官方标志")
			slogan, sloganOK := tt.Find("异环官网活动标语")
			news, _ := tt.Find("官方公告")
			if !ok || !sloganOK {
				t.Fatal("official logo and campaign slogan must both remain")
			}
			if logo.X != homeLeft || logo.Y < 32 || logo.Y > 48 || logo.W < 100 || logo.W > 124 {
				t.Fatalf("logo left the top-left safe area: %+v", logo)
			}
			want := float32(sloganSize[0]) / float32(sloganSize[1])
			if !near(logo.W/logo.H, 277./195, .02) || !near(slogan.W/slogan.H, want, .02) {
				t.Fatalf("official artwork must keep its aspect ratio: logo=%+v slogan=%+v", logo, slogan)
			}
			if slogan.X != homeLeft || slogan.W > sloganWidth || slogan.H > sloganHeight || slogan.Y < logo.Y+logo.H+12 || slogan.Y+slogan.H > news.Y-32 {
				t.Fatalf("slogan %v must sit between logo and dock: logo=%+v slogan=%+v news=%+v", sloganSize, logo, slogan, news)
			}
			s.Dispose()
		}
	}
}

func TestRailHoldsRoundedPageTiles(t *testing.T) {
	for _, size := range frameSizes {
		s := NewShell(settled(state.Ready))
		tt := ui.NewTester(s.View, size[0], size[1])
		var previous ui.Rect
		for i, label := range []string{"游戏主页", "抽卡分析", "自动签到"} {
			b, ok := tt.AccessByRole(ui.RoleButton, label)
			if !ok || b.Bounds.W != tileSize || b.Bounds.H != tileSize || !near(b.Bounds.X, (railWidth-tileSize)/2, .5) {
				t.Fatalf("%s must be a centered %vx%v tile: %+v", label, tileSize, tileSize, b.Bounds)
			}
			if i > 0 && !near(b.Bounds.Y, previous.Y+previous.H+tileGap, .5) {
				t.Fatalf("rail tiles drift from the %v DIP rhythm: %+v after %+v", tileGap, b.Bounds, previous)
			}
			previous = b.Bounds
		}
		settings, ok := tt.AccessByRole(ui.RoleButton, "启动器设置")
		if !ok || settings.Bounds.W != tileSize || settings.Bounds.Y+settings.Bounds.H < float32(size[1])-28 {
			t.Fatalf("settings must close the rail at the bottom: %+v", settings.Bounds)
		}
		s.Dispose()
	}
}

func TestRailMarksCurrentPageWithToneIndicator(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1280, 720)
	home, _ := tt.Find("游戏主页")
	other, _ := tt.Find("抽卡分析")
	img := tt.Image()
	isTone := func(c color.RGBA) bool {
		return near(float32(c.R), float32(defaultAccent.R), 16) && near(float32(c.G), float32(defaultAccent.G), 16) && near(float32(c.B), float32(defaultAccent.B), 16)
	}
	if mark := img.RGBAAt(2, int(home.Y+home.H/2)); !isTone(mark) {
		t.Fatalf("current page needs a tone indicator at the rail edge, got %+v", mark)
	}
	if rest := img.RGBAAt(2, int(other.Y+other.H/2)); isTone(rest) {
		t.Fatalf("only the current page carries the indicator, got %+v", rest)
	}
}

func TestCaptionButtonsAreRoundGlassInsideTheCanvas(t *testing.T) {
	for _, size := range frameSizes {
		s := NewShell(settled(state.Ready))
		tt := ui.NewTester(s.View, size[0], size[1])
		closeButton, ok := tt.AccessByRole(ui.RoleButton, "关闭窗口")
		minimize, minOK := tt.AccessByRole(ui.RoleButton, "最小化")
		if !ok || !minOK {
			t.Fatal("caption buttons missing")
		}
		c, m := closeButton.Bounds, minimize.Bounds
		if c.W != captionSize || c.H != captionSize || !near(c.X+c.W, float32(size[0])-bezelWidth-16, .5) || !near(c.Y, bezelWidth+16, .5) {
			t.Fatalf("close must be a round button in the canvas corner: %+v", c)
		}
		if m.W != captionSize || !near(m.Y, c.Y, .5) || m.X >= c.X {
			t.Fatalf("caption buttons must share one row: min=%+v close=%+v", m, c)
		}
		s.Dispose()
	}
}

func TestLaunchPillStatesStayLegible(t *testing.T) {
	for _, flow := range []state.Flow{state.Ready, state.Paused, state.Failed, state.Running, state.Checking} {
		m := settled(flow)
		m.Progress = .42
		s := NewShell(m)
		tt := ui.NewTester(s.View, 1280, 720)
		p := state.Present(s.Model)
		if !tt.HasText(p.Status) {
			t.Fatalf("launch pill must caption its status %q", p.Status)
		}
		cta, _ := tt.Find(p.Primary)
		if p.Progress {
			bar, ok := tt.AccessByRole(ui.RoleProgress, "任务进度")
			if !ok || bar.Bounds.Y+bar.Bounds.H > cta.Y {
				t.Fatalf("progress rides in the task deck above the launch pill: %+v over %+v", bar.Bounds, cta)
			}
			if !tt.HasText("42%") {
				t.Fatal("progress percentage missing")
			}
		}
		s.Dispose()
	}
}
