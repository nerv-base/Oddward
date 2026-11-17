// SPDX-License-Identifier: GPL-3.0-or-later

package news

import (
	"strings"
	"testing"
)

func TestArticleExtractsTitleDateAndReadableParagraphs(t *testing.T) {
	detail, err := parseArticle(fixture(t, "article.html"), "https://example.invalid/news/gamebroad/20991231/900001.html")
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	if detail.Title != "测试公告甲" {
		t.Fatalf("title not mapped: %q", detail.Title)
	}
	if detail.Date != "2099.12.31" {
		t.Fatalf("date not mapped: %q", detail.Date)
	}
	want := []string{
		"亲爱的测试者：",
		"服务器将于 12月31日 02:00-06:00 进行测试维护。",
		"嵌套段落应当在正文内。",
		"第一行 第二行",
		"《异环》项目组",
	}
	if len(detail.Paragraphs) != len(want) {
		t.Fatalf("paragraphs = %q", detail.Paragraphs)
	}
	for i, text := range want {
		if detail.Paragraphs[i] != text {
			t.Fatalf("paragraph %d = %q, want %q", i, detail.Paragraphs[i], text)
		}
	}
	for _, text := range detail.Paragraphs {
		for _, leak := range []string{"<", ">", "&nbsp", "不应出现", "相关推荐"} {
			if strings.Contains(text, leak) {
				t.Fatalf("paragraph leaked markup, scripts or the related-article blurb: %q", text)
			}
		}
	}
	if detail.URL != "https://example.invalid/news/gamebroad/20991231/900001.html" {
		t.Fatalf("article keeps its official URL: %q", detail.URL)
	}
}

func TestArticleRejectsPagesWithoutReadableText(t *testing.T) {
	for name, page := range map[string]string{
		"no-content-block": `<html><body><h1 class="abs articleTitle">标题</h1></body></html>`,
		"empty-content":    `<html><body><div class="articleContent">   <p>&nbsp;</p> </div></body></html>`,
		"script-only":      `<html><body><div class="articleContent"><script>var a = 1;</script></div></body></html>`,
	} {
		if _, err := parseArticle([]byte(page), "https://example.invalid/news/gamebroad/20991231/900001.html"); err == nil {
			t.Fatalf("%s must be reported instead of shown as an empty article", name)
		}
	}
}
