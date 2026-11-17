// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
	"github.com/nerv-base/Oddward/internal/news"
)

// officialFeed is a small, synthetic shape of what the public site returns:
// three channels, one announcement each, newest first.
func officialFeed() news.Feed {
	var feed news.Feed
	add := func(title, url, date, channel string, category news.Category) {
		when, err := time.Parse("2006-01-02", date)
		if err != nil {
			panic(err)
		}
		feed.Items = append(feed.Items, news.Item{Title: title, URL: url, Date: when, Category: category, Channel: channel})
	}
	add("测试服维护公告", "/news/gamebroad/20991231/100001.html", "2099-12-31", "gamebroad", news.Announcements)
	add("测试版本更新公告", "/news/gamebroad/20991230/100002.html", "2099-12-30", "gamebroad", news.Announcements)
	add("测试联名活动", "/news/gameevent/20991229/100003.html", "2099-12-29", "gameevent", news.Events)
	add("测试情报速递", "/news/gamenews/20991228/100004.html", "2099-12-28", "gamenews", news.News)
	return feed
}

func TestOfficialFeedProjectsIntoThreeChannels(t *testing.T) {
	rows := state.ProjectNews(officialFeed())
	if rows.Counts != [3]uint8{2, 1, 1} {
		t.Fatalf("counts = %v, want 2/1/1", rows.Counts)
	}
	if rows.Titles[0][0] != "测试服维护公告" || rows.Dates[0][0] != "12.31" {
		t.Fatalf("newest announcement not first: %q %q", rows.Titles[0][0], rows.Dates[0][0])
	}
	if rows.URLs[0][1] != "/news/gamebroad/20991230/100002.html" {
		t.Fatalf("row URL = %q", rows.URLs[0][1])
	}
	if rows.Counts[2] != 1 || rows.Titles[2][0] != "测试情报速递" {
		t.Fatalf("news channel not mapped: %v", rows.Titles[2])
	}
	if _, _, _, ok := rows.At(1, 1); ok {
		t.Fatal("a channel must not report a story it does not have")
	}
	if rows.Empty() {
		t.Fatal("a non-empty feed reported empty")
	}
}

func TestBoardRequestsAndFencesTheOfficialRead(t *testing.T) {
	m, fx := state.Reduce(state.Initial(), state.Event{Kind: state.NewsRequested})
	if len(fx) != 1 || fx[0].Kind != state.FetchNews || fx[0].Token != m.NewsRequest || m.NewsRequest == 0 {
		t.Fatalf("read not requested: %+v", fx)
	}
	if m.News != state.NewsPending {
		t.Fatal("a request must show the reading state")
	}
	rows := state.ProjectNews(officialFeed())
	m, fx = state.Reduce(m, state.Event{Kind: state.NewsLoaded, Token: m.NewsRequest, Rows: rows})
	if len(fx) != 0 || m.News != state.NewsPresent || m.NewsRows != rows {
		t.Fatal("a matching response must be shown")
	}
	// A response to an older request must be dropped, not merged.
	stale, _ := state.Reduce(m, state.Event{Kind: state.NewsLoaded, Token: m.NewsRequest - 1, Rows: state.NewsRow{}})
	if stale.NewsRows != rows || stale.News != state.NewsPresent {
		t.Fatal("a stale response replaced the board")
	}
	failed, _ := state.Reduce(m, state.Event{Kind: state.NewsFailed, Token: m.NewsRequest + 1, Message: "旧的失败"})
	if failed.News != state.NewsPresent || failed.NewsError != "" {
		t.Fatal("a stale failure replaced the board")
	}
	failed, _ = state.Reduce(m, state.Event{Kind: state.NewsFailed, Token: m.NewsRequest, Message: "官网不可用"})
	if failed.News != state.NewsUnavailable || failed.NewsError != "官网不可用" || !failed.NewsRows.Empty() {
		t.Fatal("a matching failure must be reported honestly")
	}
}

func TestPendingNewsReadCannotFanOutOnRepeatedClicks(t *testing.T) {
	m, _ := state.Reduce(state.Initial(), state.Event{Kind: state.NewsRequested})
	for i := 0; i < 20; i++ {
		n, fx := state.Reduce(m, state.Event{Kind: state.NewsRequested})
		if n != m || len(fx) != 0 {
			t.Fatal("repeated click while pending started another announcement worker")
		}
	}
}

