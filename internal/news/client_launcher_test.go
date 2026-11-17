// SPDX-License-Identifier: GPL-3.0-or-later

package news

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

// launcherProbe wires a probe client the way the launcher page and the
// carousel script are reached: the page from the official site, the carousel
// script from the official static-data host, images from the official image
// host. In tests all three are the same loopback server.
func launcherProbe(t *testing.T, handler func(p *probe, w http.ResponseWriter, r *http.Request)) *probe {
	t.Helper()
	var p *probe
	p = newProbe(t, func(w http.ResponseWriter, r *http.Request) {
		if handler == nil {
			http.NotFound(w, r)
			return
		}
		handler(p, w, r)
	})
	p.client.BannerScript = p.server.URL + "/yh-gameSwiper.js"
	return p
}

// carouselScript serves the synthetic carousel fixture from the loopback host.
func (p *probe) carouselScript() string {
	return strings.NewReplacer(
		"https://site.example", p.server.URL,
		"https://yhvmg.wmupd.com", p.server.URL,
	).Replace(bannerScript)
}

// launcherPageBody serves the synthetic launcher fixture from the loopback host.
func (p *probe) launcherPageBody() string {
	return strings.ReplaceAll(launcherPage, "/news/", p.server.URL+"/news/")
}

// The launcher's own news page and carousel are the first choice: one page read,
// one script read, and the site's public data file is never touched.
func TestClientPrefersTheLauncherPageAndItsCarousel(t *testing.T) {
	p := launcherProbe(t, func(p *probe, w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/launcher/launcher_ob2.html":
			if got := r.URL.Query().Get("expand"); got != "0" {
				t.Errorf("the launcher page is read with expand=0, got %q", got)
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(p.launcherPageBody()))
		case "/yh-gameSwiper.js":
			w.Header().Set("Content-Type", "application/javascript")
			w.Write([]byte(p.carouselScript()))
		default:
			http.NotFound(w, r)
		}
	})
	feed, err := p.client.Feed(context.Background())
	if err != nil {
		t.Fatalf("launcher feed failed: %v", err)
	}
	if feed.Source != SourceLauncher {
		t.Fatalf("source = %q, want %q", feed.Source, SourceLauncher)
	}
	if got := feed.Counts(); got != [3]uint8{2, 2, 2} {
		t.Fatalf("counts = %v", got)
	}
	if len(feed.Banners) != 3 {
		t.Fatalf("banners = %+v", feed.Banners)
	}
	if feed.Banners[0].ImageURL != p.server.URL+"/rms/yh/one.jpg" {
		t.Fatalf("banner image not mapped: %+v", feed.Banners[0])
	}
	want := []string{"/launcher/launcher_ob2.html", "/yh-gameSwiper.js"}
	if got := p.requested(); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("requested %v, want %v (the public data file must not be read)", got, want)
	}
}

// The public web site feed is a declared fallback: it is used only when the
// launcher read failed, and the board is told so through Source.
func TestClientFallsBackToThePublicSiteWhenTheLauncherFails(t *testing.T) {
	p := launcherProbe(t, func(p *probe, w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/launcher/launcher_ob2.html":
			http.Error(w, "boom", http.StatusInternalServerError)
		case "/main.html":
			serveFixture(t, w, "main.html", "text/html; charset=utf-8")
		case "/include/newsData20991231.js":
			serveFixture(t, w, "newsdata.js", "application/javascript")
		default:
			http.NotFound(w, r)
		}
	})
	feed, err := p.client.Feed(context.Background())
	if err != nil {
		t.Fatalf("fallback feed failed: %v", err)
	}
	if feed.Source != SourceSiteFallback {
		t.Fatalf("source = %q, want %q", feed.Source, SourceSiteFallback)
	}
	if got := feed.Counts(); got != [3]uint8{2, 1, 1} {
		t.Fatalf("fallback counts = %v", got)
	}
	if len(feed.Banners) != 0 {
		t.Fatalf("the public site feed has no carousel: %+v", feed.Banners)
	}
	want := []string{"/launcher/launcher_ob2.html", "/main.html", "/include/newsData20991231.js"}
	if got := p.requested(); len(got) != len(want) || strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("requested %v, want %v", got, want)
	}
}

