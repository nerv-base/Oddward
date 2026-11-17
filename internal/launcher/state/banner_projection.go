// SPDX-License-Identifier: GPL-3.0-or-later

package state

import "github.com/nerv-base/Oddward/internal/news"

// BannerRow is a bounded, comparable projection. Banner selection is independent
// of article categories; these are the actual launcher slides, not fake stories.
type BannerRow struct {
	Count                uint8
	Titles, URLs, Images [8]string
	Source               string
}

func (r BannerRow) At(index uint8) (title, url, image string, ok bool) {
	if index >= r.Count || index >= 8 {
		return "", "", "", false
	}
	return r.Titles[index], r.URLs[index], r.Images[index], true
}
func ProjectBanners(feed news.Feed) BannerRow {
	r := BannerRow{Source: feed.Source}
	n := len(feed.Banners)
	if n > len(r.Titles) {
		n = len(r.Titles)
	}
	r.Count = uint8(n)
	for i, b := range feed.Banners[:n] {
		r.Titles[i], r.URLs[i], r.Images[i] = b.Title, b.URL, b.ImageURL
	}
	return r
}

const OpenNewsBanner EventKind = 180