func TestBoardReadsOnlyFromTheConnectedHome(t *testing.T) {
	away, _ := state.Reduce(state.Initial(), state.Event{Kind: state.Navigate, Page: state.CheckIn})
	if _, fx := state.Reduce(away, state.Event{Kind: state.NewsRequested}); len(fx) != 0 {
		t.Fatal("a background page must not read the feed")
	}
	open, _ := state.Reduce(state.Initial(), state.Event{Kind: state.OpenOverlay, Overlay: state.Settings})
	if _, fx := state.Reduce(open, state.Event{Kind: state.NewsRequested}); len(fx) != 0 {
		t.Fatal("a modal panel must not read the feed")
	}
}

func TestOfficialBoardIsNotLabelledAsMockAndOpensNoBrowser(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	s.Dispatch(state.Event{Kind: state.NewsLoaded, Token: s.Model.NewsRequest, Rows: state.ProjectNews(officialFeed())})
	tt := ui.NewTester(s.View, 1280, 720)
	tt.Frame()
	if !tt.HasText("测试服维护公告") || !tt.HasText("OFFICIAL / 公告") {
		t.Fatal("the board must show the official announcement it read")
	}
	if tt.HasText("资讯示例") || tt.HasText("本地模拟资讯") || tt.HasText("模拟内容") {
		t.Fatal("real official data must not be labelled as mock")
	}
	if !tt.HasText("交互演示") {
		t.Fatal("the demo businesses must keep their honest mock label")
	}
	if clickFailed(tt, "活动") {
		t.Fatal("the category strip must switch channels")
	}
	if s.Model.NewsTab != 1 || !tt.HasText("测试联名活动") {
		t.Fatal("switching to 活动 must show the activity channel")
	}
	tt.Click("测试联名活动")
	if s.Model.Overlay != state.NewsDetails || s.Model.DetailItem != 0 {
		t.Fatal("a story row must open its announcement")
	}
	s.Dispatch(state.Event{Kind: state.DetailLoaded, Token: s.Model.DetailRequest, Body: "测试正文第一段。\n\n第二段。"})
	tt.Frame()
	if !tt.HasText("测试正文第一段。") || !tt.HasText("OFFICIAL · 官网公告") {
		t.Fatal("the detail panel must show the official text")
	}
	if urls := tt.OpenedURLs(); len(urls) != 0 {
		t.Fatalf("the launcher must never open a browser: %v", urls)
	}
	if clickFailed(tt, "返回主页") || s.Model.Overlay != state.NoOverlay || s.Model.NewsTab != 1 {
		t.Fatal("returning must keep the reader on the same channel")
	}
}

func TestBoardReportsFailureAndRetries(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	s.Dispatch(state.Event{Kind: state.NewsFailed, Token: s.Model.NewsRequest, Message: "网络不可用"})
	tt := ui.NewTester(s.View, 1280, 720)
	tt.Frame()
	if !tt.HasText("暂时无法获取官方公告") || !tt.HasText("获取失败") {
		t.Fatal("a failed read must say so instead of inventing announcements")
	}
	before := s.Model.NewsRequest
	if clickFailed(tt, "重新获取公告") {
		t.Fatal("a failed board must offer a retry")
	}
	if s.Model.News != state.NewsPending || s.Model.NewsRequest != before+1 {
		t.Fatalf("retry did not start a new fenced read: %v %d", s.Model.News, s.Model.NewsRequest)
	}
}

func TestEmptyBoardSaysSoAndOpensNothing(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	s.Dispatch(state.Event{Kind: state.NewsLoaded, Token: s.Model.NewsRequest, Rows: state.NewsRow{}})
	tt := ui.NewTester(s.View, 1280, 720)
	tt.Frame()
	if !tt.HasText("官网暂无公告") {
		t.Fatal("an empty feed must be reported as empty")
	}
	before := s.Model.NewsRequest
	tt.Click("查看官方公告")
	if s.Model.Overlay != state.NoOverlay {
		t.Fatal("an empty board must not open a detail panel")
	}
	if s.Model.NewsRequest != before+1 || s.Model.News != state.NewsPending {
		t.Fatal("the cover must re-read the official feed when it has nothing to show")
	}
}
