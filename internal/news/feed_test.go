// SPDX-License-Identifier: GPL-3.0-or-later

package news

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func readFixture(name string) ([]byte, error) {
	return os.ReadFile(filepath.Join("testdata", name))
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := readFixture(name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

// The data file name is datestamped and changes: it must be discovered from
// the public home page, never guessed or hardcoded.
func TestDataFileIsDiscoveredFromThePublicHomePage(t *testing.T) {
	page := fixture(t, "main.html")
	path, err := dataPath(page)
	if err != nil {
		t.Fatalf("discovery failed: %v", err)
	}
	if path != "/include/newsData20991231.js" {
		t.Fatalf("wrong discovered path %q", path)
	}
	for _, wrong := range []string{"", "newsData20991231.js"} {
		if path == wrong {
			t.Fatalf("path must be an absolute same-origin path, got %q", path)
		}
	}
	if _, err := dataPath([]byte("<html><body>no feed here</body></html>")); err == nil {
		t.Fatal("a page without a news data file must be an error, not a guess")
	}
}

func TestFeedMapsOfficialChannelsCategoriesAndDates(t *testing.T) {
	feed, err := parseFeed(fixture(t, "newsdata.js"), "https://example.invalid")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if got := feed.Counts(); got != [3]uint8{2, 1, 1} {
		t.Fatalf("official channels must map to 公告/活动/资讯 without duplicates: %v", got)
	}
	announcements := feed.Of(Announcements)
	first := announcements[0]
	if first.Title != "测试公告甲" || first.URL != "https://example.invalid/news/gamebroad/20991231/900001.html" {
		t.Fatalf("first announcement mapped wrong: %+v", first)
	}
	if !first.Date.Equal(time.Date(2099, 12, 31, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("published date not parsed: %+v", first.Date)
	}
	if first.Channel != "gamebroad" || first.Category != Announcements {
		t.Fatalf("channel must stay the official one: %+v", first)
	}
	if first.ShortDate() != "12.31" || first.DateText() != "2099.12.31" {
		t.Fatalf("display dates wrong: %q %q", first.ShortDate(), first.DateText())
	}
	if second := announcements[1]; second.Title != "测试公告丁" {
		t.Fatalf("announcements must stay newest first: %+v", announcements)
	}
	for _, item := range feed.Items {
		if strings.Contains(item.Title, "移动端") || strings.HasPrefix(item.URL, "https://example.invalid/m/") {
			t.Fatalf("the mobile mirror must not be merged into the desktop feed: %+v", item)
		}
	}
}

func TestFeedSkipsEntriesOutsideTheOfficialArticleShape(t *testing.T) {
	feed, err := parseFeed(fixture(t, "newsdata.js"), "https://example.invalid")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	for _, item := range feed.Items {
		if strings.Contains(item.Title, "危险链接") || strings.Contains(item.Title, "路径穿越") || strings.Contains(item.Title, "字段缺失") {
			t.Fatalf("unusable entry survived validation: %+v", item)
		}
		if !strings.HasPrefix(item.URL, "https://example.invalid/news/") || !strings.HasSuffix(item.URL, ".html") {
			t.Fatalf("only official article URLs may pass: %+v", item)
		}
	}
	if _, err := parseFeed([]byte(`{"pc":{"gamebroad":[{"title":"x","url":"javascript:alert(1)","time":"2099-12-31","channelName":"gamebroad"}]}}`), "https://example.invalid"); err == nil {
		t.Fatal("a feed of only unusable entries must be reported, not shown as an empty board")
	}
}

func TestFeedRejectsUnrecognisedPayloads(t *testing.T) {
	for name, payload := range map[string]string{
		"not-json":   "var newsdataObj = not json;",
		"no-object":  "var newsdataObj = 42;",
		"no-pc":      `{"m":{"gamebroad":[]}}`,
		"wrong-type": `{"pc":{"gamebroad":"nope"}}`,
	} {
		if _, err := parseFeed([]byte(payload), "https://example.invalid"); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}
}

// An official feed with no entries is a readable empty state, not a failure.
func TestFeedKeepsAnEmptyChannelListAsEmpty(t *testing.T) {
	feed, err := parseFeed([]byte(`var newsdataObj = {"pc":{"news":[],"gamenews":[],"gamebroad":[],"gameevent":[]}};`), "https://example.invalid")
	if err != nil {
		t.Fatalf("an empty feed is not a parse failure: %v", err)
	}
	if got := feed.Counts(); got != [3]uint8{} {
		t.Fatalf("empty feed reported items: %v", got)
	}
	if _, ok := feed.At(Announcements, 0); ok {
		t.Fatal("empty feed must not yield an item")
	}
}
