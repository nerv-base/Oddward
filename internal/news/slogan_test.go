// SPDX-License-Identifier: GPL-3.0-or-later

package news

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"sync/atomic"
	"testing"
)

func TestSloganPathFindsOneOfficialCoverReference(t *testing.T) {
	page := []byte(`<img src="https://yh.wanmei.com/images/cover261105/slogan.png"><img src="https://yh.wanmei.com/images/cover261105/slogan.png">`)
	got, err := sloganPath(page)
	if err != nil {
		t.Fatalf("slogan discovery failed: %v", err)
	}
	if got.coverCode != "cover261105" || got.url != "https://yh.wanmei.com/images/cover261105/slogan.png" {
		t.Fatalf("discovered slogan = %+v", got)
	}
}

func TestSloganPathReportsMissingReferenceWithoutGuessing(t *testing.T) {
	_, err := sloganPath([]byte(`<html><body>活动页</body></html>`))
	if !errors.Is(err, ErrNoSloganLink) {
		t.Fatalf("missing slogan link error = %v", err)
	}
}

func TestVersionNameSelectsOnlyNamedVersionUpdate(t *testing.T) {
	data := []byte(`var newsdataObj = {
	  "pc": {"gamebroad": [
	    {"title":"《异环》11月13日不停服更新公告","url":"/news/gamebroad/20991113/hotfix.html","time":"2099-11-13","channelName":"gamebroad"},
	    {"title":"《异环》1.3版本「旧名」更新公告","url":"/news/gamebroad/20991101/old.html","time":"2099-11-01","channelName":"gamebroad"},
	    {"title":"《异环》1.5版本PC端预下载现已开启","url":"/news/gamebroad/20991112/pre.html","time":"2099-11-12","channelName":"gamebroad"},
	    {"title":"《异环》1.5版本「致电，空想迷数」更新公告","url":"/news/gamebroad/20991110/new.html","time":"2099-11-10","channelName":"gamebroad"}
	  ]}
	}
	;`)
	got, err := parseVersionName(data, "https://yh.wanmei.com")
	if err != nil {
		t.Fatalf("version name parse failed: %v", err)
	}
	if got.number != "1.5" || got.name != "致电，空想迷数" || got.articleURL != "https://yh.wanmei.com/news/gamebroad/20991110/new.html" {
		t.Fatalf("version name = %+v", got)
	}
}

func TestVersionNameRequiresNamedVersionUpdate(t *testing.T) {
	data := []byte(`var newsdataObj = {"pc":{"gamebroad":[{"title":"《异环》11月13日不停服更新公告","url":"/news/gamebroad/20991113/hotfix.html","time":"2099-11-13","channelName":"gamebroad"}]}};`)
	_, err := parseVersionName(data, "https://yh.wanmei.com")
	if !errors.Is(err, ErrNoVersionName) {
		t.Fatalf("missing version name error = %v", err)
	}
}

func TestClientSloganReadsAndValidatesOfficialImage(t *testing.T) {
	want := syntheticPNG(t, 11, 7)
	var imageHits atomic.Int32
	p := newProbe(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Write([]byte(`<img src="https://yh.wanmei.com/images/cover261105/slogan.png">`))
		case "/images/cover261105/slogan.png":
			imageHits.Add(1)
			w.Header().Set("ETag", `"slogan-v1"`)
			w.Header().Set("Content-Type", "image/png")
			w.Write(want)
		default:
			http.NotFound(w, r)
		}
	})
	p.client.HTTP = rewriteHTTPClient(p.client.HTTP, p.server.URL)
	p.client.ImageHosts = append(p.client.ImageHosts, "yh.wanmei.com")
	got, err := p.client.Slogan(context.Background())
	if err != nil {
		t.Fatalf("slogan fetch failed: %v", err)
	}
	if got.CoverCode != "cover261105" || got.ETag != `"slogan-v1"` || got.MD5 == "" || got.Bytes != len(want) || got.Width != 11 || got.Height != 7 {
		t.Fatalf("slogan metadata = %+v", got)
	}
	if imageHits.Load() != 1 {
		t.Fatalf("image requests = %d, want one", imageHits.Load())
	}
}

func TestSloganAssetUsesETagAndKeepsPriorOnNotModified(t *testing.T) {
	want := syntheticPNG(t, 5, 3)
	var conditional atomic.Int32
	p := newProbe(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			w.Write([]byte(`<img src="https://yh.wanmei.com/images/cover261105/slogan.png">`))
		case "/images/cover261105/slogan.png":
			if r.Header.Get("If-None-Match") == `"slogan-v1"` {
				conditional.Add(1)
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", `"slogan-v1"`)
			w.Write(want)
		default:
			http.NotFound(w, r)
		}
	})
	p.client.HTTP = rewriteHTTPClient(p.client.HTTP, p.server.URL)
	p.client.ImageHosts = append(p.client.ImageHosts, "yh.wanmei.com")
	image, prior, err := p.client.SloganAsset(context.Background(), Slogan{})
	if err != nil || len(image) == 0 {
		t.Fatalf("initial slogan fetch = %d bytes, %v", len(image), err)
	}
	image, unchanged, err := p.client.SloganAsset(context.Background(), prior)
	if err != nil || image != nil || unchanged.ETag != prior.ETag {
		t.Fatalf("conditional slogan fetch = %d bytes, %+v, %v", len(image), unchanged, err)
	}
	if conditional.Load() != 1 {
		t.Fatalf("conditional requests = %d, want one", conditional.Load())
	}
}

func TestClientVersionNameFollowsDiscoveredDataFile(t *testing.T) {
	p := newProbe(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/main.html":
			w.Write([]byte(`<script src="/include/newsData20991231.js"></script>`))
		case "/include/newsData20991231.js":
			w.Write([]byte(`var newsdataObj = {"pc":{"gamebroad":[{"title":"《异环》1.5版本「致电，空想迷数」更新公告","url":"/news/gamebroad/20991230/update.html","time":"2099-12-30","channelName":"gamebroad"}]}};`))
		default:
			http.NotFound(w, r)
		}
	})
	number, name, articleURL, err := p.client.VersionName(context.Background())
	if err != nil {
		t.Fatalf("version name fetch failed: %v", err)
	}
	if number != "1.5" || name != "致电，空想迷数" || articleURL != p.server.URL+"/news/gamebroad/20991230/update.html" {
		t.Fatalf("version name = %q %q %q", number, name, articleURL)
	}
}

func rewriteHTTPClient(client *http.Client, target string) *http.Client {
	parsed, _ := url.Parse(target)
	base := client.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	return &http.Client{Transport: rewriteTransport{base: base, target: parsed}, Timeout: client.Timeout}
}

type rewriteTransport struct {
	base   http.RoundTripper
	target *url.URL
}

func (t rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	copy.URL.Scheme = t.target.Scheme
	copy.URL.Host = t.target.Host
	copy.Host = t.target.Host
	return t.base.RoundTrip(copy)
}
