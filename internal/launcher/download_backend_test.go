// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"context"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

type downloadBackendFunc func(context.Context, DownloadRequest, func(state.Event)) state.Event

func (fn downloadBackendFunc) HandleDownload(ctx context.Context, r DownloadRequest, report func(state.Event)) state.Event {
	return fn(ctx, r, report)
}

func receiveDownload[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("synthetic download worker did not respond")
		var zero T
		return zero
	}
}

func pumpDownloads(t *testing.T, s *Shell, wake <-chan struct{}, until func() bool) {
	t.Helper()
	deadline := time.NewTimer(3 * time.Second)
	defer deadline.Stop()
	for !until() {
		s.syncDownloads()
		if until() {
			return
		}
		select {
		case <-wake:
		case <-deadline.C:
			t.Fatal("download state did not settle")
		}
	}
}

func TestDownloadReportsPreserveReliableRouteAndResult(t *testing.T) {
	s := NewShell(normalFixture(state.ManagedReady))
	defer s.Dispose()
	wake := make(chan struct{}, 1)
	s.RequestFrame = func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	s.GameDownloads = downloadBackendFunc(func(ctx context.Context, r DownloadRequest, report func(state.Event)) state.Event {
		for range 100 {
			report(state.Event{Kind: state.DownloadProgress, Downloaded: 1, Total: 100, Stage: state.StageNetwork})
		}
		report(state.Event{Kind: state.DownloadRoutePlanned, Index: uint8(state.RoutePatch), Total: 100, PatchBytes: 70, Reused: 200})
		return state.Event{Kind: state.DownloadPlanned, Message: "1.1", Total: 100, Reused: 200, FilesTotal: 3}
	})
	s.Dispatch(state.Event{Kind: state.DownloadRequest})
	pumpDownloads(t, s, wake, func() bool { return s.Model.Download.AwaitingConfirmation })
	if s.Model.Download.Route != state.RoutePatch || s.Model.Download.PatchBytes != 70 || s.Model.Download.Version != "1.1" {
		t.Fatalf("reliable route or result dropped: %+v", s.Model.Download)
	}
}

func TestDownloadCancelWaitsForWorkerAndSuppressesLateApply(t *testing.T) {
	s := NewShell(downloadFixture(state.DownloadRunning, state.StageNetwork))
	s.Model.Overlay = state.NoOverlay
	defer s.Dispose()
	started, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	cleanup := make(chan DownloadRequest, 1)
	wake := make(chan struct{}, 1)
	s.RequestFrame = func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	s.GameDownloads = downloadBackendFunc(func(ctx context.Context, r DownloadRequest, report func(state.Event)) state.Event {
		if r.Effect.Kind == state.EffectDownloadCancel {
			cleanup <- r
			return state.Event{Kind: state.DownloadCancelled}
		}
		if r.Effect.Kind != state.EffectDownloadRun {
			t.Errorf("late result started effect %v", r.Effect.Kind)
			return state.Event{Kind: state.DownloadFailed}
		}
		close(started)
		<-ctx.Done()
		close(cancelled)
		<-release
		report(state.Event{Kind: state.ApplyStarted, FilesTotal: 2})
		return state.Event{Kind: state.DownloadVerified}
	})
	s.apply([]state.Effect{{Kind: state.EffectDownloadRun, Token: s.Model.Download.Request, Path: s.Model.Managed.Root}})
	receiveDownload(t, started)
	s.Dispatch(state.Event{Kind: state.DownloadCancel})
	receiveDownload(t, cancelled)
	select {
	case <-cleanup:
		t.Fatal("cleanup raced an active worker")
	default:
	}
	if s.Model.Download.Phase == state.DownloadIdle {
		t.Fatal("cancelled displayed before backend acknowledgement")
	}
	close(release)
	request := receiveDownload(t, cleanup)
	if request.Effect.Token != s.Model.Download.Request || request.Root != s.Model.Managed.Root {
		t.Fatal("cleanup lost task identity")
	}
	pumpDownloads(t, s, wake, func() bool { return s.Model.Download.Phase == state.DownloadIdle })
	if s.Model.Download.Current != "1.0.0" {
		t.Fatal("late worker result applied after cancellation")
	}
}

