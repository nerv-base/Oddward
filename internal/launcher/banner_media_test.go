// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
	"github.com/nerv-base/Oddward/internal/news"
)

// All media in these tests is generated in memory; no live URL is requested.
func bannerTestImage(t *testing.T, width, height int, jpegFormat bool, shade color.RGBA) []byte {
	t.Helper()
	picture := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			picture.SetRGBA(x, y, shade)
		}
	}
	var out bytes.Buffer
	var err error
	if jpegFormat {
		err = jpeg.Encode(&out, picture, &jpeg.Options{Quality: 90})
	} else {
		err = png.Encode(&out, picture)
	}
	if err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestBannerMediaDecodeBoundsResidentPixels(t *testing.T) {
	for _, test := range []struct {
		w, h, wantW, wantH int
		jpeg               bool
	}{{24, 12, 24, 12, false}, {2048, 1024, 1024, 512, false}, {1200, 2400, 256, 512, true}} {
		t.Run(fmt.Sprintf("%dx%d/jpeg=%v", test.w, test.h, test.jpeg), func(t *testing.T) {
			data := bannerTestImage(t, test.w, test.h, test.jpeg, color.RGBA{35, 140, 210, 255})
			got, err := decodeBanner(data)
			if err != nil {
				t.Fatalf("valid synthetic artwork rejected: %v", err)
			}
			if got.Bounds().Dx() != test.wantW || got.Bounds().Dy() != test.wantH || got.Stride != test.wantW*4 || len(got.Pix) > 1024*512*4 {
				t.Fatalf("unbounded or distorted resident bitmap: bounds=%v stride=%d pixels=%d", got.Bounds(), got.Stride, len(got.Pix))
			}
		})
	}
}

func TestBannerMediaDecodeRejectsInvalidAndOverBudget(t *testing.T) {
	tooWide := bannerTestImage(t, 4097, 1, false, color.RGBA{20, 40, 60, 255})
	for _, payload := range [][]byte{nil, []byte("not an image"), bytes.Repeat([]byte{0}, (4<<20)+1), tooWide} {
		if bitmap, err := decodeBanner(payload); err == nil || bitmap != nil {
			t.Fatalf("invalid image must fail without a resident bitmap: bitmap=%v error=%v", bitmap != nil, err)
		}
	}
}

type bannerTestReply struct {
	data []byte
	err  error
}

type bannerTestCall struct {
	ctx   context.Context
	url   string
	reply chan bannerTestReply
}

type bannerTestSource struct {
	calls        chan bannerTestCall
	ignoreCancel bool // Only the stale-completion test intentionally delays cancellation.
}

