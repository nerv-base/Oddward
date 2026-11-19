// SPDX-License-Identifier: GPL-3.0-or-later

package state

import (
	"strings"
	"testing"
)

func TestInstallVerifyPauseResumesVerificationInsteadOfDownloading(t *testing.T) {
	m := downloadModel()
	m, _ = Reduce(m, Event{Kind: InstallVerifyRequest})
	m, _ = Reduce(m, Event{Kind: DownloadPause})
	m, _ = Reduce(m, downloadEvent(m, DownloadPaused))
	m, fx := Reduce(m, Event{Kind: DownloadResume})
	requireDownloadEffect(t, m, fx, EffectInstallVerify)
	if m.Download.Phase != DownloadVerifying {
		t.Fatal("manual verification did not resume")
	}
	m, _ = Reduce(m, Event{Kind: InstallVerified, Token: m.Download.Request, Message: "v1"})
	if !m.Download.Verified {
		t.Fatal("resumed verification completion ignored")
	}
}

func TestDownloadSpaceShortOfferAllowsExplicitReplanning(t *testing.T) {
	m := plannedDownload(t, DownloadInstall)
	m.Download.SpaceShort = true
	m.Download.Note = "disk-short"
	old := m.Download.Request
	m, fx := Reduce(m, Event{Kind: DownloadRequest, Enabled: true})
	requireDownloadEffect(t, m, fx, EffectDownloadPlan)
	if m.Download.Request == old || m.Download.SpaceShort || m.Download.AwaitingConfirmation {
		t.Fatal("replan retained obsolete offer")
	}
}

func TestPreloadReconcileSupplementalPlanRequiresFreshConfirmation(t *testing.T) {
	m := downloadModel()
	m.Download.Kind = DownloadPreload
	m.Download.Preload = PreloadStateWaitingRelease
	m.Download.PreloadVersion = "v2"
	m.Download.Version = "v2"
	m.Download.Total = 900
	m.Download.Downloaded = 900
	m.Download.FilesDone = 7
	m.Download.FilesTotal = 7
	m, fx := Reduce(m, Event{Kind: PreloadReconcileRequest})
	requireDownloadEffect(t, m, fx, EffectPreloadCheck)
	m, _ = Reduce(m, Event{Kind: DownloadRoutePlanned, Token: m.Download.Request, Index: uint8(RouteFull), Total: 12, Reused: 888})
	m, fx = Reduce(m, Event{Kind: DownloadPlanned, Token: m.Download.Request, Message: "v2", Total: 12, Reused: 888, FilesTotal: 1, Needed: 24, Free: 100})
	if len(fx) != 0 || m.Download.Phase != DownloadPreparing || !m.Download.AwaitingConfirmation || m.Download.Total != 12 || m.Download.Downloaded != 0 || m.Download.FilesDone != 0 {
		t.Fatalf("supplemental offer not reset/confirmed: %+v", m.Download)
	}
	m, fx = Reduce(m, Event{Kind: DownloadConfirm})
	requireDownloadEffect(t, m, fx, EffectPreloadRun)
	m, _ = Reduce(m, downloadEvent(m, PreloadStarted))
	m, fx = Reduce(m, downloadEvent(m, PreloadStaged))
	if len(fx) != 0 || m.Download.Preload != PreloadStateWaitingRelease {
		t.Fatal("supplement completed without waiting for reconcile")
	}
	m, fx = Reduce(m, Event{Kind: PreloadReconcileRequest})
	requireDownloadEffect(t, m, fx, EffectPreloadCheck)
}

func TestPreloadSpaceShortOfferAllowsExplicitReplanning(t *testing.T) {
	m := plannedDownload(t, DownloadPreload)
	m.Download.SpaceShort = true
	m, fx := Reduce(m, Event{Kind: PreloadRequest})
	requireDownloadEffect(t, m, fx, EffectPreloadCheck)
	m, fx = Reduce(m, Event{Kind: PreloadChecked, Token: m.Download.Request, Enabled: true, Message: "v2"})
	requireDownloadEffect(t, m, fx, EffectDownloadPlan)
}