func TestDownloadCancelUnblocksSaturatedReliableDelivery(t *testing.T) {
	s := NewShell(downloadFixture(state.DownloadRunning, state.StageNetwork))
	defer s.Dispose()
	saturated := make(chan struct{})
	cleanup := make(chan struct{}, 1)
	wake := make(chan struct{}, 1)
	s.RequestFrame = func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	s.GameDownloads = downloadBackendFunc(func(ctx context.Context, r DownloadRequest, report func(state.Event)) state.Event {
		if r.Effect.Kind == state.EffectDownloadCancel {
			cleanup <- struct{}{}
			return state.Event{Kind: state.DownloadCancelled}
		}
		for i := 0; i < 17; i++ {
			if i == 16 {
				close(saturated)
			}
			report(state.Event{Kind: state.DownloadRoutePlanned, Index: uint8(state.RoutePatch)})
		}
		<-ctx.Done()
		return state.Event{Kind: state.DownloadVerified}
	})
	s.apply([]state.Effect{{Kind: state.EffectDownloadRun, Token: s.Model.Download.Request}})
	receiveDownload(t, saturated)
	s.Dispatch(state.Event{Kind: state.DownloadCancel})
	receiveDownload(t, cleanup)
	pumpDownloads(t, s, wake, func() bool { return s.Model.Download.Phase == state.DownloadIdle })
}

func TestDownloadCancelStopsPriorWorkerBeforeQueuedStageCleanup(t *testing.T) {
	s := NewShell(downloadFixture(state.DownloadRebuilding, state.StageApply))
	defer s.Dispose()
	reported, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	cleanup := make(chan struct{}, 1)
	wake := make(chan struct{}, 1)
	s.RequestFrame = func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	s.GameDownloads = downloadBackendFunc(func(ctx context.Context, r DownloadRequest, report func(state.Event)) state.Event {
		switch r.Effect.Kind {
		case state.EffectDownloadRun:
			report(state.Event{Kind: state.ApplyFinished, FilesDone: 2})
			close(reported)
			<-ctx.Done()
			close(cancelled)
			<-release
			return state.Event{}
		case state.EffectDownloadCancel:
			cleanup <- struct{}{}
			return state.Event{Kind: state.DownloadCancelled}
		default:
			t.Errorf("queued effect ran after cancel: %v", r.Effect.Kind)
			return state.Event{}
		}
	})
	s.apply([]state.Effect{{Kind: state.EffectDownloadRun, Token: s.Model.Download.Request}})
	receiveDownload(t, reported)
	s.syncDownloads()
	if s.Model.Download.Phase != state.DownloadVerifying {
		t.Fatalf("apply callback did not queue next stage: %+v", s.Model.Download)
	}
	s.Dispatch(state.Event{Kind: state.DownloadCancel})
	receiveDownload(t, cancelled)
	select {
	case <-cleanup:
		t.Fatal("cleanup ran before previous stage stopped")
	default:
	}
	close(release)
	receiveDownload(t, cleanup)
	pumpDownloads(t, s, wake, func() bool { return s.Model.Download.Phase == state.DownloadIdle })
}

func TestDownloadPauseAcknowledgementAndResume(t *testing.T) {
	s := NewShell(downloadFixture(state.DownloadRunning, state.StageNetwork))
	defer s.Dispose()
	runs := make(chan DownloadRequest, 2)
	release := make(chan struct{})
	wake := make(chan struct{}, 1)
	s.RequestFrame = func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	s.GameDownloads = downloadBackendFunc(func(ctx context.Context, r DownloadRequest, report func(state.Event)) state.Event {
		if r.Effect.Kind == state.EffectDownloadPause {
			<-release
			return state.Event{Kind: state.DownloadPaused}
		}
		runs <- r
		report(state.Event{Kind: state.DownloadStarted})
		<-ctx.Done()
		return state.Event{}
	})
	s.apply([]state.Effect{{Kind: state.EffectDownloadRun, Token: s.Model.Download.Request}})
	first := receiveDownload(t, runs)
	s.Dispatch(state.Event{Kind: state.DownloadPause})
	if s.Model.Download.Phase == state.DownloadSuspended || state.PresentDownload(s.Model).CanResume {
		t.Fatal("pause displayed before checkpoint acknowledgement")
	}
	close(release)
	pumpDownloads(t, s, wake, func() bool { return s.Model.Download.Phase == state.DownloadSuspended })
	s.Dispatch(state.Event{Kind: state.DownloadResume})
	second := receiveDownload(t, runs)
	if second.Effect.Token <= first.Effect.Token {
		t.Fatal("resume reused cancelled worker identity")
	}
	pumpDownloads(t, s, wake, func() bool { return s.Model.Download.Phase == state.DownloadRunning })
}

