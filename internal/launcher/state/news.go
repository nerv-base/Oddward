// SPDX-License-Identifier: GPL-3.0-or-later

package state

import (
	"strings"

	"github.com/nerv-base/Oddward/internal/news"
)

// newsCategories are the three public channels of the official site, in tab
// order. They are the only news tabs the board has.
var NewsCategories = [3]string{"公告", "活动", "资讯"}

// The board says what it is doing, in every state, and never presents a mock
// story as official news.
const (
	NewsLoadingHeadline = "正在获取官方公告…"
	NewsLoadingDetail   = "读取官网公开公告数据"
	NewsFailedHeadline  = "暂时无法获取官方公告"
	NewsFailedDetail    = "网络或官网暂时不可用，可稍后重试"
	NewsEmptyHeadline   = "官网暂无公告"
	NewsEmptyDetail     = "公告发布后会显示在这里"
)

// projectNews maps the official feed onto the model's comparable projection:
// the three channels, up to three stories each, newest first.
func ProjectNews(feed news.Feed) NewsRow {
	var row NewsRow
	for tab := 0; tab < 3; tab++ {
		items := feed.Of(news.Category(tab))
		count := len(items)
		if count > 3 {
			count = 3
		}
		row.Counts[tab] = uint8(count)
		for index := 0; index < 3 && index < len(items); index++ {
			row.Titles[tab][index] = items[index].Title
			row.Dates[tab][index] = items[index].ShortDate()
			row.URLs[tab][index] = items[index].URL
		}
	}
	return row
}

// briefError trims a fetch failure to the single line the board can show.
func BriefError(err error) string {
	if err == nil {
		return ""
	}
	text := strings.TrimSpace(err.Error())
	if runes := []rune(text); len(runes) > 160 {
		return string(runes[:160]) + "…"
	}
	return text
}