// An official launcher page that lists nothing is a valid empty board. It is
// not a failure, so it must not be replaced by the fallback.
func TestClientKeepsAValidEmptyLauncherBoardAndDoesNotFallBack(t *testing.T) {
	empty := `<html><body>
      <div class="news-cont"><ul></ul></div>
      <div class="news-cont"><ul></ul></div>
      <div class="news-cont"><ul></ul></div>
    </body></html>`
	p := launcherProbe(t, func(p *probe, w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/launcher/launcher_ob2.html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(empty))
		case "/yh-gameSwiper.js":
			w.Header().Set("Content-Type", "application/javascript")
			w.Write([]byte(`var yh_data_data = {"lb1":[]};`))
		default:
			http.NotFound(w, r)
		}
	})
	feed, err := p.client.Feed(context.Background())
	if err != nil {
		t.Fatalf("an empty official board is not a failure: %v", err)
	}
	if feed.Source != SourceLauncher || len(feed.Items) != 0 {
		t.Fatalf("empty board mapped wrong: %+v", feed)
	}
	for _, path := range p.requested() {
		if path == "/main.html" {
			t.Fatal("a valid empty board must not fall back to the public site feed")
		}
	}
}

// Cancelling the read is not a reason to start another upstream call.
func TestClientDoesNotFallBackAfterCancellation(t *testing.T) {
	p := launcherProbe(t, func(p *probe, w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/launcher/launcher_ob2.html" {
			<-r.Context().Done()
			return
		}
		serveFixture(t, w, "main.html", "text/html; charset=utf-8")
	})
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	if _, err := p.client.Feed(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want the deadline error, got %v", err)
	}
	for _, path := range p.requested() {
		if path == "/main.html" {
			t.Fatal("a cancelled read must not continue into the fallback")
		}
	}
}

// The carousel is an independent part of the page: losing it must not discard
// readable stories, and Banners reports the failure on its own.
func TestClientKeepsStoriesWhenOnlyTheCarouselFails(t *testing.T) {
	p := launcherProbe(t, func(p *probe, w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/launcher/launcher_ob2.html":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write([]byte(p.launcherPageBody()))
		default:
			http.NotFound(w, r)
		}
	})
	feed, err := p.client.Feed(context.Background())
	if err != nil {
		t.Fatalf("stories must survive a carousel failure: %v", err)
	}
	if feed.Source != SourceLauncher || len(feed.Items) != 6 {
		t.Fatalf("stories lost: %+v", feed)
	}
	if len(feed.Banners) != 0 {
		t.Fatalf("a failed carousel must stay empty, not be invented: %+v", feed.Banners)
	}
	if _, err := p.client.Banners(context.Background()); err == nil {
		t.Fatal("Banners must report the carousel failure")
	}
}

// When both upstreams fail, neither failure may be swallowed: the board has to
// be able to say the launcher page and the fallback both failed.
func TestClientReportsBothUpstreamFailures(t *testing.T) {
	p := launcherProbe(t, func(p *probe, w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	})
	_, err := p.client.Feed(context.Background())
	if err == nil {
		t.Fatal("both upstreams failed; the read must fail")
	}
	text := err.Error()
	if !strings.Contains(text, "500") || !strings.Contains(text, "fallback") {
		t.Fatalf("the error must describe both failures, got %q", text)
	}
	paths := strings.Join(p.requested(), ",")
	if !strings.Contains(paths, "/launcher/launcher_ob2.html") || !strings.Contains(paths, "/main.html") {
		t.Fatalf("both upstreams must have been tried: %v", paths)
	}
}

// The launcher page itself is read over the allowlist: a different host is
// never contacted even if the client is pointed at it.
func TestClientRefusesALauncherPageOffTheOfficialHosts(t *testing.T) {
	p := launcherProbe(t, nil)
	p.client.Base = "https://evil.example"
	if _, err := p.client.Feed(context.Background()); err == nil {
		t.Fatal("a launcher page outside the allowlist must be refused")
	}
	if got := p.hits.Load(); got != 0 {
		t.Fatalf("the refused origin must never be contacted, made %d requests", got)
	}
}
