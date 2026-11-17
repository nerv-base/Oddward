// SPDX-License-Identifier: GPL-3.0-or-later

// Package news reads the official public announcement feed of the game from
// its public web site. It performs read-only GET requests against a fixed
// allowlist of official HTTPS hosts, never sends credentials, and never
// stores anything it fetches: the feed and the article text live in memory
// only, for as long as the launcher shows them.
package news

import (
	"errors"
	"time"
)

// Category is one of the three public news channels of the official site.
type Category uint8

const (
	// Announcements is 公告 (the site's gamebroad channel).
	Announcements Category = iota
	// Events is 活动 (the site's gameevent channel).
	Events
	// News is 资讯 (the site's gamenews channel).
	News
)

var channelOf = [3]struct{ label, channel string }{
	{"公告", "gamebroad"},
	{"活动", "gameevent"},
	{"资讯", "gamenews"},
}

func (c Category) Valid() bool { return c < 3 }

func (c Category) String() string {
	if !c.Valid() {
		return ""
	}
	return channelOf[c].label
}

// Channel is the official site's own name for the channel.
func (c Category) Channel() string {
	if !c.Valid() {
		return ""
	}
	return channelOf[c].channel
}

// CategoryFor maps an official channel name to the board's category.
func CategoryFor(channel string) (Category, bool) {
	for i, entry := range channelOf {
		if entry.channel == channel {
			return Category(i), true
		}
	}
	return 0, false
}

// Item is one announcement entry of the official feed.
type Item struct {
	Title    string
	URL      string
	Date     time.Time
	Category Category
	Channel  string
}

// ShortDate is the compact date shown on a story row, e.g. "11.06".
func (i Item) ShortDate() string {
	if i.Date.IsZero() {
		return ""
	}
	return i.Date.Format("01.02")
}

// DateText is the full date shown in the reading panel, e.g. "2026.11.06".
func (i Item) DateText() string {
	if i.Date.IsZero() {
		return ""
	}
	return i.Date.Format("2006.01.02")
}

// Banner is one slide of the official launcher's carousel.
type Banner struct {
	Title    string
	URL      string
	ImageURL string
}

// The two upstreams a board can come from. The launcher's own page is the
// first choice; the public site's data file is a declared fallback.
const (
	// SourceLauncher is the official launcher's own news page and carousel.
	SourceLauncher = "launcher"
	// SourceSiteFallback is the public web site feed, used only when the
	// launcher page could not be read at all.
	SourceSiteFallback = "site"
)

// Feed is the official feed as the board shows it: newest first, grouped by
// the site's own channels.
type Feed struct {
	Items []Item
	// Banners is the launcher carousel, in the site's own order. It is
	// independent of Items: losing the carousel never discards stories.
	Banners []Banner
	// Source names the upstream this board was read from.
	Source string
}

// Of returns the items of one category, newest first.
func (f Feed) Of(c Category) []Item {
	var out []Item
	for _, item := range f.Items {
		if item.Category == c {
			out = append(out, item)
		}
	}
	return out
}

// Counts reports how many items each category holds.
func (f Feed) Counts() [3]uint8 {
	var counts [3]uint8
	for _, item := range f.Items {
		if item.Category.Valid() && counts[item.Category] < 255 {
			counts[item.Category]++
		}
	}
	return counts
}

// At returns the item at index within a category.
func (f Feed) At(c Category, index int) (Item, bool) {
	items := f.Of(c)
	if index < 0 || index >= len(items) {
		return Item{}, false
	}
	return items[index], true
}

// Detail is the readable text of one official announcement.
type Detail struct {
	Title      string
	URL        string
	Date       string
	Paragraphs []string
}

var (
	// ErrNoFeedLink means the public home page advertised no data file.
	ErrNoFeedLink = errors.New("news: the official page advertises no announcement data file")
	// ErrFeedFormat means the data file was not the expected object.
	ErrFeedFormat = errors.New("news: the announcement data file is not a recognised object")
	// ErrEntryShape means the data file held no usable entry at all.
	ErrEntryShape = errors.New("news: the announcement data file holds no usable entry")
	// ErrArticleText means the announcement page had no readable text.
	ErrArticleText = errors.New("news: the announcement page has no readable text")
	// ErrLauncherPage means the launcher page was not a recognised news page.
	ErrLauncherPage = errors.New("news: the official launcher page has no news block")
	// ErrBannerScript means the carousel script held no recognisable carousel.
	ErrBannerScript = errors.New("news: the launcher carousel script holds no usable slide")
	// ErrImage means an image URL was refused or its payload was unusable.
	ErrImage = errors.New("news: the official image could not be read")
)
