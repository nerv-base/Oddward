// SPDX-License-Identifier: GPL-3.0-or-later

package state

import "testing"

func TestDownloadStartupAutoResumeRequiresEligibilityAndNetworkGuard(t *testing.T) {
	for _, tt := range []struct {
		name                              string
		eligible, official, preload, game bool
		want                              EffectKind
	}{
		{name: "eligible", eligible: true, want: EffectDownloadRun},
		{name: "explicit pause"},
		{name: "official launcher", eligible: true, official: true},
		{name: "game permits network", eligible: true, game: true, want: EffectDownloadRun},
		{name: "preload transfer", eligible: true, preload: true, want: EffectPreloadRun},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := downloadModel()
			m.Download.OfficialRunning = tt.official
			m.Download.GameRunning = tt.game
			m, _ = Reduce(m, Event{Kind: DownloadRecoveryScanRequest})
			old := m.Download.Request
			kind := DownloadUpdate
			if tt.preload {
				kind = DownloadPreload
			}
			m, fx := Reduce(m, Event{Kind: DownloadRestored, Token: old, Path: m.Managed.Root, Index: uint8(kind), Stage: StageNetwork, Message: "v2", Focused: tt.eligible})
			if tt.want == 0 {
				if len(fx) != 0 || m.Download.Request != old || m.Download.Phase != DownloadSuspended {
					t.Fatalf("restore resumed without eligibility: %+v %+v", m.Download, fx)
				}
				return
			}
			requireDownloadEffect(t, m, fx, tt.want)
			if m.Download.Request == old {
				t.Fatal("restoration reused scan token")
			}
			before := m
			m, _ = Reduce(m, Event{Kind: DownloadFailed, Token: old, Message: "扫描晚到"})
			if m != before {
				t.Fatal("scan completion changed resumed transfer")
			}
		})
	}
}

func TestDownloadStartupRestorePausesWithoutStartingTransfer(t *testing.T) {
	m := downloadModel()
	m, fx := Reduce(m, Event{Kind: DownloadRecoveryScanRequest})
	requireDownloadEffect(t, m, fx, EffectRecoverScan)
	stale := Event{Kind: DownloadRestored, Token: m.Download.Request - 1, Path: m.Managed.Root, Index: uint8(DownloadUpdate), Stage: StageNetwork, Message: "v2", CurrentVersion: "v1", Enabled: true}
	n, _ := Reduce(m, stale)
	if n != m {
		t.Fatal("stale restoration admitted")
	}
	e := stale
	e.Token = m.Download.Request
	e.Path = `E:\Other`
	n, _ = Reduce(m, e)
	if n != m {
		t.Fatal("restored task rebound to another root")
	}
	e.Path = m.Managed.Root
	m, fx = Reduce(m, e)
	if len(fx) != 0 || m.Download.Phase != DownloadSuspended || m.Download.Version != "v2" || m.Download.Current != "v1" || !m.Download.Verified {
		t.Fatalf("restoration must wait for resume: %+v %+v", m.Download, fx)
	}
	m, fx = Reduce(m, Event{Kind: DownloadResume})
	requireDownloadEffect(t, m, fx, EffectDownloadRun)
}
func TestDownloadStartupRestoresPreloadWaitingForReconcile(t *testing.T) {
	m := downloadModel()
	m.Download.Settings = DefaultDownloadPreferences()
	m, _ = Reduce(m, Event{Kind: DownloadRecoveryScanRequest})
	m, fx := Reduce(m, Event{Kind: DownloadRestored, Token: m.Download.Request, Path: m.Managed.Root, Index: uint8(DownloadPreload), Stage: StageIdle, Message: "v2", CurrentVersion: "v1", Body: "waiting-release", Focused: true})
	if len(fx) != 0 || m.Download.Preload != PreloadStateWaitingRelease || m.Download.Current != "v1" || PresentDownload(m).CanApply || !PresentDownload(m).CanReconcile {
		t.Fatalf("preload restore skipped reconcile: %+v", m.Download)
	}
	for _, kind := range []EventKind{PreloadCheckRequest, PreloadRequest} {
		n, fx := Reduce(m, Event{Kind: kind})
		if n != m || len(fx) != 0 {
			t.Fatal("discovery replaced staged preload")
		}
	}
}
func TestDownloadRecoveryAcceptsExplicitVerifiedRetryCompletionOnly(t *testing.T) {
	m := downloadModel()
	m, _ = Reduce(m, Event{Kind: DownloadRecoveryScanRequest})
	m, _ = Reduce(m, Event{Kind: DownloadRecoveryNeeded, Token: m.Download.Request, Message: "事务未完成，请恢复"})
	m, fx := Reduce(m, Event{Kind: DownloadRecoverRequest})
	if len(fx) != 1 || !fx[0].Enabled {
		t.Fatal("explicit recovery effect lacks user intent flag")
	}
	before := m
	m, _ = Reduce(m, Event{Kind: DownloadCommitted, Token: m.Download.Request, Message: "v2"})
	if m != before {
		t.Fatal("ordinary commit event escaped recovery")
	}
	m, fx = Reduce(m, Event{Kind: DownloadRecoveryCleared, Token: m.Download.Request, Message: "v2", Enabled: true})
	if len(fx) != 0 || m.Download.Current != "v2" || !m.Download.Verified || m.Download.Phase != DownloadIdle {
		t.Fatalf("verified explicit recovery result missing: %+v", m.Download)
	}
}