func TestPreloadReconcileWaitsWithoutOfferingApplyWhenBackendCannotApply(t *testing.T) {
	for _, note := range []string{"waiting-release", "disk-short", "game-running", "official-running", "locked"} {
		t.Run(note, func(t *testing.T) {
			m := downloadModel()
			m.Download.Kind = DownloadPreload
			m.Download.Preload = PreloadStateWaitingRelease
			m.Download.PreloadVersion = "v2"
			m, _ = Reduce(m, Event{Kind: PreloadReconcileRequest})
			m, fx := Reduce(m, Event{Kind: PreloadChecked, Token: m.Download.Request, Message: "v2", Body: note})
			if len(fx) != 0 || m.Download.Preload != PreloadStateWaitingRelease || m.Download.Phase != DownloadDone || m.Download.Stage != StageIdle || m.Download.Note != note || PresentDownload(m).CanApply || !PresentDownload(m).CanReconcile {
				t.Fatalf("unsafe or stuck deferred reconcile: %+v", m.Download)
			}
		})
	}
}

func TestDownloadRequestPlansWithoutStartingTransfer(t *testing.T) {
	m := InitialNormal()
	m.Managed.Phase, m.Managed.Root = ManagedReady, `D:\SyntheticGame`
	// 260 is the protocol's DownloadRequest event; assert behavior before
	// introducing its declarations so RED is a behavior failure.
	kind := uint16(260)
	n, fx := Reduce(m, Event{Kind: EventKind(kind)})
	if len(fx) != 1 || uint16(fx[0].Kind) != 260 || fx[0].Token == 0 || fx[0].Path != m.Managed.Root {
		t.Fatalf("request must plan only with root and fresh token: model=%+v effects=%+v", n, fx)
	}
}
func TestDownloadFreshInstallLocationIsExplicitAndDoesNotAdopt(t *testing.T) {
	m := downloadModel()
	m, fx := Reduce(m, Event{Kind: InstallLocateRequest, Enabled: true})
	if len(fx) != 1 || fx[0].Kind != EffectInstallLocate || !fx[0].Enabled {
		t.Fatalf("fresh picker missing flag: %+v", fx)
	}
	m, fx = Reduce(m, Event{Kind: InstallLocated, Token: m.Download.Request, Path: `E:\Empty`})
	if m.Managed.Root != `E:\Empty` || m.Download.Located || m.Download.Verified {
		t.Fatalf("fresh root claimed installed: %+v", m.Download)
	}
	for _, f := range fx {
		if f.Kind == EffectInstallAdopt || f.Kind == EffectInstallVerify || f.Kind == EffectDownloadPlan {
			t.Fatal("picker implicitly began work")
		}
	}
	m, fx = Reduce(m, Event{Kind: DownloadRequest, Enabled: true})
	requireDownloadEffect(t, m, fx, EffectDownloadPlan)
	if m.Download.Kind != DownloadInstall {
		t.Fatal("fresh install planned as update")
	}
}
func TestInstallDialogPlansSelectedPathBeforeExplicitStart(t *testing.T) {
	m := normalFixture(ManagedUnlocated)
	m.Managed.Root = ""
	m, _ = Reduce(m, Event{Kind: Primary})
	m, fx := Reduce(m, Event{Kind: InstallLocateRequest, Enabled: true})
	if len(fx) != 1 || fx[0].Kind != EffectInstallLocate || !fx[0].Enabled {
		t.Fatalf("select must request the fresh-install picker: %+v", fx)
	}
	const selected = `E:\SyntheticEmpty`
	m, fx = Reduce(m, Event{Kind: InstallLocated, Token: m.Download.Request, Path: selected})
	requireDownloadEffect(t, m, fx, EffectDownloadPlan)
	if fx[0].Path != selected || m.Download.Kind != DownloadInstall || m.Download.Located || m.Download.Verified {
		t.Fatalf("selection must plan only for the chosen fresh path: %+v %+v", m.Download, fx)
	}
	if _, early := Reduce(m, Event{Kind: DownloadConfirm}); len(early) != 0 {
		t.Fatalf("start before planning emitted effects: %+v", early)
	}
	m, fx = Reduce(m, Event{Kind: DownloadPlanned, Token: m.Download.Request, Message: "v2", Total: 419, Needed: 804, Free: 1960})
	if p := PresentDownload(m); len(fx) != 0 || !p.CanConfirm || p.Total != 419 || p.Needed != 804 || p.Free != 1960 {
		t.Fatalf("plan must display backend facts and wait: %+v %+v", p, fx)
	}
	m, fx = Reduce(m, Event{Kind: DownloadConfirm})
	if len(fx) != 2 || fx[0].Kind != SaveManagedSettings || fx[1].Kind != EffectDownloadRun || fx[1].Path != selected ||
		m.Overlay != NoOverlay || m.HomeDownloadsOpen || Present(m).Primary != "正在准备下载" {
		t.Fatalf("explicit start must save the target and show preparation in the compact Home action: %+v %+v", m.Download, fx)
	}
}

