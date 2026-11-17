// SPDX-License-Identifier: GPL-3.0-or-later

package state

import "github.com/nerv-base/Oddward/internal/news"

func send(m Model, kind EventKind) Model {
	n, _ := Reduce(m, Event{Kind: kind})
	return n
}

func settled(flow Flow) Model { m := Initial(); m.Flow = flow; return m }

func normalFixture(phase ManagedPhase) Model {
	m := Initial()
	m.NormalMode = true
	m.Flow = Ready
	m.Managed = ManagedState{Phase: phase, Request: 8, Root: `C:\fixture\nte`}
	return m
}

func projectedBannerFixture() BannerRow {
	return BannerRow{Count: 2, Titles: [8]string{"启动器推荐一", "启动器推荐二"}, URLs: [8]string{"https://yh.wanmei.com/news/gamebroad/20261028/264788.html", "https://yh.wanmei.com/news/gameevent/20261027/264764.html"}, Images: [8]string{"https://yhvmg.wmupd.com/one.jpg", "https://yhvmg.wmupd.com/two.jpg"}, Source: news.SourceLauncher}
}