func (*bannerTestSource) Feed(context.Context) (news.Feed, error) { return news.Feed{}, nil }
func (*bannerTestSource) Detail(context.Context, string) (news.Detail, error) {
	return news.Detail{}, nil
}
func (src *bannerTestSource) Image(ctx context.Context, url string) ([]byte, error) {
	call := bannerTestCall{ctx: ctx, url: url, reply: make(chan bannerTestReply, 1)}
	select {
	case src.calls <- call:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if src.ignoreCancel {
		select {
		case reply := <-call.reply:
			return reply.data, reply.err
		case <-time.After(5 * time.Second):
			return nil, errors.New("synthetic test source deadline")
		}
	}
	select {
	case reply := <-call.reply:
		return reply.data, reply.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func newBannerMediaTest(t *testing.T, count uint8, ignoreCancel bool) (*Shell, *bannerTestSource, *ui.Tester, chan struct{}) {
	t.Helper()
	m := settled(state.Ready)
	m.News, m.NewsRequest = state.NewsPresent, 7
	m.NewsBanners.Count = count
	for i := 0; i < 8; i++ {
		m.NewsBanners.Titles[i] = fmt.Sprintf("真实标题 %d", i+1)
		m.NewsBanners.URLs[i] = fmt.Sprintf("https://example.invalid/news/%d", i)
		m.NewsBanners.Images[i] = fmt.Sprintf("https://example.invalid/image/%d.png", i)
	}
	s := NewShell(m)
	t.Cleanup(s.Dispose)
	source := &bannerTestSource{calls: make(chan bannerTestCall, 32), ignoreCancel: ignoreCancel}
	s.Source = source
	wake := make(chan struct{}, 32)
	s.RequestFrame = func() { wake <- struct{}{} }
	tester := ui.NewTester(func(c *ui.Context) {
		c.SetTheme(launcherTheme())
		s.syncBannerMedia()
		ui.Box(c.Key("test-module")).Fill().Background(ui.Hex("#11151A")).Children(func() {
			ui.ButtonBase(c.Key("test-cover")).Label("封面容器").Absolute().Left(0).Top(0).Size(newsWidth, coverHeight).Clip().Children(func() {
				s.bannerArtwork(c)
			})
			s.bannerSelector(c)
		})
	}, int(newsWidth), 180)
	return s, source, tester, wake
}

func awaitBannerCall(t *testing.T, source *bannerTestSource) bannerTestCall {
	t.Helper()
	select {
	case call := <-source.calls:
		return call
	case <-time.After(2 * time.Second):
		t.Fatal("selected banner image was never requested")
		return bannerTestCall{}
	}
}

func awaitBannerWake(t *testing.T, wake <-chan struct{}) {
	t.Helper()
	select {
	case <-wake:
	case <-time.After(2 * time.Second):
		t.Fatal("worker completion did not request a UI frame")
	}
}

func assertBannerPixel(t *testing.T, tt *ui.Tester, want color.RGBA) {
	t.Helper()
	picture := tt.Image()
	for _, p := range []image.Point{{2, 2}, {370, 2}, {2, 139}, {185, 70}} {
		got := color.RGBAModel.Convert(picture.At(p.X, p.Y)).(color.RGBA)
		if got != want {
			t.Fatalf("banner must cover the whole 372x142 cover: pixel %v=%v want %v", p, got, want)
		}
	}
}

func TestBannerMediaLoadsOnlySelectionAndCachesIt(t *testing.T) {
	s, src, tt, wake := newBannerMediaTest(t, 3, false)
	first := awaitBannerCall(t, src)
	if first.url != s.Model.NewsBanners.Images[0] {
		t.Fatalf("requested nonselected image: %s", first.url)
	}
	for i := 0; i < 4; i++ {
		tt.Frame()
	}
	select {
	case extra := <-src.calls:
		t.Fatalf("unselected/pending image was prefetched: %s", extra.url)
	default:
	}
	red := color.RGBA{190, 40, 30, 255}
	first.reply <- bannerTestReply{data: bannerTestImage(t, 24, 12, false, red)}
	awaitBannerWake(t, wake)
	if len(s.bannerMedia.cache) != 0 {
		t.Fatal("worker mutated the UI-owned cache before a frame drained its result")
	}
	tt.Frame()
	assertBannerPixel(t, tt, red)
	if err := tt.Click("官方轮播封面 2"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	second := awaitBannerCall(t, src)
	if s.Model.NewsIndex != 1 || second.url != s.Model.NewsBanners.Images[1] {
		t.Fatalf("selector did not select its real image: index=%d url=%s", s.Model.NewsIndex, second.url)
	}
	blue := color.RGBA{25, 80, 200, 255}
	second.reply <- bannerTestReply{data: bannerTestImage(t, 24, 12, false, blue)}
	awaitBannerWake(t, wake)
	tt.Frame()
	assertBannerPixel(t, tt, blue)
	if err := tt.Click("官方轮播封面 1"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	assertBannerPixel(t, tt, red)
	select {
	case extra := <-src.calls:
		t.Fatalf("valid resident image was downloaded again: %s", extra.url)
	default:
	}
}

func TestBannerMediaSelectorUsesRealCountAndTransparentTargets(t *testing.T) {
	for _, count := range []uint8{0, 1, 2, 5, 8} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			s, _, tt, _ := newBannerMediaTest(t, count, false)
			var tabs []ui.AccessInfo
			for _, item := range tt.AccessAll() {
				if item.Role == ui.RoleTab {
					tabs = append(tabs, item)
				}
			}
			if len(tabs) != int(count) {
				t.Fatalf("selector count=%d, actual feed count=%d", len(tabs), count)
			}
			for i, item := range tabs {
				if item.Bounds.W != 20 || item.Bounds.H != 16 || !item.Focusable || item.Selected != (i == 0) || item.Description != s.Model.NewsBanners.Titles[i] {
					t.Fatalf("lost original 20x16 focus/selection/title contract: %+v", item)
				}
				if item.Bounds.Y != 10 || item.Bounds.X < 10 || item.Bounds.X+item.Bounds.W > coverWidth-10 {
					t.Fatalf("selector must stay in the cover's top-left corner: %+v", item.Bounds)
				}
				pixel := color.RGBAModel.Convert(tt.Image().At(int(item.Bounds.X+2), int(item.Bounds.Y+2))).(color.RGBA)
				if pixel != (color.RGBA{17, 21, 26, 255}) {
					t.Fatalf("Selected() painted a backing plate: %v", pixel)
				}
			}
		})
	}
}

func TestBannerMediaRejectsOldGenerationAndURL(t *testing.T) {
	s, src, tt, wake := newBannerMediaTest(t, 2, true)
	old := awaitBannerCall(t, src)
	s.Model.NewsRequest++
	s.Model.NewsIndex = 1
	s.Model.NewsBanners.Images[1] += "?new-generation"
	tt.Frame()
	select {
	case <-old.ctx.Done():
	default:
		t.Fatal("superseded selection did not cancel its worker context")
	}
	before := s.Model
	old.reply <- bannerTestReply{data: bannerTestImage(t, 16, 8, false, color.RGBA{200, 0, 0, 255})}
	awaitBannerWake(t, wake)
	tt.Frame()
	if s.Model != before || len(s.bannerMedia.cache) != 0 {
		t.Fatal("late old-generation image mutated the Model or cache")
	}
	current := awaitBannerCall(t, src)
	if current.url != s.Model.NewsBanners.Images[1] {
		t.Fatalf("new selection not requested: %s", current.url)
	}
	green := color.RGBA{30, 160, 70, 255}
	current.reply <- bannerTestReply{data: bannerTestImage(t, 16, 8, false, green)}
	awaitBannerWake(t, wake)
	tt.Frame()
	assertBannerPixel(t, tt, green)
}

func TestBannerMediaRejectsQueuedResultsAfterIndependentFences(t *testing.T) {
	for _, fence := range []string{"generation", "url"} {
		t.Run(fence, func(t *testing.T) {
			s, src, tt, wake := newBannerMediaTest(t, 1, false)
			old := awaitBannerCall(t, src)
			old.reply <- bannerTestReply{data: bannerTestImage(t, 12, 6, false, color.RGBA{200, 0, 0, 255})}
			awaitBannerWake(t, wake) // Already queued successfully, before either fence changes.
			if fence == "generation" {
				s.Model.NewsRequest++
			} else {
				s.Model.NewsBanners.Images[0] += "?replacement"
			}
			before := s.Model
			tt.Frame()
			if s.Model != before || len(s.bannerMedia.cache) != 0 {
				t.Fatalf("queued stale %s result was accepted", fence)
			}
			current := awaitBannerCall(t, src)
			if current.url != s.Model.NewsBanners.Images[0] {
				t.Fatalf("wrong current URL: %s", current.url)
			}
			green := color.RGBA{30, 160, 70, 255}
			current.reply <- bannerTestReply{data: bannerTestImage(t, 12, 6, false, green)}
			awaitBannerWake(t, wake)
			tt.Frame()
			assertBannerPixel(t, tt, green)
		})
	}
}

func TestBannerMediaKeepsArtworkButHidesActionsUnderModal(t *testing.T) {
	s, src, tt, wake := newBannerMediaTest(t, 1, false)
	call := awaitBannerCall(t, src)
	shade := color.RGBA{80, 120, 170, 255}
	call.reply <- bannerTestReply{data: bannerTestImage(t, 12, 6, false, shade)}
	awaitBannerWake(t, wake)
	tt.Frame()
	s.Model.Overlay = state.LaunchNotice
	tt.Frame()
	assertBannerPixel(t, tt, shade)
	for _, node := range tt.AccessAll() {
		if node.Role == ui.RoleTab {
			t.Fatal("hidden selector constructed actionable tabs under a modal")
		}
	}
}

func TestBannerMediaNoImageSourceAndHiddenStatesAreQuiet(t *testing.T) {
	for _, scenario := range []string{"empty", "off-home", "pending", "failed", "closed", "no-image-source"} {
		t.Run(scenario, func(t *testing.T) {
			s, src, tt, _ := newBannerMediaTest(t, 0, false)
			s.Model.NewsBanners.Count = 2
			s.Source = &stubSource{} // Feed/Detail only; no optional Image method.
			switch scenario {
			case "empty":
				s.Model.NewsBanners.Count = 0
			case "off-home":
				s.Model.Page = state.WishAnalysis
			case "pending":
				s.Model.News = state.NewsPending
			case "failed":
				s.Model.News = state.NewsUnavailable
			case "closed":
				s.Model.Closed = true
			}
			before := s.Model
			tt.Frame()
			if s.Model != before || s.bannerMedia.activeID != 0 {
				t.Fatal("hidden/missing-image source started work or mutated Model")
			}
			select {
			case call := <-src.calls:
				t.Fatalf("unexpected image call: %s", call.url)
			default:
			}
			if scenario != "no-image-source" {
				for _, node := range tt.AccessAll() {
					if node.Role == ui.RoleTab {
						t.Fatalf("%s constructed a fake selector", scenario)
					}
				}
			}
		})
	}
}

func TestBannerMediaSelectorTracksShrinkingFeedWithinOneView(t *testing.T) {
	s, _, tt, _ := newBannerMediaTest(t, 0, false)
	s.Source = nil
	for _, count := range []uint8{5, 2, 0, 1, 8} {
		s.Model.NewsBanners.Count = count
		tt.Frame()
		tabs := 0
		for _, node := range tt.AccessAll() {
			if node.Role == ui.RoleTab {
				tabs++
			}
		}
		if tabs != int(count) {
			t.Fatalf("same-view selector retained stale controls: count=%d tabs=%d", count, tabs)
		}
	}
}

func TestBannerMediaRefreshDoesNotReusePriorGenerationBitmap(t *testing.T) {
	s, src, tt, wake := newBannerMediaTest(t, 1, false)
	first := awaitBannerCall(t, src)
	first.reply <- bannerTestReply{data: bannerTestImage(t, 12, 6, false, color.RGBA{200, 30, 20, 255})}
	awaitBannerWake(t, wake)
	tt.Frame()
	s.Model.NewsRequest++ // Same URL, a different feed-generation contract.
	tt.Frame()
	if len(s.bannerMedia.cache) != 0 {
		t.Fatal("refresh retained a bitmap authenticated only by an old feed generation")
	}
	assertBannerPixel(t, tt, color.RGBA{17, 21, 26, 255})
	second := awaitBannerCall(t, src)
	if second.url != first.url {
		t.Fatalf("test requires the same URL across generations: %s / %s", first.url, second.url)
	}
	green := color.RGBA{30, 160, 70, 255}
	second.reply <- bannerTestReply{data: bannerTestImage(t, 12, 6, false, green)}
	awaitBannerWake(t, wake)
	tt.Frame()
	assertBannerPixel(t, tt, green)
}

func TestBannerMediaSelectorSupportsKeyboardActivation(t *testing.T) {
	s, _, tt, _ := newBannerMediaTest(t, 0, false)
	s.Source = nil
	s.Model.NewsBanners.Count = 2
	tt.Frame()
	for i := 0; i < 4 && !tt.Focused("官方轮播封面 2"); i++ {
		tt.Key(0, ui.KeyTab)
	}
	if !tt.Focused("官方轮播封面 2") {
		t.Fatal("second real banner is absent from the focus sequence")
	}
	tt.Key(0, ui.KeySpace)
	if s.Model.NewsIndex != 1 {
		t.Fatal("keyboard activation did not select the real banner index")
	}
}

func TestBannerMediaFailureStaysSeparateFromNewsAndUsesFallback(t *testing.T) {
	s, src, tt, wake := newBannerMediaTest(t, 1, false)
	call := awaitBannerCall(t, src)
	fallback := color.RGBA{60, 70, 80, 255}
	picture := image.NewRGBA(image.Rect(0, 0, 20, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 20; x++ {
			picture.SetRGBA(x, y, fallback)
		}
	}
	s.News = ui.NewBitmap(picture)
	before := s.Model
	call.reply <- bannerTestReply{err: errors.New("synthetic artwork failure")}
	awaitBannerWake(t, wake)
	for i := 0; i < 4; i++ {
		tt.Frame()
	}
	if s.Model != before {
		t.Fatal("artwork failure was projected as a news/model failure")
	}
	assertBannerPixel(t, tt, fallback)
	select {
	case extra := <-src.calls:
		t.Fatalf("failed artwork is being retried every frame: %s", extra.url)
	default:
	}
}

func TestBannerMediaCancelsWithoutPostDisposeProjection(t *testing.T) {
	s, src, tt, wake := newBannerMediaTest(t, 1, false)
	call := awaitBannerCall(t, src)
	s.Dispose()
	select {
	case <-call.ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("Dispose did not cancel in-flight artwork")
	}
	before := s.Model
	tt.Frame()
	if s.Model != before || len(s.bannerMedia.cache) != 0 {
		t.Fatal("disposed artwork modified model/cache")
	}
	if len(tt.AccessAll()) > 2 {
		t.Fatal("disposed selector created actionable UI nodes")
	}
	select {
	case <-wake:
		t.Fatal("cancelled worker woke disposed UI")
	default:
	}
}

func TestBannerMediaCacheBoundAndResidentDimensions(t *testing.T) {
	s, src, tt, wake := newBannerMediaTest(t, 1, false)
	large := bannerTestImage(t, 2048, 1024, false, color.RGBA{80, 100, 120, 255})
	for i := 0; i < 10; i++ {
		if i > 0 {
			s.Model.NewsBanners.Images[0] = fmt.Sprintf("https://example.invalid/image/replacement-%d.png", i)
			tt.Frame()
		}
		call := awaitBannerCall(t, src)
		call.reply <- bannerTestReply{data: large}
		awaitBannerWake(t, wake)
		tt.Frame()
		if len(s.bannerMedia.cache) > 8 {
			t.Fatalf("resident cache grew to %d", len(s.bannerMedia.cache))
		}
		for _, bitmap := range s.bannerMedia.cache {
			if bitmap == nil {
				continue
			}
			w, h := bitmap.Size()
			if w > 1024 || h > 512 {
				t.Fatalf("full-size decoder image was retained: %dx%d", w, h)
			}
		}
	}
	if len(s.bannerMedia.cache) != 8 {
		t.Fatalf("bounded cache did not retain recent images: %d", len(s.bannerMedia.cache))
	}
	if _, exists := s.bannerMedia.cache["https://example.invalid/image/0.png"]; exists {
		t.Fatal("least-recently-used image was not evicted")
	}
}
