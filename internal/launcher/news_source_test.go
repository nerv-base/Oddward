// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"context"
	"errors"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
	"github.com/nerv-base/Oddward/internal/news"
)

// stubSource stands in for the public site: it answers with a synthetic feed
// so the shell's threading and fencing can be tested without any network.
type stubSource struct {
	feed      news.Feed
	feedErr   error
	detail    news.Detail
	detailErr error
	hold      chan struct{} // when set, reads wait for it (or for cancellation)
	cancelled chan struct{} // closed when a held read is cancelled
	feeds     atomic.Int32
	details   atomic.Int32
}

func (s *stubSource) Feed(ctx context.Context) (news.Feed, error) {
	s.feeds.Add(1)
	if s.hold != nil {
		select {
		case <-s.hold:
		case <-ctx.Done():
			if s.cancelled != nil {
				close(s.cancelled)
			}
			return news.Feed{}, ctx.Err()
		}
	}
	if s.feedErr != nil {
		return news.Feed{}, s.feedErr
	}
	return s.feed, nil
}

func (s *stubSource) Detail(ctx context.Context, url string) (news.Detail, error) {
	s.details.Add(1)
	if s.detailErr != nil {
		return news.Detail{}, s.detailErr
	}
	return s.detail, nil
}

// frameUntil frames until the view settles on cond. It waits on the result,
// never on the clock; the deadline only turns a hang into a failure.
func frameUntil(t *testing.T, tt *ui.Tester, cond func() bool) {
	t.Helper()
	guard := time.Now().Add(5 * time.Second)
	for attempts := 0; attempts < 4000; attempts++ {
		if cond() {
			return
		}
		tt.Frame()
		runtime.Gosched()
		if time.Now().After(guard) {
			break
		}
	}
	if !cond() {
		t.Fatal("the board never reached the expected state")
	}
}

func TestShellReadsTheFeedOffTheThreadAndShowsIt(t *testing.T) {
	stub := &stubSource{feed: officialFeed(), hold: make(chan struct{})}
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	s.Source = stub
	tt := ui.NewTester(s.View, 1280, 720)
	tt.Frame()
	if s.Model.News != state.NewsPending || !tt.HasText("正在获取官方公告…") {
		t.Fatal("the first frame must render the reading state, not block on the network")
	}
	close(stub.hold)
	frameUntil(t, tt, func() bool { return s.Model.News == state.NewsPresent })
	if !tt.HasText("测试服维护公告") {
		t.Fatal("the official announcement must appear once the read returns")
	}
	if stub.feeds.Load() != 1 {
		t.Fatalf("feed reads = %d, want 1", stub.feeds.Load())
	}
}

func TestShellRetriesAFailedRead(t *testing.T) {
	stub := &stubSource{feed: officialFeed(), feedErr: errors.New("网络不可用")}
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	s.Source = stub
	tt := ui.NewTester(s.View, 1280, 720)
	tt.Frame()
	frameUntil(t, tt, func() bool { return s.Model.News == state.NewsUnavailable })
	if !tt.HasText("暂时无法获取官方公告") || s.Model.NewsError != "网络不可用" {
		t.Fatalf("failure not reported: %q %q", s.Model.NewsError, tt.Texts())
	}
	stub.feedErr = nil
	if clickFailed(tt, "重新获取公告") {
		t.Fatal("a failed board must offer a retry")
	}
	frameUntil(t, tt, func() bool { return s.Model.News == state.NewsPresent })
	if !tt.HasText("测试服维护公告") || stub.feeds.Load() != 2 {
		t.Fatalf("retry did not read again: reads=%d", stub.feeds.Load())
	}
}

func TestShellReadsTheAnnouncementBodyOnRequest(t *testing.T) {
	stub := &stubSource{
		feed:   officialFeed(),
		detail: news.Detail{Title: "测试服维护公告", Paragraphs: []string{"第一段正文。", "第二段正文。"}},
	}
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	s.Source = stub
	tt := ui.NewTester(s.View, 1280, 720)
	tt.Frame()
	frameUntil(t, tt, func() bool { return s.Model.News == state.NewsPresent })
	if clickFailed(tt, "测试服维护公告") {
		t.Fatal("a story row must be clickable")
	}
	frameUntil(t, tt, func() bool { return s.Model.Detail == state.NewsPresent })
	if !tt.HasText("第一段正文。") || !tt.HasText("第二段正文。") {
		t.Fatal("the announcement text must reach the panel")
	}
	if stub.details.Load() != 1 {
		t.Fatalf("detail reads = %d, want 1", stub.details.Load())
	}
	if urls := tt.OpenedURLs(); len(urls) != 0 {
		t.Fatalf("the launcher must never open a browser: %v", urls)
	}
}

type snapshotNewsSource struct {
	*stubSource
	cached news.Feed
}

func (s *snapshotNewsSource) CachedFeed(context.Context) (news.Feed, error) {
	return s.cached, nil
}

func TestShellShowsCachedNewsDuringRefreshAndKeepsItOnFailure(t *testing.T) {
	base := &stubSource{feed: officialFeed(), feedErr: errors.New("网络不可用"), hold: make(chan struct{})}
	src := &snapshotNewsSource{stubSource: base, cached: officialFeed()}
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	s.Source = src
	tt := ui.NewTester(s.View, 1280, 720)
	frameUntil(t, tt, func() bool { return tt.HasText("测试服维护公告") })
	close(base.hold)
	frameUntil(t, tt, func() bool { return !s.newsPending })
	if !tt.HasText("测试服维护公告") || !tt.HasText("离线缓存") {
		t.Fatalf("failed refresh erased or mislabeled the cache: %q", tt.Texts())
	}
	base.feedErr = nil
	if err := tt.Click("重新获取公告"); err != nil {
		t.Fatal(err)
	}
	frameUntil(t, tt, func() bool { return base.feeds.Load() == 2 && !s.newsPending })
	if !tt.HasText("测试服维护公告") || tt.HasText("离线缓存") {
		t.Fatalf("successful refresh did not replace the offline status: %q", tt.Texts())
	}
}

func TestDisposeCancelsAnOutstandingRead(t *testing.T) {
	stub := &stubSource{feed: officialFeed(), hold: make(chan struct{}), cancelled: make(chan struct{})}
	s := NewShell(settled(state.Ready))
	s.Source = stub
	tt := ui.NewTester(s.View, 1280, 720)
	tt.Frame()
	s.Dispose()
	select {
	case <-stub.cancelled:
	case <-time.After(5 * time.Second):
		t.Fatal("Dispose must cancel the outstanding official read")
	}
}
