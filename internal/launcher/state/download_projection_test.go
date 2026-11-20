// SPDX-License-Identifier: GPL-3.0-or-later

package state

import (
	"fmt"
	"testing"
)

func TestActivePreloadProjectionExplainsGameRunningRateCap(t *testing.T) {
	for _, tt := range []struct{ user, want int }{{0, 512}, {128, 128}, {512, 512}, {2048, 512}} {
		m := downloadModel()
		m.Download.Kind = DownloadPreload
		m.Download.Phase = DownloadRunning
		m.Download.Stage = StageNetwork
		m.Download.GameRunning = true
		m.Download.Settings.RateLimitKiB = tt.user
		m.Download.Rate = 42
		p := PresentDownload(m)
		want := fmt.Sprintf("游戏运行中，预下载限速上限为 %d KiB/s", tt.want)
		if p.Note != want || p.Rate != 42 || !p.ShowRate {
			t.Fatalf("cap=%d: %+v", tt.user, p)
		}
	}
}
func TestPreloadRuntimeCapNotePreservesOperationalMessagesAndActivity(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*DownloadState)
		want   string
	}{
		{"official note", func(d *DownloadState) { d.Note = "official-running" }, DownloadNoteText("official-running")},
		{"fallback note", func(d *DownloadState) { d.FallbackNote = true }, DownloadNoteText("fallback-full")},
		{"error", func(d *DownloadState) { d.Error = "校验失败" }, ""},
		{"official snapshot", func(d *DownloadState) { d.OfficialRunning = true }, ""},
		{"paused", func(d *DownloadState) { d.Phase = DownloadSuspended }, ""},
		{"pausing", func(d *DownloadState) { d.pausePending = true }, ""},
		{"cancelling", func(d *DownloadState) { d.cancelPending = true }, ""},
		{"verifying", func(d *DownloadState) { d.Stage = StageVerify }, ""},
		{"ordinary update", func(d *DownloadState) { d.Kind = DownloadUpdate }, ""},
		{"game exited", func(d *DownloadState) { d.GameRunning = false }, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := downloadModel()
			m.Download.Kind = DownloadPreload
			m.Download.Phase = DownloadRunning
			m.Download.Stage = StageNetwork
			m.Download.GameRunning = true
			tt.change(&m.Download)
			p := PresentDownload(m)
			if p.Note != tt.want {
				t.Fatalf("runtime cap obscured state: %+v", p)
			}
		})
	}
}

