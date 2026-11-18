// SPDX-License-Identifier: GPL-3.0-or-later

package news

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestCachedClientPersistsLastGoodFeedWithoutHidingUpstreamErrors(t *testing.T) {
	var offline atomic.Bool
	p := launcherProbe(t, func(p *probe, w http.ResponseWriter, r *http.Request) {
		if offline.Load() {
			http.Error(w, "outage", http.StatusServiceUnavailable)
			return
		}
		switch r.URL.Path {
		case "/launcher/launcher_ob2.html":
			fmt.Fprint(w, p.launcherPageBody())
		case "/yh-gameSwiper.js":
			fmt.Fprint(w, p.carouselScript())
		default:
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()
	dir := t.TempDir()
	live, err := NewCachedClient(p.client, dir).Feed(ctx)
	if err != nil || len(live.Items) == 0 || len(live.Banners) == 0 {
		t.Fatalf("live feed = %+v, %v", live, err)
	}

	offline.Store(true)
	reopened := NewCachedClient(p.client, dir)
	before := p.hits.Load()
	cached, err := reopened.CachedFeed(ctx)
	if err != nil || !reflect.DeepEqual(cached, live) {
		t.Fatalf("reopened snapshot = %+v, %v; want %+v", cached, err, live)
	}
	if p.hits.Load() != before {
		t.Fatal("CachedFeed contacted the network")
	}
	if _, err := reopened.Feed(ctx); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("Feed should report the upstream outage, got %v", err)
	}
	cached, err = reopened.CachedFeed(ctx)
	if err != nil || !reflect.DeepEqual(cached, live) {
		t.Fatalf("failed refresh changed the saved snapshot: %+v, %v", cached, err)
	}
}

func TestCachedClientMergesPartialFeedAndKeepsSnapshotOnEmptyRefresh(t *testing.T) {
	var phase atomic.Int32
	p := launcherProbe(t, func(p *probe, w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/launcher/launcher_ob2.html":
			switch phase.Load() {
			case 0:
				fmt.Fprint(w, p.launcherPageBody())
			case 1:
				fmt.Fprint(w, `<div class="news-cont"><ul><li><a href="/news/gamebroad/20261119/revised.html">Updated story</a><span>11/19</span></li></ul></div>`)
			default:
				fmt.Fprint(w, `<div class="news-cont"><ul></ul></div>`)
			}
		case "/yh-gameSwiper.js":
			if phase.Load() == 0 {
				fmt.Fprint(w, p.carouselScript())
				return
			}
			http.Error(w, "script offline", http.StatusServiceUnavailable)
		default:
			http.NotFound(w, r)
		}
	})
	ctx := context.Background()
	dir := t.TempDir()
	initial, err := NewCachedClient(p.client, dir).Feed(ctx)
	if err != nil || len(initial.Banners) == 0 {
		t.Fatalf("initial feed = %+v, %v", initial, err)
	}
	phase.Store(1)
	partial, err := NewCachedClient(p.client, dir).Feed(ctx)
	if err != nil || len(partial.Items) != 1 || partial.Items[0].Title != "Updated story" || !reflect.DeepEqual(partial.Banners, initial.Banners) {
		t.Fatalf("partial refresh should show fresh items with saved banners, got %+v, %v", partial, err)
	}
	phase.Store(2)
	empty, err := NewCachedClient(p.client, dir).Feed(ctx)
	if err != nil || len(empty.Items) != 0 || len(empty.Banners) != 0 {
		t.Fatalf("empty refresh should reach the caller as received, got %+v, %v", empty, err)
	}
	saved, err := NewCachedClient(p.client, dir).CachedFeed(ctx)
	if err != nil || !reflect.DeepEqual(saved, partial) {
		t.Fatalf("snapshot after empty refresh = %+v, %v; want %+v", saved, err, partial)
	}
}

func TestCachedClientRejectsInvalidSnapshots(t *testing.T) {
	const good = `{"Source":"launcher","Banners":[{"Title":"Official","URL":"https://yh.wanmei.com/","ImageURL":"https://yhvmg.wmupd.com/one.png"}]}`
	cases := map[string]string{
		"trailing data":      good + `{}`,
		"too many bytes":     good + strings.Repeat(" ", 512<<10),
		"external image":     strings.Replace(good, "yhvmg.wmupd.com", "elsewhere.example", 1),
		"external link":      strings.Replace(good, "yh.wanmei.com/", "elsewhere.example/", 1),
		"credentialled link": strings.Replace(good, "yh.wanmei.com/", "user:secret@yh.wanmei.com/", 1),
		"relative link":      strings.Replace(good, "https://yh.wanmei.com/", "/", 1),
	}
	for label, data := range cases {
		t.Run(label, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "feed.json")
			c := NewCachedClient(NewClient(), dir)
			if err := os.WriteFile(path, []byte(good), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := c.CachedFeed(context.Background()); err != nil {
				t.Fatalf("fixture must be readable before corruption: %v", err)
			}
			if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := c.CachedFeed(context.Background()); err == nil {
				t.Fatal("invalid snapshot was accepted")
			}
		})
	}
}

