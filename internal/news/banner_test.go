// SPDX-License-Identifier: GPL-3.0-or-later

package news

import (
	"fmt"
	"strings"
	"testing"
)

// bannerScript mirrors the real carousel script's shape: one JavaScript
// assignment wrapping a JSON object, a trailing comma, a second unrelated
// array (launcherPic) and trailing code. The content is synthetic.
const bannerScript = `var yh_data_data = {"lb1":[
{"title":"幻灯其一","viceTitle":"","bigpic":"https://yhvmg.wmupd.com/rms/yh/one.jpg","viewpic":"","link":"https://site.example/news/gamebroad/20991028/400001.html","mlink":"https://site.example/m/news/gamebroad/20991028/400002.html"},
{"title":"幻灯其二","viceTitle":"","bigpic":"https://yhvmg.wmupd.com/rms/yh/two.png","viewpic":"","link":"/news/gameevent/20991027/400003.html"},
{"title":"幻灯其三","viceTitle":"","bigpic":"https://yhvmg.wmupd.com/rms/yh/three.jpg","viewpic":"","link":"https://site.example/news/gamenews/20990528/400004.html"},
],"launcherPic":[{"title":"","viceTitle":"","bigpic":"https://yhvmg.wmupd.com/webops/yh/pic.png","viewpic":"","link":"https://site.example/news/gamenews/20990809/500001.html"}]};
// trailing code below must be ignored, never executed
var bannerStr = '', len = yh_data_data.lb1.length;
console.log(yh_data_data.lb1.length);`

var (
	bannerPageHosts  = []string{"site.example"}
	bannerImageHosts = []string{"yhvmg.wmupd.com"}
)

func parseBannerFixture(t *testing.T, script string) []Banner {
	t.Helper()
	banners, err := parseBanners([]byte(script), "https://site.example", bannerPageHosts, bannerImageHosts)
	if err != nil {
		t.Fatalf("banner script parse failed: %v", err)
	}
	return banners
}

// The carousel is data, not code: the object is read out of the assignment and
// the trailing comma, the unrelated launcherPic array and the trailing script
// are all handled without evaluating anything.
func TestBannersParseTheCarouselDataWithoutEvaluatingIt(t *testing.T) {
	banners := parseBannerFixture(t, bannerScript)
	if len(banners) != 3 {
		t.Fatalf("the lb1 carousel has three items, got %d: %+v", len(banners), banners)
	}
	first := banners[0]
	if first.Title != "幻灯其一" {
		t.Fatalf("title not mapped: %+v", first)
	}
	if first.URL != "https://site.example/news/gamebroad/20991028/400001.html" {
		t.Fatalf("link not mapped: %+v", first)
	}
	if first.ImageURL != "https://yhvmg.wmupd.com/rms/yh/one.jpg" {
		t.Fatalf("image not mapped: %+v", first)
	}
	// A relative link is resolved against the official origin.
	if banners[1].URL != "https://site.example/news/gameevent/20991027/400003.html" {
		t.Fatalf("relative link not resolved: %+v", banners[1])
	}
	// The carousel order is the site's order and must be preserved.
	if banners[0].Title != "幻灯其一" || banners[1].Title != "幻灯其二" || banners[2].Title != "幻灯其三" {
		t.Fatalf("carousel order must be preserved: %+v", banners)
	}
	for _, banner := range banners {
		if strings.Contains(banner.URL, "/m/") {
			t.Fatalf("the mobile mirror link must not be used: %+v", banner)
		}
		if strings.Contains(banner.URL, "500001") {
			t.Fatalf("launcherPic is not the carousel: %+v", banner)
		}
	}
}