func TestInstallDialogDirectoryChangeAndDismissIgnoreStalePlans(t *testing.T) {
	m := normalFixture(ManagedUnlocated)
	m.Managed.Root = ""
	m, _ = Reduce(m, Event{Kind: Primary})
	m, _ = Reduce(m, Event{Kind: InstallLocateRequest, Enabled: true})
	m, _ = Reduce(m, Event{Kind: InstallLocated, Token: m.Download.Request, Path: `E:\First`})
	firstPlan := Event{Kind: DownloadPlanned, Token: m.Download.Request, Message: "v2", Total: 419, Needed: 804, Free: 600, Enabled: true}
	m, _ = Reduce(m, firstPlan)
	if p := PresentDownload(m); p.CanConfirm || !p.SpaceShort || !CanSelectInstallPath(m) {
		t.Fatalf("a short volume must allow changing path but prevent starting: %+v", p)
	}
	m, _ = Reduce(m, Event{Kind: InstallLocateRequest, Enabled: true})
	m, _ = Reduce(m, Event{Kind: InstallLocated, Token: m.Download.Request})
	if m.Managed.Root != `E:\First` || !PresentDownload(m).SpaceShort {
		t.Fatal("closing the native picker must preserve the previous plan")
	}
	m, _ = Reduce(m, Event{Kind: InstallLocateRequest, Enabled: true})
	m, fx := Reduce(m, Event{Kind: InstallLocated, Token: m.Download.Request, Path: `F:\Second`})
	requireDownloadEffect(t, m, fx, EffectDownloadPlan)
	m, _ = Reduce(m, firstPlan)
	if p := PresentDownload(m); p.AwaitingConfirmation || p.Total != 0 || m.Managed.Root != `F:\Second` {
		t.Fatalf("pending second path must ignore first path's budget: %+v", p)
	}
	secondPlan := Event{Kind: DownloadPlanned, Token: m.Download.Request, Message: "v2", Total: 419, Needed: 804, Free: 1960}
	m, fx = Reduce(m, Event{Kind: Dismiss})
	if len(fx) != 0 || m.Managed.Root != "" || m.Overlay != NoOverlay || m.HomeDownloadsOpen {
		t.Fatalf("dismiss must discard the draft without running work: overlay=%v root=%q effects=%+v", m.Overlay, m.Managed.Root, fx)
	}
	m, fx = Reduce(m, secondPlan)
	if len(fx) != 0 || PresentDownload(m).AwaitingConfirmation {
		t.Fatal("closed modal accepted a pending plan")
	}
}

