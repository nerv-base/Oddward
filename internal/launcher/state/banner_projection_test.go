// SPDX-License-Identifier: GPL-3.0-or-later

package state

import (
	"testing"

	"github.com/nerv-base/Oddward/internal/news"
)

func TestOfficialBannerProjectionKeepsActualCountAndOrder(t *testing.T) {
	feed := news.Feed{Source: news.SourceLauncher, Banners: []news.Banner{{Title: "one", URL: "https://yh.wanmei.com/one", ImageURL: "https://yhvmg.wmupd.com/one.jpg"}, {Title: "two", URL: "https://yh.wanmei.com/two", ImageURL: "https://yhvmg.wmupd.com/two.jpg"}}}
	got := ProjectBanners(feed)
	if got.Count != 2 || got.Source != feed.Source || got.Titles[1] != "two" || got.URLs[0] != feed.Banners[0].URL || got.Images[1] != feed.Banners[1].ImageURL {
		t.Fatalf("lost official slides: %+v", got)
	}
	feed.Banners = append(feed.Banners, make([]news.Banner, 10)...)
	if n := ProjectBanners(feed).Count; n != 8 {
		t.Fatalf("projection is not bounded to8: %d", n)
	}
	if ProjectBanners(news.Feed{}).Count != 0 {
		t.Fatal("empty source invented slides")
	}
}
func TestOfficialBannerSelectionRejectsFictitiousSlide(t *testing.T) {
	m := Model{Page: Home, Flow: Ready, News: NewsPresent, NewsBanners: projectedBannerFixture(), NewsIndex: 1}
	got, _ := Reduce(m, Event{Kind: SelectNewsStory, Index: 2})
	if got.NewsIndex != 1 {
		t.Fatal("third slide accepted for a two-slide feed")
	}
	got, _ = Reduce(m, Event{Kind: SelectNewsTab, Index: 2})
	if got.NewsIndex != 1 || got.NewsTab != 2 {
		t.Fatal("article category changed independent banner selection")
	}
}
func TestOfficialBannerOpensOwnArticleNotFirstCategoryRow(t *testing.T) {
	m := Model{Page: Home, Flow: Ready, News: NewsPresent, NewsBanners: projectedBannerFixture(), NewsIndex: 1}
	m.NewsRows.Counts[0] = 1
	m.NewsRows.Titles[0][0] = "different article"
	m.NewsRows.URLs[0][0] = "https://yh.wanmei.com/news/gamebroad/20261106/other.html"
	got, fx := Reduce(m, Event{Kind: OpenNewsBanner})
	if got.Overlay != NewsDetails || got.Detail != NewsPending || got.DetailURL != m.NewsBanners.URLs[1] || got.DetailTitle != m.NewsBanners.Titles[1] || len(fx) != 1 || fx[0].Kind != FetchDetail {
		t.Fatalf("wrong banner target: %+v / %+v", got, fx)
	}
	got, fx = Reduce(got, Event{Kind: DetailRequested})
	if got.DetailURL != m.NewsBanners.URLs[1] || len(fx) != 1 {
		t.Fatal("detail retry lost selected banner target")
	}
	m.NewsBanners = BannerRow{}
	got, fx = Reduce(m, Event{Kind: OpenNewsBanner})
	if got != m || len(fx) != 0 {
		t.Fatal("empty carousel opened fictional content")
	}
}
func TestOfficialFeedLoadedInstallsBannerProjection(t *testing.T) {
	m := Model{News: NewsPending, NewsRequest: 3}
	got, _ := Reduce(m, Event{Kind: NewsLoaded, Token: 3, Banners: projectedBannerFixture()})
	if got.NewsBanners.Count != 2 {
		t.Fatal("UI dropped real feed banners")
	}
}
