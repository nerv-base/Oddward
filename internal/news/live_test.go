//go:build live

// SPDX-License-Identifier: GPL-3.0-or-later

// These checks talk to the public official site, read-only. They are opt-in
// (`scripts/go.ps1 test ./internal/news/ -tags live -run Live -v`) because the
// default suite must pass with no network at all.
package news

import (
	"bytes"
	"context"
	"image"
	"testing"
	"time"
)

func liveClient() *Client {
	c := NewClient()
	return c
}

func TestLiveReadsTheOfficialAnnouncementFeed(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	feed, err := liveClient().Feed(ctx)
	if err != nil {
		t.Fatalf("official feed: %v", err)
	}
	counts := feed.Counts()
	t.Logf("official feed: %d announcements / %d activities / %d news", counts[0], counts[1], counts[2])
	if feed.Items[0].Date.After(time.Now()) {
		t.Fatalf("newest announcement is dated in the future: %v", feed.Items[0].Date)
	}
	seen := map[Category]bool{}
	for _, item := range feed.Items {
		seen[item.Category] = true
		t.Logf("%s %s %s %s", item.DateText(), item.Channel, item.Title, item.URL)
		if item.Title == "" || item.URL == "" || item.Channel == "" {
			t.Fatalf("incomplete official entry: %+v", item)
		}
	}
	if !seen[Announcements] {
		t.Fatal("the official feed listed no announcement")
	}
}

func TestLiveReadsOneOfficialAnnouncementPage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client := liveClient()
	feed, err := client.Feed(ctx)
	if err != nil {
		t.Fatalf("official feed: %v", err)
	}
	item, ok := feed.At(Announcements, 0)
	if !ok {
		t.Fatal("no official announcement to read")
	}
	detail, err := client.Detail(ctx, item.URL)
	if err != nil {
		t.Fatalf("official article %s: %v", item.URL, err)
	}
	t.Logf("article %s: title=%q date=%q paragraphs=%d", item.URL, detail.Title, detail.Date, len(detail.Paragraphs))
	if len(detail.Paragraphs) == 0 || detail.Title == "" {
		t.Fatalf("article page produced no readable text: %+v", detail)
	}
	for _, paragraph := range detail.Paragraphs[:min(2, len(detail.Paragraphs))] {
		t.Logf("  %s", paragraph)
	}
}

// The launcher's own page and carousel are the first choice. This test shows
// which upstream actually answered and what the carousel holds; it downloads no
// image and stores nothing.
func TestLiveReadsTheOfficialLauncherNewsAndCarousel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client := liveClient()
	feed, err := client.Feed(ctx)
	if err != nil {
		t.Fatalf("official launcher feed: %v", err)
	}
	t.Logf("source=%s announcements=%d activities=%d news=%d banners=%d",
		feed.Source, feed.Counts()[0], feed.Counts()[1], feed.Counts()[2], len(feed.Banners))
	if feed.Source != SourceLauncher {
		t.Fatalf("the official launcher page is the first choice, got source %q", feed.Source)
	}
	if len(feed.Items) == 0 {
		t.Fatal("the official launcher listed no story at all")
	}
	for _, item := range feed.Items {
		t.Logf("%s %s %s %s", item.ShortDate(), item.Channel, item.Title, item.URL)
		if item.Title == "" || item.URL == "" || item.Channel == "" {
			t.Fatalf("incomplete launcher entry: %+v", item)
		}
	}
	banners, err := client.Banners(ctx)
	if err != nil {
		t.Fatalf("official carousel: %v", err)
	}
	for _, banner := range banners {
		t.Logf("banner %q %s %s", banner.Title, banner.URL, banner.ImageURL)
	}
	if len(banners) == 0 {
		t.Fatal("the official carousel listed no slide")
	}
}

// Exactly one banner image is read, in memory, to prove the image boundary
// against the real host. Nothing is written to disk and nothing is packaged.
// Run it once: `scripts/go.ps1 test ./internal/news/ -tags live -run LiveBannerImage -v`.
func TestLiveReadsOneBannerImageInMemory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	client := liveClient()
	banners, err := client.Banners(ctx)
	if err != nil {
		t.Fatalf("official carousel: %v", err)
	}
	var target string
	for _, banner := range banners {
		if banner.ImageURL != "" {
			target = banner.ImageURL
			break
		}
	}
	if target == "" {
		t.Fatal("the official carousel named no image")
	}
	data, err := client.Image(ctx, target)
	if err != nil {
		t.Fatalf("banner image %s: %v", target, err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("decode read image: %v", err)
	}
	t.Logf("image %s: %s %dx%d %d bytes (in memory only)", target, format, cfg.Width, cfg.Height, len(data))
}
