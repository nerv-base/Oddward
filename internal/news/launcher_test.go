// SPDX-License-Identifier: GPL-3.0-or-later

package news

import (
	"strings"
	"testing"
	"time"
)

// launcherPage mirrors the structure of the real official launcher page
// (div.news-cont blocks in the page order 资讯, 公告, 活动; a row is a
// tooltip div plus an anchor plus a trailing date span). The content itself is
// synthetic: no official text is copied into the repository.
const launcherPage = `<!DOCTYPE html>
<html><head><title>异环</title></head>
<body>
<div class="wrap" id="wrap" style="display: none;">
  <div class="main-cont">
    <div class="news-box">
      <ul class="news-tab">
        <li class="on">新闻</li>
        <li>公告</li>
        <li>活动</li>
      </ul>
      <div class="news-cont" style="display: block;">
        <ul>
          <li>
            <div class="hover-qipao"><span class="qipap-first"></span><span class="qipap-cont">资讯其一提示</span><span class="qipap-last"></span></div>
            <a href="/news/gamenews/20990922/100001.html" target="_blank">资讯其一</a>
            <span>09/22</span>
          </li>
          <li>
            <div class="hover-qipao"><span class="qipap-first"></span><span class="qipap-cont">资讯其二提示</span><span class="qipap-last"></span></div>
            <a href="/news/gamenews/20990808/100002.html" target="_blank">资讯其二</a>
            <span>08/08</span>
          </li>
        </ul>
      </div>
      <div class="news-cont">
        <ul>
          <li>
            <div class="hover-qipao"><span class="qipap-first"></span><span class="qipap-cont">01/01</span><span class="qipap-last"></span></div>
            <a href="/news/gamebroad/20991106/200001.html" target="_blank">公告其一</a>
            <span>11/06</span>
          </li>
          <li>
            <div class="hover-qipao"><span class="qipap-first"></span><span class="qipap-cont">公告其二提示</span><span class="qipap-last"></span></div>
            <a href="/news/gamebroad/20991104/200002.html" target="_blank">公告其二</a>
            <span>11/04</span>
          </li>
        </ul>
      </div>
      <div class="news-cont">
        <ul>
          <li>
            <div class="hover-qipao"><span class="qipap-first"></span><span class="qipap-cont">活动其一提示</span><span class="qipap-last"></span></div>
            <a href="/news/gameevent/20991027/300001.html" target="_blank">活动其一</a>
            <span>10/27</span>
          </li>
          <li>
            <div class="hover-qipao"><span class="qipap-first"></span><span class="qipap-cont">活动其二提示</span><span class="qipap-last"></span></div>
            <a href="/news/gameevent/20990706/300002.html" target="_blank">活动其二</a>
            <span>07/02</span>
          </li>
        </ul>
      </div>
    </div>
  </div>
</div>
<span id="btn-switch" class="on"></span>
<ul class="ewm-list">
  <li class="icon-weibo">
    <div class="ewm-hover">
      <a class="ewm-title" href="https://weibo.com/u/0000000000" target="_blank">访问官方微博 &gt;</a>
      <img src="https://example.invalid/weibo.jpg">
      <span class="ewm-tip">扫码关注官方微博</span>
    </div>
  </li>
</ul>
</body></html>`

const launcherBase = "https://example.invalid"

var launcherPageHosts = []string{"example.invalid"}

func parseLauncherFixture(t *testing.T, page string) Feed {
	t.Helper()
	feed, err := parseLauncherPage([]byte(page), launcherBase, launcherPageHosts)
	if err != nil {
		t.Fatalf("launcher page parse failed: %v", err)
	}
	return feed
}

// The page lists 资讯, 公告, 活动 — the board's tabs are 公告, 活动, 资讯. Filing
// a row by the block it sits in would therefore misfile everything, so a row is
// filed by the channel its own article URL names and the block order is ignored.
func TestLauncherPageMapsCategoriesByTheArticleChannelNotTheBlockOrder(t *testing.T) {
	feed := parseLauncherFixture(t, launcherPage)
	if got := feed.Counts(); got != [3]uint8{2, 2, 2} {
		t.Fatalf("counts = %v, want 2/2/2", got)
	}
	announcements := feed.Of(Announcements)
	if announcements[0].Title != "公告其一" {
		t.Fatalf("the 公告 block must land in Announcements, got %+v", announcements)
	}
	if feed.Of(News)[0].Title != "资讯其一" {
		t.Fatalf("the 新闻/资讯 block must land in News, got %+v", feed.Of(News))
	}
	if feed.Of(Events)[0].Title != "活动其一" {
		t.Fatalf("the 活动 block must land in Events, got %+v", feed.Of(Events))
	}
	for _, item := range feed.Items {
		if !strings.HasPrefix(item.URL, launcherBase+"/news/") {
			t.Fatalf("launcher links must resolve to the official origin: %+v", item)
		}
		if want := Category(item.Category).Channel(); item.Channel != want {
			t.Fatalf("channel must follow the article URL: %+v", item)
		}
		if want := "/news/" + item.Channel + "/"; !strings.Contains(item.URL, want) {
			t.Fatalf("category and URL channel disagree: %+v", item)
		}
	}
}

