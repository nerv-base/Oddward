// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"context"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nerv-base/Oddward/internal/gacha"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func TestWishBackendLoadsOnUIThreadAndDiscardsStaleArchives(t *testing.T) {
	m := settled(state.Ready)
	m.Page = state.WishAnalysis
	s := NewShell(m)
	defer s.Dispose()
	a := wishArchiveFixture()
	s.WishBackend = wishBackendFixture{archive: a}
	wake := make(chan struct{}, 8)
	s.RequestFrame = func() { wake <- struct{}{} }
	tt := ui.NewTester(s.View, 1280, 720)
	for s.Model.Wish.Phase == state.WishLoading {
		select {
		case <-wake:
			tt.Frame()
		case <-time.After(time.Minute):
			t.Fatal("wish worker did not return")
		}
	}
	if s.Model.Wish.Phase != state.WishReady || s.wish.archive.RoleID != a.RoleID || !tt.HasText("限定卡池") {
		t.Fatalf("archive never reached UI thread: %+v", s.Model.Wish)
	}
	before := s.wish.archive.RoleID
	s.wish.out <- wishArchiveResult{effect: state.Effect{Kind: state.WishLoad, Token: s.Model.Wish.Token - 1}, archive: gacha.Archive{RoleID: "999"}}
	s.syncWish()
	if s.wish.archive.RoleID != before {
		t.Fatal("stale load mutated the UI archive before token filtering")
	}
}

func TestWishLoadAppliesOfficialNamesAndFallsBackToIDs(t *testing.T) {
	catalog := gacha.Catalog{Items: map[string]gacha.Item{"11001": {Kind: gacha.Character, Name: "官方测试角色"}}}
	for _, tc := range []struct {
		name string
		fake wishBackendFixture
		want string
	}{
		{name: "catalog", fake: wishBackendFixture{archive: wishArchiveFixture(), catalog: catalog}, want: "官方测试角色"},
		{name: "catalog error", fake: wishBackendFixture{archive: wishArchiveFixture(), catalogError: gacha.ErrCorrupt}, want: "11001"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := settled(state.Ready)
			m.Page = state.WishAnalysis
			s := NewShell(m)
			defer s.Dispose()
			s.WishBackend = tc.fake
			wake := make(chan struct{}, 8)
			s.RequestFrame = func() { wake <- struct{}{} }
			tt := ui.NewTester(s.View, 1280, 720)
			for s.Model.Wish.Phase == state.WishLoading {
				select {
				case <-wake:
					tt.Frame()
				case <-time.After(time.Minute):
					t.Fatal("wish worker did not return")
				}
			}
			if !tt.HasText(tc.want) {
				t.Fatalf("record name %q missing from the rendered page", tc.want)
			}
			if tc.name == "catalog" && tt.HasText("11001") {
				t.Fatal("a catalogued id must render its official name, not the raw id")
			}
		})
	}
}

func TestWishCorruptBackendOffersConfirmedDeletionWithoutOverwriting(t *testing.T) {
	m := settled(state.Ready)
	m.Page = state.WishAnalysis
	s := NewShell(m)
	defer s.Dispose()
	a := wishArchiveFixture()
	s.WishBackend = wishBackendFixture{archive: a, loadError: gacha.ErrCorrupt}
	wake := make(chan struct{}, 8)
	s.RequestFrame = func() { wake <- struct{}{} }
	tt := ui.NewTester(s.View, 1280, 720)
	for s.Model.Wish.Phase == state.WishLoading {
		select {
		case <-wake:
			tt.Frame()
		case <-time.After(time.Minute):
			t.Fatal("corrupt load did not return")
		}
	}
	if !tt.HasText("本地档案已损坏") {
		t.Fatalf("corruption lost typed state: %+v", s.Model.Wish)
	}
	s.Dispatch(state.Event{Kind: state.WishRequestDelete, WishCount: 0})
	tt.Frame()
	if !tt.HasText("记录条数无法读取") {
		t.Fatal("corrupt archive deletion pretended to know its record count")
	}
	if _, ok := tt.AccessByRole(ui.RoleDialog, "删除本地记录"); !ok {
		t.Fatal("corrupt archive deletion lacks confirmation")
	}
}

func TestWishLossyExportKeepsFormatAcrossDialogResult(t *testing.T) {
	m := settled(state.Ready)
	m.Page = state.WishAnalysis
	m.Wish = state.WishState{RoleID: "100012345", Phase: state.WishReady}
	s := NewShell(m)
	defer s.Dispose()
	s.wish.started = true
	exported := make(chan state.WishFormat, 1)
	s.WishBackend = wishBackendFixture{exported: exported}
	s.PickWishExport = func(ctx context.Context, suggestion string, format state.WishFormat) (string, error) {
		if format != state.WishNTEUID || suggestion != "nteuid-gacha-100012345.json" {
			return "", context.Canceled
		}
		return `C:\fixture\export.json`, nil
	}
	wake := make(chan struct{}, 4)
	s.RequestFrame = func() { wake <- struct{}{} }
	s.Dispatch(state.Event{Kind: state.WishRequestExport, WishFormat: state.WishNTEUID})
	if s.Model.Wish.Confirm.Kind != state.WishConfirmLossy {
		t.Fatal("lossy export skipped confirmation")
	}
	s.Dispatch(state.Event{Kind: state.WishAcceptConfirm})
	select {
	case <-wake:
	case <-time.After(time.Minute):
		t.Fatal("export dialog did not return")
	}
	s.syncWish()
	select {
	case format := <-exported:
		if format != state.WishNTEUID {
			t.Fatalf("dialog result changed export format to %d", format)
		}
	case <-time.After(time.Minute):
		t.Fatal("export backend was not called")
	}
}

func TestWishPickerCancellationAndSuggestedExportNames(t *testing.T) {
	m := settled(state.Ready)
	m.Page = state.WishAnalysis
	s := NewShell(m)
	defer s.Dispose()
	s.wish.started = true
	s.PickWishImport = func(context.Context) (string, error) { return "", nil }
	wake := make(chan struct{}, 2)
	s.RequestFrame = func() { wake <- struct{}{} }
	s.Dispatch(state.Event{Kind: state.WishRequestImport})
	select {
	case <-wake:
	case <-time.After(time.Minute):
		t.Fatal("picker did not return")
	}
	s.syncWish()
	if s.Model.Wish.Phase == state.WishFailed || s.Model.Wish.Phase == state.WishLoading {
		t.Fatalf("cancelled picker must return without failure: %+v", s.Model.Wish)
	}
	if got := wishSuggestedFilename("100", state.WishOddward); got != "oddward-gacha-100.json" {
		t.Fatal(got)
	}
	if got := wishSuggestedFilename("100", state.WishNTEUID); got != "nteuid-gacha-100.json" {
		t.Fatal(got)
	}
}