func TestDownloadProjectionOnlyExposesMeasuredNetworkProgress(t *testing.T) {
	m := downloadModel()
	m.Download = DownloadState{Phase: DownloadRunning, Stage: StageNetwork, Route: RouteMixed, Downloaded: 40, Total: 100, PatchBytes: 30, Reused: 900, FilesDone: 2, FilesTotal: 4}
	p := PresentDownload(m)
	if !p.ShowProgress || p.ShowRate || p.ShowETA || !p.CanPause || !p.CanCancel || p.Downloaded != 40 || p.Total != 100 || p.PatchBytes != 30 || p.Reused != 900 || p.StageText != "正在下载" || p.RouteText == "" {
		t.Fatalf("network projection: %+v", p)
	}
	m.Download.Rate, m.Download.ETA = 50, 2
	p = PresentDownload(m)
	if !p.ShowRate || !p.ShowETA || p.Rate != 50 || p.ETA != 2 {
		t.Fatalf("measured values hidden: %+v", p)
	}
	for _, stage := range []DownloadStage{StagePlanning, StageVerify, StageApply, StageCommit, StageFinalize} {
		m.Download.Stage = stage
		p = PresentDownload(m)
		if p.ShowRate || p.ShowETA || p.Rate != 0 || p.ETA != 0 || p.StageText == "" {
			t.Fatalf("non-network speed at %d: %+v", stage, p)
		}
		if stage == StagePlanning && p.ShowProgress {
			t.Fatal("planning invented percentage")
		}
	}
}
func TestDownloadProjectionActionsExplainFailureAndRecovery(t *testing.T) {
	for _, tc := range []struct {
		phase DownloadPhase
		stage DownloadStage
		text  string
	}{
		{DownloadRunning, StageNetwork, "正在下载"},
		{DownloadSuspended, StageNetwork, "下载已暂停"},
		{DownloadRebuilding, StageApply, "正在重建文件"},
		{DownloadCommitting, StageCommit, "正在提交更新"},
		{DownloadError, StageIdle, "操作未完成"},
		{DownloadRecoveryRequired, StageIdle, "上次更新未完成"},
	} {
		m := downloadModel()
		m.Download.Phase, m.Download.Stage = tc.phase, tc.stage
		p := PresentDownload(m)
		if p.StageText != tc.text {
			t.Fatalf("phase %d text=%q", tc.phase, p.StageText)
		}
		if tc.phase == DownloadError && p.NextAction == "" {
			t.Fatal("failure has no next step")
		}
		if tc.phase == DownloadRecoveryRequired && (!p.CanRecover || p.CanConfirm || p.CanCancel || p.CanVerify) {
			t.Fatalf("recovery offers unsafe actions: %+v", p)
		}
	}
	m := plannedDownload(t, DownloadUpdate)
	if p := PresentDownload(m); p.CanConfirm || !p.CanCancel || p.StageText == "" {
		t.Fatalf("a started transfer must stay cancellable: %+v", p)
	}
	short := downloadModel()
	short, _ = Reduce(short, Event{Kind: DownloadRequest})
	short, _ = Reduce(short, Event{Kind: DownloadPlanned, Token: short.Download.Request, Message: "v2", Total: 8 << 20, Needed: 64 << 20, Free: 1 << 20, Enabled: true})
	if p := PresentDownload(short); p.CanConfirm || p.NextAction == "" || !p.SpaceShort {
		t.Fatal("space shortage not actionable")
	}
	m = downloadModel()
	m.Download.Preload = PreloadStateApplicable
	p := PresentDownload(m)
	if !p.CanApply {
		t.Fatal("reconciled preload cannot apply")
	}
	m.Download.GameRunning = true
	if PresentDownload(m).CanApply {
		t.Fatal("apply enabled while game runs")
	}
}
func TestDownloadRouteAndFallbackProjection(t *testing.T) {
	for _, route := range []RouteKind{RouteUnknown, RouteAllReuse, RoutePatch, RouteFull, RouteMixed} {
		m := downloadModel()
		m.Download.Route = route
		m.Download.FallbackNote = true
		p := PresentDownload(m)
		if p.RouteText == "" || !p.FallbackNote || p.Note != "部分文件改为完整下载" {
			t.Fatalf("route/fallback not explained: %+v", p)
		}
	}
	m := downloadModel()
	m.Download.Phase, m.Download.Stage = DownloadRebuilding, StageApply
	if Present(m).Status != "正在重建文件" {
		t.Fatal("primary mislabels reconstruction as commit")
	}
}
func TestDownloadGuardsPendingLaunchConsentAndKnownRunningGame(t *testing.T) {
	m := downloadModel()
	m.Managed.RiskAccepted = false
	m, _ = Reduce(m, Event{Kind: Primary})
	if m.Overlay != LaunchNotice {
		t.Fatal("missing consent fixture")
	}
	m.Download.Phase, m.Download.Stage = DownloadCommitting, StageCommit
	n, fx := Reduce(m, Event{Kind: ManagedAcceptLaunch})
	if n != m || len(fx) != 0 {
		t.Fatal("pending consent started login during commit")
	}
	m = downloadModel()
	m.Download.GameRunning = true
	n, fx = Reduce(m, Event{Kind: Primary})
	if n != m || len(fx) != 0 || Present(m).Enabled {
		t.Fatal("known running game allowed duplicate launch")
	}
}

func TestDownloadUserRequestsBlockedWhileLegacyLocateOrLaunchActive(t *testing.T) {
	for _, phase := range []ManagedPhase{ManagedLoading, ManagedLocating, ManagedPreparing, ManagedStarting, ManagedRunning, ManagedUnobserved} {
		m := downloadModel()
		m.Managed.Phase = phase
		n, fx := Reduce(m, Event{Kind: InstallLocateRequest})
		if n != m || len(fx) != 0 {
			t.Fatalf("locate during managed %d", phase)
		}
	}
}
func TestDownloadQuitConfirmsAndCancelsBeforeClose(t *testing.T) {
	m := runningDownload(t)
	n, fx := Reduce(m, Event{Kind: RequestQuit})
	if n.Closed || n.Overlay != ConfirmQuit || len(fx) != 0 {
		t.Fatal("active download closed without confirmation")
	}
	n, fx = Reduce(n, Event{Kind: ConfirmExit})
	if !n.Closed || len(fx) != 3 || fx[0].Kind != EffectDownloadPause || fx[1].Kind != CancelOperation || fx[2].Kind != CloseWindow {
		t.Fatalf("close must request checkpoint before close: %+v", fx)
	}
}