func TestPreloadCheckOnlyReconcileAndDiscardAreUserDriven(t *testing.T) {
	m := downloadModel()
	m, fx := Reduce(m, Event{Kind: PreloadCheckRequest})
	requireDownloadEffect(t, m, fx, EffectPreloadCheck)
	m, fx = Reduce(m, Event{Kind: PreloadChecked, Token: m.Download.Request, Message: "v2", Enabled: true})
	if len(fx) != 0 || m.Download.Preload != PreloadStateAvailable || m.Download.Phase != DownloadIdle {
		t.Fatalf("check-only started planning: %+v %+v", m.Download, fx)
	}
	m.Download.Kind = DownloadPreload
	m.Download.Preload = PreloadStateWaitingRelease
	m, fx = Reduce(m, Event{Kind: PreloadReconcileRequest})
	requireDownloadEffect(t, m, fx, EffectPreloadCheck)
	if !fx[0].Enabled || m.Download.Preload != PreloadStateReconcile {
		t.Fatal("reconcile flag/phase missing")
	}
	m, _ = Reduce(m, Event{Kind: PreloadAvailableToApply, Token: m.Download.Request})
	if m.Download.Preload != PreloadStateApplicable {
		t.Fatal("reconcile completion ignored")
	}
	m, fx = Reduce(m, Event{Kind: PreloadDiscardRequest})
	requireDownloadEffect(t, m, fx, EffectPreloadDiscard)
	m, _ = Reduce(m, Event{Kind: PreloadDiscarded, Token: m.Download.Request})
	if m.Download.Preload != PreloadStateUnavailable || m.Download.Current != "" {
		t.Fatal("discard changed installed version or retained offer")
	}
}

