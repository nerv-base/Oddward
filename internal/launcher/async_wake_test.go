// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"context"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/launcher/state"
	"github.com/nerv-base/Oddward/internal/news"
)

type wakeSource struct{}

func (wakeSource) Feed(context.Context) (news.Feed, error) { return news.Feed{}, nil }
func (wakeSource) Detail(context.Context, string) (news.Detail, error) {
	return news.Detail{Paragraphs: []string{"真实来源适配器的合成正文"}}, nil
}

// An idle native window has no reason to build another frame. A channel send
// alone is not a wake-up; the former frameUntil tests manually drew frames and
// therefore could not detect a board stuck forever in its loading state.
func TestNewsWorkersWakeAnIdleNativeWindow(t *testing.T) {
	for _, detail := range []bool{false, true} {
		name := "feed"
		if detail {
			name = "detail"
		}
		t.Run(name, func(t *testing.T) {
			s := NewShell(state.Model{Flow: state.Ready})
			defer s.Dispose()
			s.Source = wakeSource{}
			wake := make(chan struct{}, 1)
			s.RequestFrame = func() { wake <- struct{}{} }
			if detail {
				s.Model.DetailURL = "https://yh.wanmei.com/news/gamebroad/20261106/264931.html"
				s.beginDetail(1)
			} else {
				s.beginNews(1)
			}
			select {
			case <-wake:
				if detail && len(s.detailOut) != 1 || !detail && len(s.newsOut) != 1 {
					t.Fatal("window was woken before its UI-thread result was queued")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("completed background read never requested a native frame")
			}
		})
	}
}
