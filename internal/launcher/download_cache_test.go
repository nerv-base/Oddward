// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"context"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func cacheFixture() state.Model {
	m := downloadFixture(state.DownloadIdle, state.StageIdle)
	m.Download.Current = ""
	m.Download.Version = ""
	m.Download.Kind = state.DownloadInstall
	m.Download.Located = false
	m.Download.Verified = false
	m.Download.Route = state.RouteUnknown
	return m
}

func cachePlanFixture(sourceShort bool) state.Model {
	m := cacheFixture()
	m, _ = state.Reduce(m, state.Event{Kind: state.DownloadCacheSelect})
	m, _ = state.Reduce(m, state.Event{Kind: state.DownloadCacheLocated, Token: m.Download.Request, Path: `E:\SyntheticScratch`})
	m, _ = state.Reduce(m, state.Event{Kind: state.DownloadRequest, Enabled: true})
	needed, free := int64(40<<30), int64(80<<30)
	if sourceShort {
		needed, free = free, needed
	}
	m, _ = state.Reduce(m, state.Event{Kind: state.DownloadPlanned, Token: m.Download.Request, Message: "2.0.0", Body: "cross-volume", Focused: sourceShort, Enabled: sourceShort, Needed: needed, Free: free, Total: 20 << 30})
	return m
}

func TestDownloadCacheShortSourceVolumeHasExplicitBudgetLabel(t *testing.T) {
	m := cachePlanFixture(true)
	s := NewShell(m)
	defer s.Dispose()
	home := ui.NewTester(s.View, 1280, 720)
	if _, ok := home.AccessByRole(ui.RoleButton, "空间不足"); !ok {
		t.Fatalf("a short scratch volume must surface on the home capsule: %q", home.Texts())
	}
	s.Dispatch(state.Event{Kind: state.Navigate, Page: state.LauncherSettings})
	s.selectSettingsSection(state.SettingsNetwork)
	tt := ui.NewTester(s.View, 1280, 1600)
	text := strings.Join(tt.Texts(), "\n")
	for _, want := range []string{"下载暂存卷最低所需：81920.0 MB", "下载暂存卷可用空间：40960.0 MB", "跨卷安装：", "可用空间不足"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing constrained-source presentation %q", want)
		}
	}
	if _, ok := tt.Find("确认并开始"); ok {
		t.Fatal("source-volume shortage allowed confirmation")
	}
}

func TestDownloadCachePickerValidatesBeforePlanningAndShowsCrossVolumeNotice(t *testing.T) {
	m := cacheFixture()
	s := NewShell(m)
	defer s.Dispose()
	calls := make(chan DownloadRequest, 3)
	wake := make(chan struct{}, 4)
	picked := false
	validated := make(chan struct{})
	s.RequestFrame = func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	s.PickGameDirectory = func(context.Context) (string, error) { picked = true; return `E:\SyntheticScratch`, nil }
	s.GameDownloads = downloadBackendFunc(func(ctx context.Context, r DownloadRequest, _ func(state.Event)) state.Event {
		calls <- r
		switch r.Effect.Kind {
		case state.EffectInstallCacheLocate:
			select {
			case <-validated:
			case <-ctx.Done():
			}
			return state.Event{Kind: state.DownloadCacheLocated, Path: r.CacheRoot}
		case state.EffectDownloadPlan:
			return state.Event{Kind: state.DownloadPlanned, Message: "2.0.0", Body: "cross-volume", Needed: 40 << 20, Free: 80 << 20, Total: 20 << 20}
		case state.EffectDownloadRun:
			return state.Event{Kind: state.DownloadStarted, Token: r.Effect.Token}
		}
		return state.Event{}
	})
	s.Dispatch(state.Event{Kind: state.Navigate, Page: state.LauncherSettings})
	s.selectSettingsSection(state.SettingsNetwork)
	tt := ui.NewTester(s.View, 1280, 1600)
	if _, ok := tt.AccessByRole(ui.RoleButton, "选择下载暂存目录"); !ok {
		t.Fatal("fresh install has no optional scratch selector")
	}
	tt.Click("选择下载暂存目录")
	if !picked {
		t.Fatal("native directory picker was not called inline")
	}
	r := receiveDownload(t, calls)
	if r.Effect.Kind != state.EffectInstallCacheLocate || r.Root != s.Model.Managed.Root || r.CacheRoot != `E:\SyntheticScratch` || r.Effect.Path != r.CacheRoot {
		t.Fatalf("scratch path bypassed validation or rebound install root: %+v", r)
	}
	if s.Model.Download.CacheRoot != "" {
		t.Fatal("scratch selection accepted before backend validation")
	}
	close(validated)
	pumpDownloads(t, s, wake, func() bool { return s.Model.Download.CacheRoot == `E:\SyntheticScratch` })
	s.Dispatch(state.Event{Kind: state.DownloadRequest, Enabled: true})
	r = receiveDownload(t, calls)
	if r.Effect.Kind != state.EffectDownloadPlan || r.CacheRoot != `E:\SyntheticScratch` {
		t.Fatalf("fresh plan lost validated scratch location: %+v", r)
	}
	pumpDownloads(t, s, wake, func() bool {
		phase := s.Model.Download.Phase
		return phase == state.DownloadPreparing || phase == state.DownloadRunning
	})
	tt.Frame()
	if _, ok := tt.Find("确认并开始"); ok {
		t.Fatal("cross-volume plan fell back to the retired panel")
	}
	if !s.Model.Download.AwaitingConfirmation {
		t.Fatal("an install plan waits for the dialog that owns the path choice")
	}
	if got := s.Model.Download.CacheRoot; got != `E:\SyntheticScratch` {
		t.Fatalf("plan dropped the validated scratch root: %q", got)
	}
}

func TestDownloadCachePickerCancellationRetainsConfirmationAndDoesNotCallBackend(t *testing.T) {
	m := cacheFixture()
	m, _ = state.Reduce(m, state.Event{Kind: state.DownloadRequest, Enabled: true})
	m, _ = state.Reduce(m, state.Event{Kind: state.DownloadPlanned, Token: m.Download.Request, Message: "2.0.0"})
	s := NewShell(m)
	defer s.Dispose()
	calls := make(chan DownloadRequest, 1)
	s.PickGameDirectory = func(context.Context) (string, error) { return "", nil }
	s.GameDownloads = downloadBackendFunc(func(_ context.Context, r DownloadRequest, _ func(state.Event)) state.Event {
		calls <- r
		return state.Event{}
	})
	s.Dispatch(state.Event{Kind: state.DownloadCacheSelect})
	if !state.PresentDownload(s.Model).CanConfirm || s.Model.Download.Version != "2.0.0" {
		t.Fatal("picker cancellation invalidated existing plan")
	}
	select {
	case <-calls:
		t.Fatal("cancelled picker invoked backend")
	default:
	}
}