// Minified and commented declarations are still just an assignment; the object
// is located by name and read as data.
func TestBannersAcceptMinifiedAndCommentedDeclarations(t *testing.T) {
	one := `{"title":"%s","bigpic":"https://yhvmg.wmupd.com/a.jpg","link":"https://site.example/news/gamebroad/20991028/400001.html"}`
	for name, script := range map[string]string{
		"no-spaces":       `var a=yh_data_data={"lb1":[` + fmt.Sprintf(one, "甲") + `]};`,
		"window-prefix":   `window.yh_data_data = {"lb1":[` + fmt.Sprintf(one, "甲") + `]}`,
		"leading-comment": "/* generated */\nvar yh_data_data =\n{\"lb1\":[" + fmt.Sprintf(one, "甲") + "]}\n// end",
		"brace-in-title":  `var yh_data_data = {"lb1":[` + fmt.Sprintf(one, "含 } 的标题") + `]};`,
	} {
		banners := parseBannerFixture(t, script)
		if len(banners) != 1 {
			t.Fatalf("%s: one banner expected, got %+v", name, banners)
		}
	}
	title := `var yh_data_data = {"lb1":[` + fmt.Sprintf(one, "含 } 与 \\\" 的标题") + `]};`
	if banners := parseBannerFixture(t, title); banners[0].Title != "含 } 与 \" 的标题" {
		t.Fatalf("a brace inside a string must not end the object: %q", banners[0].Title)
	}
}

func TestBannersRejectUnrecognisedScripts(t *testing.T) {
	for name, script := range map[string]string{
		"empty":         ``,
		"no-assignment": `var other = {"lb1":[]};`,
		"no-object":     `var yh_data_data = 42;`,
		"not-json":      `var yh_data_data = {"lb1":[;`,
		"unterminated":  `var yh_data_data = {"lb1":[{"title":"甲"}`,
		"no-lb1":        `var yh_data_data = {"launcherPic":[]};`,
		"lb1-not-array": `var yh_data_data = {"lb1":"nope"};`,
	} {
		if _, err := parseBanners([]byte(script), "https://site.example", bannerPageHosts, bannerImageHosts); err == nil {
			t.Fatalf("%s must be rejected", name)
		}
	}
}

// A slide is only usable when its image is on the official image host and its
// link stays on the official site.
func TestBannersDropSlidesWithUnsafeLinksOrUnofficialImages(t *testing.T) {
	script := `var yh_data_data = {"lb1":[
      {"title":"脚本链接","bigpic":"https://yhvmg.wmupd.com/a.jpg","link":"javascript:alert(1)"},
      {"title":"外部链接","bigpic":"https://yhvmg.wmupd.com/b.jpg","link":"https://evil.example/news/gamebroad/20991028/400001.html"},
      {"title":"明文图片","bigpic":"http://yhvmg.wmupd.com/c.jpg","link":"https://site.example/news/gamebroad/20991028/400002.html"},
      {"title":"外部图片","bigpic":"https://evil.example/d.jpg","link":"https://site.example/news/gamebroad/20991028/400003.html"},
      {"title":"无图","link":"https://site.example/news/gamebroad/20991028/400004.html"},
      {"title":"可用幻灯","bigpic":"https://yhvmg.wmupd.com/e.jpg","link":"https://site.example/news/gamebroad/20991028/400005.html"}
    ]};`
	banners := parseBannerFixture(t, script)
	if len(banners) != 1 || banners[0].Title != "可用幻灯" {
		t.Fatalf("only the safe slide may pass: %+v", banners)
	}

	onlyUnsafe := `var yh_data_data = {"lb1":[{"title":"外部图片","bigpic":"https://evil.example/d.jpg","link":"https://site.example/news/gamebroad/20991028/400003.html"}]};`
	if _, err := parseBanners([]byte(onlyUnsafe), "https://site.example", bannerPageHosts, bannerImageHosts); err == nil {
		t.Fatal("a carousel whose slides are all unusable must be reported, not shown as an empty carousel")
	}
}

// An officially empty carousel is empty, not broken.
func TestBannersKeepAnEmptyCarouselEmpty(t *testing.T) {
	banners, err := parseBanners([]byte(`var yh_data_data = {"lb1":[],"launcherPic":[{"title":"x","bigpic":"https://yhvmg.wmupd.com/x.jpg","link":"https://site.example/news/gamebroad/20991028/400001.html"}]};`), "https://site.example", bannerPageHosts, bannerImageHosts)
	if err != nil {
		t.Fatalf("an empty carousel is not a failure: %v", err)
	}
	if len(banners) != 0 {
		t.Fatalf("launcherPic must never become the carousel: %+v", banners)
	}
}
