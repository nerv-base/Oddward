// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
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
	logo        []byte
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

func (s *brandingStub) LogoAsset(context.Context) ([]byte, error) { return s.logo, nil }

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

func TestShellShowsOfficialLogoEvenIfVersionNewsFails(t *testing.T) {
	stub := &brandingStub{logo: brandingTestPNG(t, 0x7f), versionErr: errors.New("version news unavailable")}
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	s.Source = stub
	tt := ui.NewTester(s.View, 1280, 720)
	frameUntil(t, tt, func() bool { return !s.brandingRetryAt.IsZero() })
	if _, ok := tt.Find("异环官方标志"); !ok || s.Logo == nil {
		t.Fatal("the official logo must display independently of version news")
	}
}

func TestFetchedLogoPreservesItsOriginalColorsAndAlpha(t *testing.T) {
	picture := image.NewNRGBA(image.Rect(0, 0, 12, 4))
	for y := range 4 {
		for x := range 8 {
			alpha := uint8(255)
			if x >= 4 {
				alpha = 128
			}
			picture.SetNRGBA(x, y, color.NRGBA{R: 10, G: 20, B: 30, A: alpha})
		}
	}
	var data bytes.Buffer
	if err := png.Encode(&data, picture); err != nil {
		t.Fatal(err)
	}
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1280, 720)
	before := tt.Image()
	s.Source = &brandingStub{logo: data.Bytes()}
	frameUntil(t, tt, func() bool { return s.Logo != nil })
	r, ok := tt.Find("异环官方标志")
	if !ok {
		t.Fatal("official logo not rendered")
	}
	y := int(r.Y + r.H/2)
	frame := tt.Image()
	if got, want := frame.RGBAAt(int(r.X+r.W/6), y), (color.RGBA{R: 10, G: 20, B: 30, A: 255}); got != want {
		t.Fatalf("opaque logo should retain its source color, got %v, want %v", got, want)
	}
	x := int(r.X + r.W/2)
	back, half := before.RGBAAt(x, y), frame.RGBAAt(x, y)
	want := (10 + int(back.R)) / 2
	if difference := int(half.R) - want; difference < -3 || difference > 3 {
		t.Fatalf("translucent logo should keep alpha, got %v over %v", half, back)
	}
	x = int(r.X + r.W*5/6)
	if got, want := frame.RGBAAt(x, y), before.RGBAAt(x, y); got != want {
		t.Fatalf("transparent logo pixel should leave background visible, got %v, want %v", got, want)
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
