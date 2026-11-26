// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"context"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func offerFixture() state.Model {
	m := downloadFixture(state.DownloadIdle, state.StageIdle)
	m.Download.Verified = true
	return m
}

func TestColdStartChecksInspectedGameAfterRecoveryScan(t *testing.T) {
	m := normalFixture(state.ManagedReady)
	s := NewShell(m)
	defer s.Dispose()
	calls := make(chan DownloadRequest, 4)
	wake := make(chan struct{}, 4)
	s.RequestFrame = func() { wake <- struct{}{} }
	s.GameDownloads = downloadBackendFunc(func(_ context.Context, r DownloadRequest, _ func(state.Event)) state.Event {
		calls <- r
		switch r.Effect.Kind {
		case state.EffectRecoverScan:
			return state.Event{Kind: state.DownloadRecoveryCleared}
		case state.EffectDownloadOfferPoll:
			return state.Event{Kind: state.DownloadOfferPolled, Message: "1.5.8", CurrentVersion: "1.5.6", NeedsAdoption: true}
		case state.EffectInstallAdopt:
			return state.Event{Kind: state.InstallAdopted, Message: "1.5.6"}
		case state.EffectInstallVerify:
			return state.Event{Kind: state.InstallVerified, Message: "1.5.6"}
		default:
			return state.Event{}
		}
	})
	tt := ui.NewTester(s.View, 1280, 720)
	if r := receiveDownload(t, calls); r.Effect.Kind != state.EffectRecoverScan {
		t.Fatalf("startup must restore outstanding work before polling: %+v", r)
	}
	receiveDownload(t, wake)
	tt.Frame()
	if !s.Model.Download.OfferPending {
		t.Fatal("startup postponed first offer check for an existing game")
	}
	if _, ok := tt.AccessByRole(ui.RoleButton, "启动游戏"); !ok {
		t.Fatal("waiting for an offer blocked launch")
	}
	if r := receiveDownload(t, calls); r.Effect.Kind != state.EffectDownloadOfferPoll || r.Root != m.Managed.Root {
		t.Fatalf("startup should only check the selected root: %+v", r)
	}
	receiveDownload(t, wake)
	tt.Frame()
	if _, ok := tt.AccessByRole(ui.RoleButton, "更新游戏"); !ok {
		t.Fatalf("official hotfix missing after first check: %q", tt.Texts())
	}
	tt.Advance(10 * time.Minute)
	if s.Model.Download.OfferPending || s.Model.Download.OfferRequest != 1 {
		t.Fatal("repeated startup poll before minimum interval plus jitter")
	}
	if err := tt.Click("更新游戏"); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []state.EffectKind{state.EffectInstallAdopt, state.EffectInstallVerify, state.EffectDownloadPlan} {
		r := receiveDownload(t, calls)
		if r.Effect.Kind != kind || r.Kind != state.DownloadUpdate || r.Root != m.Managed.Root {
			t.Fatalf("expected update step %v for selected game, got %+v", kind, r)
		}
		if kind != state.EffectDownloadPlan {
			receiveDownload(t, wake)
			tt.Frame()
		}
	}
}

func TestDownloadOfferPollWaitsMinimumIntervalAndPreservesTask(t *testing.T) {
	m := offerFixture()
	s := NewShell(m)
	defer s.Dispose()
	calls := make(chan DownloadRequest, 2)
	wake := make(chan struct{}, 2)
	s.RequestFrame = func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	s.GameDownloads = downloadBackendFunc(func(_ context.Context, r DownloadRequest, report func(state.Event)) state.Event {
		report(state.Event{Kind: state.DownloadSettingsLoaded, DownloadSettings: state.DownloadPreferences{RateLimitKiB: 512}})
		report(state.Event{Kind: state.DownloadPlanned, Path: r.Root, Message: "unexpected-task"})
		calls <- r
		return state.Event{Kind: state.DownloadOfferPolled, Path: r.Root, Message: "2.0.0", Body: "3.0.0", Enabled: true}
	})
	tt := ui.NewTester(s.View, 1280, 720)
	tt.Advance(10 * time.Minute)
	select {
	case r := <-calls:
		t.Fatalf("offer poll ran before minimum interval and jitter: %+v", r)
	default:
	}
	tt.Advance(2 * time.Minute)
	r := receiveDownload(t, calls)
	if r.Effect.Kind != state.EffectDownloadOfferPoll || r.Root != m.Managed.Root {
		t.Fatalf("idle polling performed a task operation: %+v", r)
	}
	receiveDownload(t, wake)
	tt.Frame()
	d := s.Model.Download
	if d.OfferVersion != "2.0.0" || d.PreloadOfferVersion != "3.0.0" {
		t.Fatalf("offer snapshot missing: %+v", d)
	}
	if d.Settings != m.Download.Settings {
		t.Fatal("check-only offer callback changed active preferences")
	}
	if d.Request != m.Download.Request || d.Kind != m.Download.Kind || d.Version != m.Download.Version || d.Phase != m.Download.Phase || d.Preload != m.Download.Preload {
		t.Fatal("offer polling replaced current task or preload state")
	}
	for range 10 {
		tt.Frame()
	}
	tt.Advance(10 * time.Minute)
	select {
	case <-calls:
		t.Fatal("offer polling repeated without minimum interval and positive jitter")
	default:
	}
}

