// SPDX-License-Identifier: GPL-3.0-or-later

package news

import "testing"

// The public data file is JavaScript, and JavaScript allows a trailing comma
// before a closing brace or bracket. The real file uses them, so the parser
// must read the shape the site actually serves.
func TestFeedAcceptsTheScriptTrailingCommas(t *testing.T) {
	script := []byte(`var newsdataObj = {
    "pc" : {
        "news": [
            {"title":"重复的混合条目","url":"/news/gamebroad/20991231/900009.html","time":"2099-12-31","channelCnName":"公告","channelName":"gamebroad"},
        ],
        "gamebroad": [
            {"title":"测试公告甲","url":"/news/gamebroad/20991231/900001.html","time":"2099-12-31","channelCnName":"公告","channelName":"gamebroad"},
            {"title":"带逗号}的标题","url":"/news/gamebroad/20991230/900002.html","time":"2099-12-30","channelCnName":"公告","channelName":"gamebroad"},
        ],
    },
};`)
	feed, err := parseFeed(script, "https://example.invalid")
	if err != nil {
		t.Fatalf("trailing commas must be tolerated: %v", err)
	}
	if got := feed.Counts(); got != [3]uint8{2, 0, 0} {
		t.Fatalf("counts = %v, want 2/0/0", got)
	}
	if feed.Items[0].Title != "测试公告甲" || feed.Items[1].Title != "带逗号}的标题" {
		t.Fatalf("a comma or brace inside a title must survive: %+v", feed.Items)
	}
}