// The tooltip bubble carries its own spans a few characters above the real date
// span. Only the row's own trailing span may be read as the date.
func TestLauncherPageReadsTheRowDateNotTheTooltip(t *testing.T) {
	feed := parseLauncherFixture(t, launcherPage)
	first := feed.Of(Announcements)[0]
	if first.Title != "公告其一" {
		t.Fatalf("the anchor text is the row title, not the tooltip: %+v", first)
	}
	if !first.Date.Equal(time.Date(2099, 11, 6, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("row date must come from the trailing span, got %v", first.Date)
	}
	for _, item := range feed.Items {
		if item.Title == "01/01" || strings.Contains(item.Title, "提示") {
			t.Fatalf("a tooltip span leaked into the feed as a row: %+v", item)
		}
	}
}

// A row shows only 月/日; the article URL carries the publication year. The
// board therefore shows the displayed month/day in the URL's year at 00:00 UTC
// and never invents a time of day. The row and the link may disagree (observed
// upstream: link 20260706, row 07/02); the row wins for what is displayed.
func TestLauncherPageKeepsTheDisplayedMonthDayAndTakesTheYearFromTheLink(t *testing.T) {
	feed := parseLauncherFixture(t, launcherPage)
	mismatched := feed.Of(Events)[1]
	if mismatched.Title != "活动其二" {
		t.Fatalf("row order changed: %+v", feed.Of(Events))
	}
	if !mismatched.Date.Equal(time.Date(2099, 7, 2, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("displayed 07/02 in the link's year expected, got %v", mismatched.Date)
	}
	if got := mismatched.ShortDate(); got != "07.02" {
		t.Fatalf("the board must show the official month/day, got %q", got)
	}
	if got := mismatched.DateText(); got != "2099.07.02" {
		t.Fatalf("DateText = %q", got)
	}
	if hour, minute, second := mismatched.Date.Clock(); hour != 0 || minute != 0 || second != 0 {
		t.Fatalf("no time of day may be invented: %02d:%02d:%02d", hour, minute, second)
	}
}

// An unreadable or absent date is not a reason to drop an official story, and
// it is certainly not a reason to invent one: the row keeps a zero date.
func TestLauncherPageLeavesAnUnreadableDateEmpty(t *testing.T) {
	page := `<html><body><div class="news-cont"><ul>
      <li><a href="/news/gamebroad/20991231/900001.html">公告无日期</a></li>
      <li><a href="/news/gamebroad/20991230/900002.html">公告坏日期</a><span>--</span></li>
      <li><a href="/news/gamebroad/20991229/900003.html">公告不可能的日期</a><span>13/45</span></li>
      <li><a href="/news/gamebroad/20991228/900004.html">公告好日期</a><span>12/28</span></li>
    </ul></div></body></html>`
	feed := parseLauncherFixture(t, page)
	if len(feed.Items) != 4 {
		t.Fatalf("all four official rows must survive, got %+v", feed.Items)
	}
	for _, item := range feed.Items {
		switch item.Title {
		case "公告好日期":
			if !item.Date.Equal(time.Date(2099, 12, 28, 0, 0, 0, 0, time.UTC)) {
				t.Fatalf("valid row date lost: %v", item.Date)
			}
		default:
			if !item.Date.IsZero() {
				t.Fatalf("%s must keep an empty date instead of a guessed one: %v", item.Title, item.Date)
			}
			if item.ShortDate() != "" || item.DateText() != "" {
				t.Fatalf("%s must show no date: %q %q", item.Title, item.ShortDate(), item.DateText())
			}
		}
	}
}

// Only rows that name an official article of one of the three channels may
// become entries; anything else is skipped rather than shown as news.
func TestLauncherPageSkipsRowsOutsideTheOfficialArticleShape(t *testing.T) {
	page := `<html><body><div class="news-cont"><ul>
      <li><a href="javascript:alert(1)">脚本链接</a><span>12/31</span></li>
      <li><a href="https://evil.example/news/gamebroad/20991231/900001.html">外部链接</a><span>12/31</span></li>
      <li><a href="/news/gamebroad/../../etc/900001.html">路径穿越</a><span>12/31</span></li>
      <li><a href="/news/gamesomething/20991231/900002.html">未知频道</a><span>12/31</span></li>
      <li><a href="/news/gamebroad/20991231/900003.html"></a><span>12/31</span></li>
      <li><span>12/31</span></li>
      <li><a href="/news/gamebroad/20991231/900004.html">可用公告</a><span>12/31</span></li>
    </ul></div></body></html>`
	feed := parseLauncherFixture(t, page)
	if len(feed.Items) != 1 || feed.Items[0].Title != "可用公告" {
		t.Fatalf("only the official article row may pass: %+v", feed.Items)
	}

	onlyBroken := `<html><body><div class="news-cont"><ul>
      <li><a href="javascript:alert(1)">脚本链接</a><span>12/31</span></li>
      <li><a href="https://evil.example/news/gamebroad/20991231/900001.html">外部链接</a><span>12/31</span></li>
    </ul></div></body></html>`
	if _, err := parseLauncherPage([]byte(onlyBroken), launcherBase, launcherPageHosts); err == nil {
		t.Fatal("a page whose rows are all unusable must be reported, not shown as an empty board")
	}
}

// A launcher page without any news block has changed shape: that is a failure,
// not "the official launcher has no news".
func TestLauncherPageRequiresANewsBlock(t *testing.T) {
	for name, page := range map[string]string{
		"empty-document":  ``,
		"no-news-block":   `<html><body><div class="main-cont"></div></body></html>`,
		"other-list-only": `<html><body><ul class="ewm-list"><li><a href="/news/gamebroad/20991231/900001.html">链接</a><span>12/31</span></li></ul></body></html>`,
	} {
		if _, err := parseLauncherPage([]byte(page), launcherBase, launcherPageHosts); err == nil {
			t.Fatalf("%s must be reported as a changed page", name)
		}
	}
}

// Three empty blocks are an official empty board, not a failure.
func TestLauncherPageKeepsAnEmptyBoardEmpty(t *testing.T) {
	page := `<html><body>
      <div class="news-cont"><ul></ul></div>
      <div class="news-cont"><ul></ul></div>
      <div class="news-cont"><ul></ul></div>
    </body></html>`
	feed, err := parseLauncherPage([]byte(page), launcherBase, launcherPageHosts)
	if err != nil {
		t.Fatalf("an empty official board is not a parse failure: %v", err)
	}
	if len(feed.Items) != 0 || feed.Counts() != [3]uint8{} {
		t.Fatalf("empty board reported rows: %+v", feed.Items)
	}
}

// The same article may be advertised twice; the board must show it once, and
// within a channel the newest row comes first.
func TestLauncherPageDeduplicatesAndOrdersNewestFirst(t *testing.T) {
	page := `<html><body>
      <div class="news-cont"><ul>
        <li><a href="/news/gamebroad/20991104/200002.html">较旧公告</a><span>11/04</span></li>
        <li><a href="/news/gamebroad/20991106/200001.html">较新公告</a><span>11/06</span></li>
      </ul></div>
      <div class="news-cont"><ul>
        <li><a href="/news/gamebroad/20991106/200001.html">较新公告</a><span>11/06</span></li>
      </ul></div>
    </body></html>`
	feed := parseLauncherFixture(t, page)
	if len(feed.Items) != 2 {
		t.Fatalf("a repeated article must appear once: %+v", feed.Items)
	}
	if feed.Items[0].Title != "较新公告" {
		t.Fatalf("newest first expected: %+v", feed.Items)
	}
}

// Nested markup inside a row must not be mistaken for the row's own date span,
// and an anchor nested in a tooltip div must not become the row link.
func TestLauncherPageHandlesNestedMarkupInARow(t *testing.T) {
	page := `<html><body><div class="news-cont"><ul>
      <li>
        <div class="hover-qipao"><div class="inner"><span>12/31</span><a href="/news/gamenews/20990101/999999.html">嵌套链接</a></div></div>
        <a href="/news/gamebroad/20991231/900001.html" class="row">外层公告</a>
        <span class="tag">置顶</span>
        <span>12/31</span>
      </li>
    </ul></div></body></html>`
	feed := parseLauncherFixture(t, page)
	if len(feed.Items) != 1 {
		t.Fatalf("one row expected, got %+v", feed.Items)
	}
	item := feed.Items[0]
	if item.Title != "外层公告" {
		t.Fatalf("the row anchor is the outer one, got %q", item.Title)
	}
	if item.Category != Announcements {
		t.Fatalf("row category must follow the outer link: %+v", item)
	}
	if !item.Date.Equal(time.Date(2099, 12, 31, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("row date lost: %v", item.Date)
	}
}