func TestCachedClientImageReusesValidatedBytesByURL(t *testing.T) {
	want := syntheticPNG(t, 8, 8)
	var offline atomic.Bool
	p := newProbe(t, func(w http.ResponseWriter, r *http.Request) {
		if offline.Load() {
			http.Error(w, "outage", http.StatusServiceUnavailable)
			return
		}
		w.Write(want)
	})
	ctx := context.Background()
	dir := t.TempDir()
	target := p.server.URL + "/rms/yh/banner.png"
	if got, err := NewCachedClient(p.client, dir).Image(ctx, target); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("online image = %d bytes, %v", len(got), err)
	}
	offline.Store(true)
	reopened := NewCachedClient(p.client, dir)
	before := p.hits.Load()
	if got, err := reopened.Image(ctx, target); err != nil || !bytes.Equal(got, want) {
		t.Fatalf("reopened image = %d bytes, %v; want %d bytes", len(got), err, len(want))
	}
	if p.hits.Load() != before {
		t.Fatal("Image should use the cached URL without reaching the upstream")
	}
	if _, err := reopened.Image(ctx, target+"?version=2"); !errors.Is(err, ErrImage) {
		t.Fatalf("uncached URL should return the upstream error, got %v", err)
	}
	p.client.ImageHosts = nil
	if _, err := reopened.Image(ctx, target); !errors.Is(err, ErrImage) {
		t.Fatalf("image host no longer allowed, got %v", err)
	}
}

func TestCachedClientBoundsImageRetentionAcrossManyDistinctURLs(t *testing.T) {
	png := syntheticPNG(t, 2, 2)
	p := newProbe(t, func(w http.ResponseWriter, r *http.Request) { w.Write(png) })
	dir := t.TempDir()
	ctx := context.Background()
	client := NewCachedClient(p.client, dir)
	for i := range 96 {
		url := fmt.Sprintf("%s/banner-%d.png", p.server.URL, i)
		if _, err := client.Image(ctx, url); err != nil {
			t.Fatalf("image %d: %v", i, err)
		}
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 || len(files) > 32 {
		t.Fatalf("96 distinct images consumed %d cache files, want 1 through 32", len(files))
	}
	before := p.hits.Load()
	if got, err := NewCachedClient(p.client, dir).Image(ctx, p.server.URL+"/banner-95.png"); err != nil || !bytes.Equal(got, png) {
		t.Fatalf("most recently stored image = %d bytes, %v", len(got), err)
	}
	if p.hits.Load() != before {
		t.Fatal("most recently stored image should still be cached")
	}
}

func TestCachedClientImageRejectsTruncatedPixelData(t *testing.T) {
	png := syntheticPNG(t, 8, 8)
	truncated := png[:len(png)-12] // Config can be read even without the IEND chunk.
	p := newProbe(t, func(w http.ResponseWriter, r *http.Request) { w.Write(truncated) })
	if _, err := NewCachedClient(p.client, t.TempDir()).Image(context.Background(), p.server.URL+"/truncated.png"); !errors.Is(err, ErrImage) {
		t.Fatalf("truncated image should fail full decode, got %v", err)
	}
}

func TestCachedClientDetailFallsBackOnlyForAnAllowlistedURL(t *testing.T) {
	var offline atomic.Bool
	p := newProbe(t, func(w http.ResponseWriter, r *http.Request) {
		if offline.Load() {
			http.Error(w, "outage", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `<h1 class="articleTitle">Official notice</h1><div class="articleContent"><p>Read &amp; saved.</p></div>`)
	})
	ctx := context.Background()
	dir := t.TempDir()
	target := p.server.URL + "/news/gamebroad/20261119/notice.html"
	first, err := NewCachedClient(p.client, dir).Detail(ctx, target)
	if err != nil || !reflect.DeepEqual(first.Paragraphs, []string{"Read & saved."}) {
		t.Fatalf("online article = %+v, %v", first, err)
	}

	offline.Store(true)
	reopened := NewCachedClient(p.client, dir)
	before := p.hits.Load()
	if got, err := reopened.Detail(ctx, target); err != nil || !reflect.DeepEqual(got, first) {
		t.Fatalf("offline article = %+v, %v; want %+v", got, err, first)
	}
	if p.hits.Load() != before+1 {
		t.Fatal("Detail should check the upstream before falling back")
	}
	if _, err := reopened.Detail(ctx, target+"?different"); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("uncached URL should return the upstream error, got %v", err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := reopened.Detail(canceled, target); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Detail = %v", err)
	}
	p.client.Hosts = nil
	if _, err := reopened.Detail(ctx, target); err == nil || !strings.Contains(err.Error(), "not an official source") {
		t.Fatalf("cached article whose host is no longer allowed = %v", err)
	}
}
