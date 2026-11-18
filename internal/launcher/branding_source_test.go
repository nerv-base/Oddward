// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/png"
	"sync/atomic"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
	"github.com/nerv-base/Oddward/internal/news"
)

type brandingStub struct {
	stubSource
	image       []byte
	sloganCalls atomic.Int32
	version     news.VersionName
	sloganErr   error
	versionErr  error
}

func (s *brandingStub) SloganAsset(context.Context, news.Slogan) ([]byte, news.Slogan, error) {
	s.sloganCalls.Add(1)
	if s.sloganErr != nil {
		return nil, news.Slogan{}, s.sloganErr
	}
	return s.image, news.Slogan{CoverCode: "cover261105", URL: "https://yh.wanmei.com/images/cover261105/slogan.png", ETag: `"v1"`, MD5: "synthetic"}, nil
}

func (s *brandingStub) VersionName(context.Context) (string, string, string, error) {
	if s.versionErr != nil {
		return "", "", "", s.versionErr
	}
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

func TestShellRetriesBrandingAfterTransientFailure(t *testing.T) {
	stub := &brandingStub{sloganErr: errors.New("temporary network failure"), versionErr: errors.New("temporary network failure"), image: brandingTestPNG(t, 0x7f)}
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	s.Source = stub
	tt := ui.NewTester(s.View, 1280, 720)
	tt.Frame()
	frameUntil(t, tt, func() bool { return stub.sloganCalls.Load() == 1 && !s.brandingPending && !s.brandingRetryAt.IsZero() })
	stub.sloganErr, stub.versionErr = nil, nil
	s.brandingRetryAt = time.Time{}
	tt.Frame()
	frameUntil(t, tt, func() bool { return stub.sloganCalls.Load() == 2 && s.Slogan != nil })
}