func TestDownloadOfferPollIsSingleFlightAndRejectsMovedTask(t *testing.T) {
	for _, change := range []string{"root", "request", "active"} {
		t.Run(change, func(t *testing.T) {
			m := offerFixture()
			s := NewShell(m)
			defer s.Dispose()
			calls := make(chan DownloadRequest, 3)
			release := make(chan struct{})
			wake := make(chan struct{}, 3)
			s.RequestFrame = func() {
				select {
				case wake <- struct{}{}:
				default:
				}
			}
			s.GameDownloads = downloadBackendFunc(func(ctx context.Context, r DownloadRequest, _ func(state.Event)) state.Event {
				calls <- r
				select {
				case <-release:
				case <-ctx.Done():
				}
				return state.Event{Kind: state.DownloadOfferPolled, Path: r.Root, Message: "stale-latest", Body: "stale-preload", Enabled: true}
			})
			tt := ui.NewTester(s.View, 1280, 720)
			tt.Advance(12 * time.Minute)
			receiveDownload(t, calls)
			tt.Advance(time.Hour)
			select {
			case <-calls:
				t.Fatal("idle config polling overlapped requests")
			default:
			}
			switch change {
			case "root":
				s.Model.Managed.Root = `C:\synthetic\other`
			case "request":
				s.Model.Download.Request++
			case "active":
				s.Model.Download.Phase = state.DownloadRunning
			}
			close(release)
			receiveDownload(t, wake)
			tt.Frame()
			if s.Model.Download.OfferVersion != "" || s.Model.Download.PreloadOfferVersion != "" {
				t.Fatal("moved task accepted stale offer snapshot")
			}
			if s.Model.Download.OfferPending {
				t.Fatal("stale offer result left polling permanently pending")
			}
		})
	}
}

func TestDownloadOfferPollSkipsFreshAndActiveInstallations(t *testing.T) {
	for _, kind := range []string{"fresh", "downloading", "reconciling", "paused"} {
		t.Run(kind, func(t *testing.T) {
			m := offerFixture()
			switch kind {
			case "fresh":
				m.Download.Kind = state.DownloadInstall
				m.Download.Current = ""
				m.Download.Verified = false
				m.Download.Located = false
			case "downloading":
				m.Download.Preload = state.PreloadStateDownloading
			case "reconciling":
				m.Download.Preload = state.PreloadStateReconcile
			case "paused":
				m.Download.Phase = state.DownloadSuspended
			}
			s := NewShell(m)
			defer s.Dispose()
			calls := make(chan DownloadRequest, 1)
			s.GameDownloads = downloadBackendFunc(func(_ context.Context, r DownloadRequest, _ func(state.Event)) state.Event {
				calls <- r
				return state.Event{}
			})
			tt := ui.NewTester(s.View, 1280, 720)
			tt.Advance(time.Hour)
			select {
			case r := <-calls:
				t.Fatalf("ineligible installation was polled: %+v", r)
			default:
			}
		})
	}
}

func TestDownloadOfferPollContinuesWhilePreloadWaitsForRelease(t *testing.T) {
	m := offerFixture()
	m.Download.Kind = state.DownloadPreload
	m.Download.Phase = state.DownloadDone
	m.Download.Preload = state.PreloadStateWaitingRelease
	m.Download.PreloadVersion = "1.5.0"
	s := NewShell(m)
	defer s.Dispose()
	calls := make(chan DownloadRequest, 2)
	wake := make(chan struct{}, 2)
	s.RequestFrame = func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	s.GameDownloads = downloadBackendFunc(func(_ context.Context, r DownloadRequest, _ func(state.Event)) state.Event {
		calls <- r
		return state.Event{Kind: state.DownloadOfferPolled, Path: r.Root, Message: "1.5.3"}
	})
	tt := ui.NewTester(s.View, 1280, 720)
	tt.Advance(12 * time.Minute)
	r := receiveDownload(t, calls)
	if r.Effect.Kind != state.EffectDownloadOfferPoll {
		t.Fatalf("expected offer poll in waiting-release, got %+v", r)
	}
	receiveDownload(t, wake)
	tt.Frame()
	if s.Model.Download.OfferVersion != "1.5.3" || s.Model.Download.Preload != state.PreloadStateWaitingRelease {
		t.Fatalf("formal release not surfaced while preload waiting: %+v", s.Model.Download)
	}
	if _, ok := tt.AccessByRole(ui.RoleButton, "更新游戏"); !ok {
		t.Fatalf("primary button did not switch to formal update: texts=%q", tt.Texts())
	}
}
