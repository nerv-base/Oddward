// SPDX-License-Identifier: GPL-3.0-or-later

package news

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"strings"
	"testing"
)

// syntheticPNG returns a real, tiny PNG. Tests never download or ship official
// artwork: every image in this file is generated here.
func syntheticPNG(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	img.Set(0, 0, color.RGBA{R: 0x20, G: 0x40, B: 0x80, A: 0xff})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode fixture png: %v", err)
	}
	return buf.Bytes()
}

func syntheticJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode fixture jpeg: %v", err)
	}
	return buf.Bytes()
}

func TestImageReadsAnOfficialBannerImage(t *testing.T) {
	want := syntheticPNG(t, 8, 8)
	p := newProbe(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rms/yh/banner.png" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Write(want)
	})
	got, err := p.client.Image(context.Background(), p.server.URL+"/rms/yh/banner.png")
	if err != nil {
		t.Fatalf("image read failed: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("image bytes changed in transit: %d vs %d bytes", len(got), len(want))
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, header := range p.auth {
		if header != "|" {
			t.Fatalf("the image reader must never send credentials: %q", header)
		}
	}
}

func TestImageAcceptsJPEG(t *testing.T) {
	want := syntheticJPEG(t)
	p := newProbe(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(want)
	})
	if _, err := p.client.Image(context.Background(), p.server.URL+"/a.jpg"); err != nil {
		t.Fatalf("jpeg must be accepted: %v", err)
	}
}

// The image reader accepts exactly one thing: an HTTPS URL on the official
// image host. Nothing else may reach the network.
func TestImageRefusesEverythingButOfficialHTTPSImages(t *testing.T) {
	p := newProbe(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write(syntheticPNG(t, 2, 2))
	})
	host := strings.TrimPrefix(p.server.URL, "https://")
	for _, target := range []string{
		"",
		"file:///C:/Windows/win.ini",
		"http://" + host + "/a.png",
		"https://evil.example/a.png",
		"https://" + host + ".evil.example/a.png",
		"https://yhvmg.wmupd.com.evil.example/a.png",
		"https://user:pass@" + host + "/a.png",
		"ftp://" + host + "/a.png",
		"/rms/yh/banner.png",
	} {
		if _, err := p.client.Image(context.Background(), target); err == nil {
			t.Fatalf("%q must be refused", target)
		}
	}
	if got := p.hits.Load(); got != 0 {
		t.Fatalf("refused image URLs must never reach the network, made %d requests", got)
	}
}

func TestImageFollowsOnlyAllowlistedRedirects(t *testing.T) {
	want := syntheticPNG(t, 2, 2)
	p := newProbe(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/moved.png":
			http.Redirect(w, r, "/final.png", http.StatusFound)
		case "/final.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write(want)
		case "/away.png":
			http.Redirect(w, r, "https://evil.example/stolen.png", http.StatusFound)
		case "/loop.png":
			http.Redirect(w, r, "/loop.png", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	})
	got, err := p.client.Image(context.Background(), p.server.URL+"/moved.png")
	if err != nil {
		t.Fatalf("an allowlisted redirect must be followed: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("redirected image bytes changed")
	}
	if _, err := p.client.Image(context.Background(), p.server.URL+"/away.png"); err == nil {
		t.Fatal("a redirect off the official image host must be refused")
	}
	if _, err := p.client.Image(context.Background(), p.server.URL+"/loop.png"); err == nil {
		t.Fatal("a redirect loop must be refused")
	}
}

func TestImageCapsSizeAndRejectsNonImages(t *testing.T) {
	p := newProbe(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/big.png":
			w.Header().Set("Content-Type", "image/png")
			blob := bytes.Repeat([]byte("a"), 1<<20)
			for i := 0; i < (maxImage>>20)+1; i++ {
				if _, err := w.Write(blob); err != nil {
					return
				}
			}
		case "/not-image.png":
			w.Header().Set("Content-Type", "image/png")
			w.Write([]byte("<html><body>这不是图片</body></html>"))
		case "/html-image.jpg":
			w.Header().Set("Content-Type", "text/html")
			w.Write([]byte("<html><body>伪装成图片的页面</body></html>"))
		case "/empty.png":
			w.Header().Set("Content-Type", "image/png")
		default:
			http.NotFound(w, r)
		}
	})
	for _, path := range []string{"/big.png", "/not-image.png", "/html-image.jpg", "/empty.png"} {
		if _, err := p.client.Image(context.Background(), p.server.URL+path); err == nil {
			t.Fatalf("%s must be refused", path)
		}
	}
}

// The pixel budget is what stops a small file from expanding into gigabytes of
// bitmap once the UI decodes it.
func TestImageConfigLimits(t *testing.T) {
	cases := map[string]struct {
		config image.Config
		ok     bool
	}{
		"banner":      {image.Config{Width: 1068, Height: 540}, true},
		"wide":        {image.Config{Width: 3840, Height: 2160}, true},
		"zero-width":  {image.Config{Width: 0, Height: 540}, false},
		"zero-height": {image.Config{Width: 1068, Height: 0}, false},
		"negative":    {image.Config{Width: -1, Height: -1}, false},
		"too-wide":    {image.Config{Width: maxImageSide + 1, Height: 16}, false},
		"too-tall":    {image.Config{Width: 16, Height: maxImageSide + 1}, false},
		"too-many":    {image.Config{Width: 8000, Height: 8000}, false},
	}
	for name, tc := range cases {
		data := syntheticPNG(t, 1, 1)
		binary.BigEndian.PutUint32(data[16:20], uint32(tc.config.Width))
		binary.BigEndian.PutUint32(data[20:24], uint32(tc.config.Height))
		binary.BigEndian.PutUint32(data[29:33], crc32.ChecksumIEEE(data[12:29]))
		err := checkImageBytes(data)
		if !tc.ok && !errors.Is(err, ErrImage) {
			t.Fatalf("%s must preserve news.ErrImage: %v", name, err)
		}
		if tc.ok && err != nil {
			t.Fatalf("%s must be accepted: %v", name, err)
		}
		if !tc.ok && err == nil {
			t.Fatalf("%s must be refused", name)
		}
	}
}
