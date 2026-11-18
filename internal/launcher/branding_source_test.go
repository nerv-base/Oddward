// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"sync/atomic"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
	"github.com/nerv-base/Oddward/internal/news"
)

type brandingStub struct {
	stubSource
	image       []byte
	sloganCalls atomic.Int32
	version     news.VersionName
}

func (s *brandingStub) SloganAsset(context.Context, news.Slogan) ([]byte, news.Slogan, error) {
	s.sloganCalls.Add(1)
	return s.image, news.Slogan{CoverCode: "cover261105", URL: "https://yh.wanmei.com/images/cover261105/slogan.png", ETag: `"v1"`, MD5: "synthetic"}, nil
}

func (s *brandingStub) VersionName(context.Context) (string, string, string, error) {
	return s.version.Number, s.version.Name, s.version.ArticleURL, nil
}

func brandingTestPNG(t *testing.T, color uint8) []byte {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, 4, 3))
	for i := range picture.Pix {
		picture.Pix[i] = color
	}
	var data bytes.Buffer
	if err := png.Encode(&data, picture); err != nil {
		t.Fatal(err)
	}
	return data.Bytes()
}

func TestShellRefreshesBrandingOffThread(t *testing.T) {
	stub := &brandingStub{
		version: news.VersionName{Number: "1.5", Name: "致电，空想迷数", ArticleURL: "https://yh.wanmei.com/news/gamebroad/20991230/update.html"},
		image:   brandingTestPNG(t, 0x7f),
	}
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	s.Source = stub
	tt := ui.NewTester(s.View, 1280, 720)
	tt.Frame()
	frameUntil(t, tt, func() bool { return s.BrandingVersion.Name == "致电，空想迷数" && s.Slogan != nil })
	if stub.sloganCalls.Load() != 1 {
		t.Fatalf("slogan reads = %d, want one", stub.sloganCalls.Load())
	}
}

func TestLocalSloganWinsOverBrandingRefresh(t *testing.T) {
	stub := &brandingStub{
		version: news.VersionName{Name: "网络版本"},
		image:   brandingTestPNG(t, 0x7f),
	}
	localBytes := brandingTestPNG(t, 0x11)
	local, err := ui.DecodeBitmap(localBytes)
	if err != nil {
		t.Fatal(err)
	}
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	s.Source, s.Slogan, s.SloganLocal = stub, local, true
	tt := ui.NewTester(s.View, 1280, 720)
	tt.Frame()
	frameUntil(t, tt, func() bool { return s.BrandingVersion.Name == "网络版本" })
	if s.Slogan != local {
		t.Fatal("branding refresh replaced an explicit local slogan")
	}
}
