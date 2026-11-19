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

func TestDownloadOfferPollSkipsUnadoptedAndStagedInstallations(t *testing.T) {
	for _, kind := range []string{"unadopted", "waiting-release", "staged", "paused"} {
		t.Run(kind, func(t *testing.T) {
			m := offerFixture()
			switch kind {
			case "unadopted":
				m.Download.Current = ""
				m.Download.Verified = false
				m.Download.Located = false
			case "waiting-release":
				m.Download.Preload = state.PreloadStateWaitingRelease
			case "staged":
				m.Download.Preload = state.PreloadStateStaged
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
