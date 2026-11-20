// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"errors"
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/nerv-base/Oddward/internal/launcher/state"
	"image/color"
	"testing"
	"time"
)

func TestWishImagesHiddenStatesDoNotQueueAndResume(t *testing.T) {
	for _, hidden := range []string{"overlay", "closed", "cancelled"} {
		t.Run(hidden, func(t *testing.T) {
			m := settled(state.Ready)
			m.Page = state.WishAnalysis
			s := NewShell(m)
			defer s.Dispose()
			s.WishImages = wishImageFixture{err: errors.New("unavailable")}
			switch hidden {
			case "overlay":
				s.Model.Overlay = state.LaunchNotice
			case "closed":
				s.Model.Closed = true
			case "cancelled":
				s.cancel()
			}
			for range 3 {
				s.wishIcon("unknown")
				s.syncWishImages()
				s.wishIcon("unknown")
				if len(s.wishIcons.wanted) != 0 || len(s.wishIcons.queued) != 0 || s.wishIcons.active {
					t.Fatal("hidden page must not requeue icons or request another frame")
				}
			}
			if hidden == "overlay" {
				s.Model.Overlay = state.NoOverlay
				s.wishIcon("unknown")
				if len(s.wishIcons.wanted) != 1 {
					t.Fatal("icons must resume after overlay closes")
				}
			}
		})
	}
}

func TestWishImagesKeepOneWorkerAndDiscardOldGeneration(t *testing.T) {
	m := settled(state.Ready)
	m.Page = state.WishAnalysis
	s := NewShell(m)
	defer s.Dispose()
	wake := make(chan struct{}, 4)
	calls := make(chan string, 4)
	release := make(chan struct{})
	s.RequestFrame = func() { wake <- struct{}{} }
	s.WishImages = wishImageFixture{data: bannerTestImage(t, 16, 16, false, color.RGBA{80, 90, 100, 255}), calls: calls, release: release}
	s.wishIcon("first")
	s.syncWishImages()
	select {
	case <-calls:
	case <-time.After(time.Minute):
		t.Fatal("first request did not start")
	}
	s.wishIcon("second")
	s.Model.Wish.Token++
	s.syncWishImages()
	if len(calls) != 0 {
		t.Fatal("superseded worker must retain its slot until completion")
	}
	select {
	case <-wake:
	case <-time.After(time.Minute):
		t.Fatal("cancelled image worker did not return")
	}
	s.syncWishImages()
	if s.wishIcons.cache["first"] != nil || s.wishIcons.failures["first"].attempts != 0 {
		t.Fatal("stale image must not populate cache or the failure backoff")
	}
	close(release)
}

func TestWishIconDecodeBudgetAndBoundedLRU(t *testing.T) {
	pixels, err := decodeWishIcon(bannerTestImage(t, 400, 200, false, color.RGBA{50, 80, 120, 255}))
	if err != nil {
		t.Fatal(err)
	}
	if pixels.Bounds().Dx() != wishIconDecodeEdge || pixels.Bounds().Dy() != wishIconDecodeEdge/2 {
		t.Fatal("icon decode must preserve aspect ratio and bound output")
	}
	if _, err := decodeWishIcon(make([]byte, 2<<20+1)); err == nil {
		t.Fatal("oversized icon accepted")
	}
	b := wishIconMedia{}
	bitmap := ui.NewBitmap(pixels)
	for i := range wishIconCacheSize + 1 {
		b.remember(fmt.Sprint(i), bitmap)
	}
	if len(b.cache) != wishIconCacheSize || b.cache["0"] != nil {
		t.Fatal("icon bitmap cache is not bounded by LRU capacity")
	}
	b.remember("1", bitmap)
	b.remember("new", bitmap)
	if b.cache["1"] == nil || b.cache["2"] != nil {
		t.Fatal("recent icons must survive LRU eviction")
	}
}

func TestWishImageSourceBuildsBitmapOnlyAfterUIResult(t *testing.T) {
	m := settled(state.Ready)
	m.Page = state.WishAnalysis
	s := NewShell(m)
	defer s.Dispose()
	wake := make(chan struct{}, 4)
	s.RequestFrame = func() { wake <- struct{}{} }
	s.WishImages = wishImageFixture{data: bannerTestImage(t, 32, 16, false, color.RGBA{20, 120, 100, 255})}
	if s.wishIcon("11001") != nil {
		t.Fatal("uncached icon unexpectedly available")
	}
	s.syncWishImages()
	if !s.wishIcons.active {
		t.Fatal("injected wish image source must start a bounded worker")
	}
	select {
	case <-wake:
	case <-time.After(time.Minute):
		t.Fatal("image worker did not return")
	}
	if s.wishIcons.cache["11001"] != nil {
		t.Fatal("worker modified UI bitmap cache")
	}
	s.syncWishImages()
	if s.wishIcon("11001") == nil {
		t.Fatal("UI did not accept decoded icon")
	}
}

func TestWishImageErrorsNeverRetryWithinSession(t *testing.T) {
	m := settled(state.Ready)
	m.Page = state.WishAnalysis
	s := NewShell(m)
	defer s.Dispose()
	wake := make(chan struct{}, 4)
	calls := make(chan string, 4)
	s.RequestFrame = func() { wake <- struct{}{} }
	s.WishImages = wishImageFixture{err: errors.New("image unavailable"), calls: calls}
	s.wishIcon("unknown")
	s.syncWishImages()
	if !s.wishIcons.active {
		t.Fatal("optional image source was not requested")
	}
	select {
	case <-wake:
	case <-time.After(time.Minute):
		t.Fatal("failure did not return")
	}
	s.syncWishImages()
	for range 3 {
		s.wishIcon("unknown")
		s.syncWishImages()
	}
	if len(calls) != 1 || s.wishIcons.active {
		t.Fatal("failed item must remain a silent placeholder without a retry storm")
	}
}
