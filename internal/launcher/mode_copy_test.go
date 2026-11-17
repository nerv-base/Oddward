// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
	"github.com/nerv-base/Oddward/internal/news"
)

func TestNewsFallbackIsExplicitInNativeBoard(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	s.Model.News = state.NewsPresent
	s.Model.NewsBanners.Source = news.SourceSiteFallback
	s.Model.NewsRows = state.ProjectNews(officialFeed())
	tt := ui.NewTester(s.View, 1280, 720)
	tt.Frame()
	if !tt.HasText("官网降级") {
		t.Fatal("website fallback silently looks like the launcher feed")
	}
}