func downloadModel() Model {
	m := InitialNormal()
	m.Managed.Phase, m.Managed.Root, m.Managed.RiskAccepted = ManagedReady, `D:\SyntheticGame`, true
	return m
}
func downloadEvent(m Model, k EventKind) Event { return Event{Kind: k, Token: m.Download.Request} }
func requireDownloadEffect(t *testing.T, m Model, fx []Effect, k EffectKind) {
	t.Helper()
	if len(fx) != 1 || fx[0].Kind != k || fx[0].Token != m.Download.Request || fx[0].Path != m.Managed.Root {
		t.Fatalf("effect %v: %+v", k, fx)
	}
}
func plannedDownload(t *testing.T, kind DownloadKind) Model {
	t.Helper()
	m, _ := Reduce(downloadModel(), Event{Kind: DownloadRequest, Enabled: kind == DownloadInstall})
	if kind == DownloadPreload {
		m = downloadModel()
		m, _ = Reduce(m, Event{Kind: PreloadRequest})
		m, _ = Reduce(m, Event{Kind: PreloadChecked, Token: m.Download.Request, Enabled: true, Message: "v2"})
	}
	m, _ = Reduce(m, Event{Kind: DownloadRoutePlanned, Token: m.Download.Request, Index: uint8(RoutePatch), Total: 100, PatchBytes: 40, Reused: 900})
	m, fx := Reduce(m, Event{Kind: DownloadPlanned, Token: m.Download.Request, Message: "v2", Total: 100, FilesTotal: 4, Reused: 900})
	if len(fx) != 0 || !m.Download.AwaitingConfirmation || m.Download.Version != "v2" {
		t.Fatalf("planning must await user confirmation: %+v %+v", m.Download, fx)
	}
	return m
}
func runningDownload(t *testing.T) Model {
	t.Helper()
	m := plannedDownload(t, DownloadUpdate)
	m, fx := Reduce(m, Event{Kind: DownloadConfirm})
	requireDownloadEffect(t, m, fx, EffectDownloadRun)
	m, _ = Reduce(m, downloadEvent(m, DownloadStarted))
	return m
}
func TestDownloadPlanRequiresConfirmationAndRejectsShortSpace(t *testing.T) {
	m := plannedDownload(t, DownloadUpdate)
	if m.Download.Route != RoutePatch || m.Download.PatchBytes != 40 || m.Download.Reused != 900 {
		t.Fatalf("offer lost backend numbers: %+v", m.Download)
	}
	m.Download.SpaceShort = true
	n, fx := Reduce(m, Event{Kind: DownloadConfirm})
	if len(fx) != 0 || n.Download.Phase != DownloadPreparing {
		t.Fatal("space short offer started")
	}
	m.Download.SpaceShort = false
	n, fx = Reduce(m, Event{Kind: DownloadConfirm})
	requireDownloadEffect(t, n, fx, EffectDownloadRun)
	if n.Download.Request == m.Download.Request {
		t.Fatal("run must fence planning responses")
	}
	n2, fx := Reduce(n, Event{Kind: DownloadConfirm})
	if n2 != n || len(fx) != 0 {
		t.Fatal("duplicate confirmation started another worker")
	}
}
func TestDownloadPauseResumeCancelFenceWorkers(t *testing.T) {
	m := runningDownload(t)
	old := m.Download.Request
	m, fx := Reduce(m, Event{Kind: DownloadPause})
	requireDownloadEffect(t, m, fx, EffectDownloadPause)
	if m.Download.Request == old {
		t.Fatal("pause did not fence old progress")
	}
	n, _ := Reduce(m, Event{Kind: DownloadProgress, Token: old, Stage: StageNetwork, Downloaded: 80, Total: 100})
	if n != m {
		t.Fatal("old runner modified pause")
	}
	m, _ = Reduce(m, downloadEvent(m, DownloadPaused))
	if m.Download.Phase != DownloadSuspended {
		t.Fatal("pause acknowledgement missing")
	}
	m, fx = Reduce(m, Event{Kind: DownloadResume})
	requireDownloadEffect(t, m, fx, EffectDownloadRun)
	m, _ = Reduce(m, downloadEvent(m, DownloadStarted))
	old = m.Download.Request
	m, fx = Reduce(m, Event{Kind: DownloadCancel})
	requireDownloadEffect(t, m, fx, EffectDownloadCancel)
	n, _ = Reduce(m, Event{Kind: DownloadVerified, Token: old})
	if n != m {
		t.Fatal("cancelled worker entered verify")
	}
	m, _ = Reduce(m, downloadEvent(m, DownloadCancelled))
	if m.Download.Phase != DownloadIdle || m.Download.Current != "" {
		t.Fatalf("cancel not idle: %+v", m.Download)
	}
}
func TestDownloadProgressValidatesTokensStagesAndMonotonicCounters(t *testing.T) {
	m := runningDownload(t)
	m, _ = Reduce(m, Event{Kind: DownloadProgress, Token: m.Download.Request, Stage: StageNetwork, Downloaded: 50, Total: 100, FilesDone: 2, FilesTotal: 4, Rate: 10, ETASecs: 5})
	if m.Download.Downloaded != 50 || m.Download.Rate != 10 || m.Download.ETA != 5 {
		t.Fatalf("progress not recorded: %+v", m.Download)
	}
	for _, e := range []Event{
		{Kind: DownloadProgress, Token: m.Download.Request - 1, Stage: StageNetwork, Downloaded: 80, Total: 100},
		{Kind: DownloadProgress, Token: m.Download.Request, Stage: StageNetwork, Downloaded: 49, Total: 100},
		{Kind: DownloadProgress, Token: m.Download.Request, Stage: StageNetwork, Downloaded: 60, Total: 99},
		{Kind: DownloadProgress, Token: m.Download.Request, Stage: StageNetwork, Downloaded: 60, Total: 100, FilesDone: 1, FilesTotal: 4},
		{Kind: DownloadProgress, Token: m.Download.Request, Stage: StageNetwork, Downloaded: -1, Total: 100},
		{Kind: DownloadProgress, Token: m.Download.Request, Stage: StageNetwork, Downloaded: 101, Total: 100},
		{Kind: DownloadProgress, Token: m.Download.Request, Stage: StageNetwork, Downloaded: 60, Total: 100, Rate: -1},
		{Kind: DownloadProgress, Token: m.Download.Request, Stage: StageNetwork, Downloaded: 60, Total: 100, FilesDone: 5, FilesTotal: 4},
		{Kind: DownloadProgress, Token: m.Download.Request, Stage: StageCommit},
		{Kind: DownloadProgress, Token: m.Download.Request},
	} {
		n, fx := Reduce(m, e)
		if n != m || len(fx) != 0 {
			t.Fatalf("invalid progress accepted: %+v => %+v", e, n.Download)
		}
	}
	n, _ := Reduce(m, Event{Kind: DownloadProgress, Token: m.Download.Request, Stage: StageVerify, FilesDone: 1, FilesTotal: 4})
	if n.Download.Stage != StageVerify || n.Download.Downloaded != 0 || n.Download.Rate != 0 || n.Download.ETA != 0 {
		t.Fatalf("stage reset failed: %+v", n.Download)
	}
	n2, _ := Reduce(n, Event{Kind: DownloadProgress, Token: n.Download.Request, Stage: StageNetwork, Downloaded: 80, Total: 100})
	if n2 != n {
		t.Fatal("late network stage resurrected")
	}
}
func TestDownloadApplyVerifyCommitPublishOrder(t *testing.T) {
	m := runningDownload(t)
	m, _ = Reduce(m, downloadEvent(m, ApplyStarted))
	if m.Download.Phase != DownloadRebuilding || m.Download.Stage != StageApply {
		t.Fatalf("apply start: %+v", m.Download)
	}
	m, _ = Reduce(m, Event{Kind: ApplyProgress, Token: m.Download.Request, Stage: StageApply, Downloaded: 20, Total: 50, FilesDone: 1, FilesTotal: 2, Rate: 500, ETASecs: 3})
	if m.Download.Rate != 0 || m.Download.ETA != 0 {
		t.Fatal("apply advertised network speed")
	}
	m, fx := Reduce(m, Event{Kind: ApplyFinished, Token: m.Download.Request, FilesDone: 2, Enabled: true})
	requireDownloadEffect(t, m, fx, EffectDownloadVerify)
	if !m.Download.FallbackNote {
		t.Fatal("fallback invisible")
	}
	m, fx = Reduce(m, downloadEvent(m, DownloadVerified))
	requireDownloadEffect(t, m, fx, EffectDownloadCommit)
	if m.Download.Current != "" || m.Download.Stage != StageCommit {
		t.Fatal("published before commit")
	}
	m, _ = Reduce(m, Event{Kind: DownloadProgress, Token: m.Download.Request, Stage: StageFinalize})
	if m.Download.Current != "" {
		t.Fatal("finalize published version")
	}
	m, _ = Reduce(m, Event{Kind: DownloadCommitted, Token: m.Download.Request, Message: "v2"})
	if m.Download.Phase != DownloadDone || m.Download.Current != "v2" || !m.Download.Verified {
		t.Fatalf("commit not published: %+v", m.Download)
	}
}
func TestDownloadCommitBlockedByGameAndRequiresFreshUserRetry(t *testing.T) {
	m := runningDownload(t)
	m.Download.GameRunning = true
	n, fx := Reduce(m, downloadEvent(m, DownloadVerified))
	if len(fx) != 0 || n.Download.Phase == DownloadCommitting || n.Download.Note != "game-running" {
		t.Fatalf("write while game active: %+v %+v", n.Download, fx)
	}
	n.Download.GameRunning = false
	n, fx = Reduce(n, Event{Kind: DownloadConfirm})
	requireDownloadEffect(t, n, fx, EffectDownloadCommit)
}
func TestDownloadRecoveryCannotBeOverwrittenOrSwitchRoots(t *testing.T) {
	m := downloadModel()
	m, fx := Reduce(m, Event{Kind: DownloadRecoveryScanRequest})
	requireDownloadEffect(t, m, fx, EffectRecoverScan)
	m, _ = Reduce(m, Event{Kind: DownloadRecoveryNeeded, Token: m.Download.Request, Message: "上次更新未完成，请恢复"})
	for _, k := range []EventKind{DownloadStarted, DownloadCommitted, DownloadPlanned, InstallLocateRequest, ManagedBrowse, ManagedSelectPath, ManagedLoad, DownloadRequest, DownloadConfirm, DownloadCancel, InstallAdoptRequest, InstallVerifyRequest, ApplyFinished} {
		n, fx := Reduce(m, Event{Kind: k, Token: m.Download.Request, Path: `E:\Other`, Message: "v2", Enabled: true})
		if n != m || len(fx) != 0 {
			t.Fatalf("recovery overwritten by %d: %+v %+v", k, n.Download, fx)
		}
	}
	if Present(m).Primary != "完成上次更新" {
		t.Fatal("recovery primary missing")
	}
	m, fx = Reduce(m, Event{Kind: DownloadRecoverRequest})
	requireDownloadEffect(t, m, fx, EffectRecoverScan)
	m, _ = Reduce(m, downloadEvent(m, DownloadRecoveryCleared))
	if m.Download.Phase != DownloadIdle || m.Download.Current != "" {
		t.Fatal("recovery clear invented version")
	}
}
func TestDownloadLocateOnlyOnUserRequestQuickAdoptAndManualVerify(t *testing.T) {
	m := downloadModel()
	for _, e := range []Event{{Kind: Navigate, Page: CheckIn}, {Kind: ManagedLoad}, {Kind: DownloadRecoveryScanRequest}} {
		_, fx := Reduce(m, e)
		for _, f := range fx {
			if f.Kind == EffectInstallLocate {
				t.Fatal("automatic directory picker")
			}
		}
	}
	m, fx := Reduce(m, Event{Kind: InstallLocateRequest})
	requireDownloadEffect(t, m, fx, EffectInstallLocate)
	old := m.Managed.Root
	m, fx = Reduce(m, Event{Kind: InstallLocated, Token: m.Download.Request})
	if m.Managed.Root != old || len(fx) != 0 {
		t.Fatal("picker cancellation changed installation")
	}
	m, _ = Reduce(m, Event{Kind: InstallLocateRequest})
	m, fx = Reduce(m, Event{Kind: InstallLocated, Token: m.Download.Request, Message: `E:\Other`, Body: "branch"})
	if m.Managed.Root != `E:\Other` || !m.Download.Located || m.Download.Verified || m.Managed.RiskAccepted {
		t.Fatalf("quick locate state: %+v %+v", m.Managed, m.Download)
	}
	if len(fx) != 1 || fx[0].Kind != SaveManagedSettings {
		t.Fatalf("locate performed more than save: %+v", fx)
	}
	m, fx = Reduce(m, Event{Kind: InstallAdoptRequest})
	requireDownloadEffect(t, m, fx, EffectInstallAdopt)
	m, _ = Reduce(m, Event{Kind: InstallAdopted, Token: m.Download.Request, Message: "v1"})
	if m.Download.Current != "v1" || m.Download.Verified {
		t.Fatal("quick adopt claimed full verification")
	}
	m, fx = Reduce(m, Event{Kind: InstallVerifyRequest})
	requireDownloadEffect(t, m, fx, EffectInstallVerify)
	m, _ = Reduce(m, Event{Kind: InstallVerified, Token: m.Download.Request, Message: "v1"})
	if !m.Download.Verified || m.Download.Current != "v1" {
		t.Fatal("manual verify not recorded")
	}
}
func TestPreloadStagingNeverWritesInstallationAndRequiresReconcile(t *testing.T) {
	m := plannedDownload(t, DownloadPreload)
	m, fx := Reduce(m, Event{Kind: DownloadConfirm})
	requireDownloadEffect(t, m, fx, EffectPreloadRun)
	m, _ = Reduce(m, downloadEvent(m, PreloadStarted))
	m, fx = Reduce(m, downloadEvent(m, DownloadVerified))
	if len(fx) != 0 || m.Download.Current != "" {
		t.Fatal("preload verification committed installation")
	}
	m, _ = Reduce(m, downloadEvent(m, PreloadStaged))
	if m.Download.Preload != PreloadStateWaitingRelease {
		t.Fatalf("preload not waiting: %+v", m.Download)
	}
	n, fx := Reduce(m, Event{Kind: PreloadApplyRequest})
	if n != m || len(fx) != 0 {
		t.Fatal("unreconciled staging applied")
	}
	m, _ = Reduce(m, downloadEvent(m, PreloadAvailableToApply))
	m, fx = Reduce(m, Event{Kind: PreloadApplyRequest})
	requireDownloadEffect(t, m, fx, EffectPreloadApply)
	m, _ = Reduce(m, Event{Kind: PreloadApplied, Token: m.Download.Request, Message: "v2"})
	if m.Download.Preload != PreloadStateCompleted || m.Download.Current != "v2" {
		t.Fatal("applied preload not published")
	}
}
func TestDownloadLaunchAndRestartGuardsPreserveOrdinaryLaunch(t *testing.T) {
	for _, phase := range []DownloadPhase{DownloadIdle, DownloadRunning, DownloadSuspended, DownloadDone, DownloadError, DownloadStale} {
		m := downloadModel()
		m.Download.Phase = phase
		m.Download.Stage = StageNetwork
		n, fx := Reduce(m, Event{Kind: Primary})
		if n.Managed.Phase != ManagedPreparing || len(fx) != 2 || fx[0].Kind != PrepareManagedLogin || fx[1].Kind != TajiduoCheckInLaunch {
			t.Fatalf("ordinary launch blocked in %d: %+v %+v", phase, n.Managed, fx)
		}
	}
	for _, phase := range []DownloadPhase{DownloadRebuilding, DownloadCommitting} {
		m := downloadModel()
		m.Download.Phase = phase
		n, fx := Reduce(m, Event{Kind: Primary})
		if n != m || len(fx) != 0 || Present(m).Enabled || !UpdateBlocked(m) {
			t.Fatalf("mutation did not guard launch/restart: %d", phase)
		}
	}
	m := downloadModel()
	m.Download.OfficialRunning = true
	if DownloadNetworkBlocked(m) != "official-running" || DownloadWriteBlocked(m) != "official-running" {
		t.Fatal("official patcher guard missing")
	}
	n, fx := Reduce(m, Event{Kind: Primary})
	if n != m || len(fx) != 0 || !UpdateBlocked(m) {
		t.Fatal("official patcher allowed launch/restart")
	}
	m.Download.OfficialRunning = false
	m.Download.GameRunning = true
	if DownloadNetworkBlocked(m) != "" || DownloadWriteBlocked(m) != "game-running" {
		t.Fatal("game/network/write guard matrix broken")
	}
}
func TestDownloadZeroResultsNeverInventWork(t *testing.T) {
	m := downloadModel()
	for k := DownloadPlanned; k <= DownloadProcessesObserved; k++ {
		if k == DownloadRequest || k == InstallLocateRequest || k == InstallAdoptRequest || k == InstallVerifyRequest || k == PreloadRequest || k == DownloadRecoverRequest || k == DownloadRecoveryScanRequest {
			continue
		}
		n, fx := Reduce(m, Event{Kind: k})
		if n != m || len(fx) != 0 {
			t.Fatalf("zero event %d invented state: %+v %+v", k, n.Download, fx)
		}
	}
	_ = m == Model{} // compile-time comparable model contract
}
func TestDownloadNoteMappingsDoNotLeakKeys(t *testing.T) {
	for _, key := range []string{"not-offered", "unverified", "official-running", "game-running", "login-running", "disk-short", "locked", "patch-source-mismatch", "fallback-full", "recovery-required", "unknown-protocol-key"} {
		text := DownloadNoteText(key)
		if text == "" || text == key || strings.Contains(text, key) {
			t.Fatalf("unmapped key %q: %q", key, text)
		}
	}
	if DownloadNoteText("not-offered") == DownloadNoteText("unverified") {
		t.Fatal("unknown preload misrepresented as unsupported")
	}
}