func TestDownloadRecoveryScanRestoresPausedWithoutRunning(t *testing.T) {
	s := NewShell(normalFixture(state.ManagedReady))
	defer s.Dispose()
	requests := make(chan DownloadRequest, 2)
	wake := make(chan struct{}, 1)
	s.RequestFrame = func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	s.GameDownloads = downloadBackendFunc(func(_ context.Context, r DownloadRequest, _ func(state.Event)) state.Event {
		requests <- r
		return state.Event{Kind: state.DownloadRestored, Path: r.Root, Stage: state.StageNetwork, Index: uint8(state.DownloadUpdate), Message: "1.1.0", CurrentVersion: "1.0.0"}
	})
	s.syncDownloads()
	r := receiveDownload(t, requests)
	if r.Effect.Kind != state.EffectRecoverScan || r.Effect.Enabled {
		t.Fatal("startup performed explicit recovery or worker operation")
	}
	pumpDownloads(t, s, wake, func() bool { return s.Model.Download.Phase == state.DownloadSuspended })
	s.syncDownloads()
	select {
	case r := <-requests:
		t.Fatalf("startup restoration auto-ran work: %+v", r)
	default:
	}
}

func TestDownloadSettingsPersistOnlyAfterBackendAcknowledgement(t *testing.T) {
	m := normalFixture(state.ManagedReady)
	m.Download.Settings = state.DefaultDownloadPreferences()
	m.Download.Request = 9
	s := NewShell(m)
	defer s.Dispose()
	calls := make(chan DownloadRequest, 1)
	release := make(chan struct{})
	wake := make(chan struct{}, 1)
	s.RequestFrame = func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	s.GameDownloads = downloadBackendFunc(func(_ context.Context, r DownloadRequest, _ func(state.Event)) state.Event {
		calls <- r
		<-release
		return state.Event{Kind: state.DownloadSettingsSaved, DownloadSettings: r.Settings}
	})
	requested := state.DefaultDownloadPreferences()
	requested.FilesInFlight = 6
	requested.RateLimitKiB = 1024
	s.Dispatch(state.Event{Kind: state.DownloadSettingsApply, DownloadSettings: requested})
	r := receiveDownload(t, calls)
	if r.Effect.Kind != state.EffectSaveDownloads || r.Settings != requested || r.Root != m.Managed.Root {
		t.Fatalf("settings persistence lost UI snapshot: %+v", r)
	}
	if s.Model.Download.Settings != m.Download.Settings || !s.Model.Download.SettingsSaving {
		t.Fatal("unsaved preferences became active")
	}
	close(release)
	pumpDownloads(t, s, wake, func() bool { return s.Model.Download.SettingsSaved })
	if s.Model.Download.Settings != requested {
		t.Fatal("acknowledged preferences were not activated")
	}
}

func TestDownloadLocatePickerRunsInlineAndOnlyOnRequest(t *testing.T) {
	s := NewShell(normalFixture(state.ManagedReady))
	defer s.Dispose()
	calls := 0
	var pickedInline bool
	s.PickGameDirectory = func(context.Context) (string, error) { calls++; pickedInline = true; return `C:\synthetic\game`, nil }
	backend := make(chan DownloadRequest, 1)
	s.GameDownloads = downloadBackendFunc(func(_ context.Context, r DownloadRequest, _ func(state.Event)) state.Event {
		backend <- r
		return state.Event{Kind: state.InstallLocated, Path: r.Root}
	})
	if calls != 0 {
		t.Fatal("picker started without user request")
	}
	s.Dispatch(state.Event{Kind: state.InstallLocateRequest})
	if !pickedInline || calls != 1 {
		t.Fatal("native directory picker was not called on dispatch thread")
	}
	if request := receiveDownload(t, backend); request.Root != `C:\synthetic\game` {
		t.Fatalf("wrong selected root %q", request.Root)
	}
}

func TestDownloadCloseWaitsForCheckpointAcknowledgement(t *testing.T) {
	s := NewShell(downloadFixture(state.DownloadRunning, state.StageNetwork))
	defer s.Dispose()
	started, release := make(chan struct{}), make(chan struct{})
	wake := make(chan struct{}, 1)
	s.RequestFrame = func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	closed := false
	s.CloseWindow = func() { closed = true }
	s.GameDownloads = downloadBackendFunc(func(ctx context.Context, r DownloadRequest, _ func(state.Event)) state.Event {
		if r.Effect.Kind == state.EffectDownloadPause {
			<-release
			return state.Event{Kind: state.DownloadPaused}
		}
		close(started)
		<-ctx.Done()
		return state.Event{Kind: state.DownloadFailed}
	})
	s.apply([]state.Effect{{Kind: state.EffectDownloadRun, Token: 9}})
	receiveDownload(t, started)
	s.Dispatch(state.Event{Kind: state.RequestQuit})
	s.Dispatch(state.Event{Kind: state.ConfirmExit})
	if closed {
		t.Fatal("window closed before the download checkpoint acknowledgement")
	}
	close(release)
	pumpDownloads(t, s, wake, func() bool { return closed })
}
