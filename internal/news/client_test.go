// SPDX-License-Identifier: GPL-3.0-or-later

package news

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// probe is a local stand-in for the official public site. Tests drive it over
// TLS on loopback so the client's HTTPS-only rule exercises real code.
type probe struct {
	t      *testing.T
	server *httptest.Server
	client *Client
	hits   atomic.Int64
	mu     sync.Mutex
	paths  []string
	auth   []string
}

func newProbe(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) *probe {
	t.Helper()
	p := &probe{t: t}
	p.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.hits.Add(1)
		p.mu.Lock()
		p.paths = append(p.paths, r.URL.Path)
		p.auth = append(p.auth, r.Header.Get("Authorization")+"|"+r.Header.Get("Cookie"))
		p.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(p.server.Close)
	host := strings.TrimPrefix(p.server.URL, "https://")
	p.client = &Client{
		HTTP:   p.server.Client(),
		Base:   p.server.URL,
		Hosts:  []string{host},
		MinGap: 0,
		// The launcher's own data files and banner images live on other
		// official hosts; tests point every allowlist at the same loopback
		// server and verify the allowlists separately.
		DataHosts:  []string{host},
		ImageHosts: []string{host},
	}
	return p
}

func (p *probe) requested() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.paths...)
}

func serveFixture(t *testing.T, w http.ResponseWriter, name, contentType string) {
	t.Helper()
	data, err := readFixture(name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(data); err != nil {
		t.Fatalf("write response: %v", err)
	}
}

// The public site chain is still the declared fallback: it reads its own home
// page, follows the data file that page advertises and maps the entries. It is
// exercised on its own here so the discovery logic stays covered independently
// of the launcher-first path.
func TestPublicSiteChainFollowsTheDiscoveredDataFile(t *testing.T) {
	p := newProbe(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/main.html":
			serveFixture(t, w, "main.html", "text/html; charset=utf-8")
		case "/include/newsData20991231.js":
			serveFixture(t, w, "newsdata.js", "application/javascript")
		default:
			http.NotFound(w, r)
		}
	})
	feed, err := p.client.siteFeed(context.Background())
	if err != nil {
		t.Fatalf("feed failed: %v", err)
	}
	if got := feed.Counts(); got != [3]uint8{2, 1, 1} {
		t.Fatalf("feed counts = %v", got)
	}
	if feed.Source != SourceSiteFallback {
		t.Fatalf("the public site chain must declare itself, got %q", feed.Source)
	}
	want := []string{"/main.html", "/include/newsData20991231.js"}
	if got := p.requested(); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("requested %v, want %v", got, want)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, header := range p.auth {
		if header != "|" {
			t.Fatalf("the client must never send credentials: %q", header)
		}
	}
}

func TestClientDetailReadsOnlyTheOfficialArticleURL(t *testing.T) {
	p := newProbe(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/news/gamebroad/20991231/900001.html" {
			http.NotFound(w, r)
			return
		}
		serveFixture(t, w, "article.html", "text/html; charset=utf-8")
	})
	detail, err := p.client.Detail(context.Background(), p.server.URL+"/news/gamebroad/20991231/900001.html")
	if err != nil {
		t.Fatalf("detail failed: %v", err)
	}
	if detail.Title != "测试公告甲" || len(detail.Paragraphs) == 0 {
		t.Fatalf("detail not mapped: %+v", detail)
	}
}

func TestClientRefusesURLsOutsideTheOfficialHosts(t *testing.T) {
	p := newProbe(t, func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	for _, target := range []string{
		"http://example.invalid/news/gamebroad/20991231/900001.html",
		"https://evil.example/news/gamebroad/20991231/900001.html",
		"https://example.invalid.attacker.example/news/gamebroad/20991231/900001.html",
		"file:///etc/passwd",
		"/news/gamebroad/20991231/900001.html",
		"",
	} {
		if _, err := p.client.Detail(context.Background(), target); err == nil {
			t.Fatalf("%q must be refused", target)
		}
	}
	if got := p.hits.Load(); got != 0 {
		t.Fatalf("refused URLs must never reach the network, made %d requests", got)
	}
}

func TestClientReportsStatusCodesAndBrokenPayloads(t *testing.T) {
	cases := map[string]func(w http.ResponseWriter, r *http.Request){
		"status-500": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "boom", http.StatusInternalServerError)
		},
		"missing-feed": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/main.html" {
				w.Header().Set("Content-Type", "text/html")
				w.Write([]byte("<html><body>没有数据文件</body></html>"))
				return
			}
			http.NotFound(w, r)
		},
		"broken-json": func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/main.html" {
				w.Header().Set("Content-Type", "text/html")
				w.Write([]byte(`<script src="/include/newsData20991231.js"></script>`))
				return
			}
			w.Header().Set("Content-Type", "application/javascript")
			w.Write([]byte("var newsdataObj = {{{;"))
		},
		"redirect-loop": func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, r.URL.Path, http.StatusFound)
		},
	}
	for name, handler := range cases {
		t.Run(name, func(t *testing.T) {
			p := newProbe(t, handler)
			if _, err := p.client.Feed(context.Background()); err == nil {
				t.Fatalf("%s must fail", name)
			}
		})
	}
}

func TestClientCapsResponseSize(t *testing.T) {
	p := newProbe(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		blob := strings.Repeat("a", 4<<10)
		for i := 0; i < 64; i++ {
			if _, err := w.Write([]byte(blob)); err != nil {
				return
			}
		}
	})
	if _, err := p.client.get(context.Background(), p.server.URL+"/main.html", maxMainHTML); err == nil {
		t.Fatal("an oversized response must be refused instead of buffered")
	}
}

func TestClientStopsWhenTheContextEnds(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	p := newProbe(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := p.client.Feed(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want deadline error, got %v", err)
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("a cancelled request must return at once, took %s", elapsed)
	}
}
