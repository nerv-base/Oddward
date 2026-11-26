// SPDX-License-Identifier: GPL-3.0-or-later

package state

import "testing"

func TestHomePrimaryPlansAnOfferedUpdate(t *testing.T) {
	m := pollableDownloadModel()
	m.Managed.RiskAccepted = true
	m, _ = Reduce(m, Event{Kind: DownloadOfferPollRequest})
	m, _ = Reduce(m, Event{Kind: DownloadOfferPolled, Token: m.Download.OfferRequest, Path: m.Managed.Root, Message: "2.0.0"})
	if p := Present(m); p.Primary != "更新游戏" || !p.Enabled {
		t.Fatalf("a confirmed game update must own the home action: %+v", p)
	}
	m, fx := Reduce(m, Event{Kind: Primary})
	if len(fx) != 1 || fx[0].Kind != EffectDownloadPlan {
		t.Fatalf("update action must plan with the real download backend: %+v", fx)
	}
	m, fx = Reduce(m, Event{Kind: DownloadPlanned, Token: m.Download.Request, Message: "2.0.0", Total: 8 << 20, Needed: 64 << 20, Free: 128 << 20})
	requireDownloadEffect(t, m, fx, EffectDownloadRun)
	if PresentDownload(m).CanConfirm || m.Download.AwaitingConfirmation {
		t.Fatal("a plan that fits must start without a confirmation step")
	}
	if p := Present(m); p.Primary != "正在准备下载" {
		t.Fatalf("the home capsule must show preparation at once: %+v", p)
	}
}

func TestHomeUpdatePlanFailureKeepsCorrectAction(t *testing.T) {
	for _, offer := range []string{"", "2.0.0"} {
		t.Run("offer="+offer, func(t *testing.T) {
			m := pollableDownloadModel()
			m.Managed.RiskAccepted = true
			if offer != "" {
				m, _ = Reduce(m, Event{Kind: DownloadOfferPollRequest})
				m, _ = Reduce(m, Event{Kind: DownloadOfferPolled, Token: m.Download.OfferRequest, Path: m.Managed.Root, Message: offer})
			}
			m, _ = Reduce(m, Event{Kind: DownloadRequest})
			m, _ = Reduce(m, Event{Kind: DownloadPlanFailed, Token: m.Download.Request, Message: "network"})
			wantLabel, wantEffect := "启动游戏", PrepareManagedLogin
			if offer != "" {
				wantLabel, wantEffect = "重试更新", EffectDownloadPlan
			}
			if p := Present(m); p.Primary != wantLabel || !p.Enabled || PresentDownload(m).Error == "" {
				t.Fatalf("planning failure must preserve the correct action and the error details: %+v", p)
			}
			_, fx := Reduce(m, Event{Kind: Primary})
			if len(fx) == 0 || fx[0].Kind != wantEffect {
				t.Fatalf("wrong action after planning failed: %+v", fx)
			}
		})
	}
}

func TestHomePreloadConfirmationKeepsControls(t *testing.T) {
	m := plannedDownload(t, DownloadPreload)
	m, _ = Reduce(m, downloadEvent(m, PreloadStarted))
	if !PresentDownload(m).CanPause || Present(m).Primary != "启动游戏" {
		t.Fatalf("starting a preload must keep its controls alongside the launch action: %+v", m.Download)
	}
	chip := PresentPreload(m)
	if !chip.Visible || chip.Label != "预下载中" || chip.Action != DownloadPause || !chip.Enabled {
		t.Fatalf("a running preload must surface as a pause chip: %+v", chip)
	}
}

func TestHomePrimaryControlsCurrentTask(t *testing.T) {
	for _, tc := range []struct {
		phase  DownloadPhase
		label  string
		effect EffectKind
	}{{DownloadRunning, "暂停下载", EffectDownloadPause}, {DownloadSuspended, "继续下载", EffectDownloadRun}} {
		m := pollableDownloadModel()
		m.Managed.RiskAccepted = true
		m.Download.Kind, m.Download.Phase, m.Download.Stage = DownloadUpdate, tc.phase, StageNetwork
		if p := Present(m); p.Primary != tc.label || !p.Enabled {
			t.Fatalf("task must present %q, got %+v", tc.label, p)
		}
		_, fx := Reduce(m, Event{Kind: Primary})
		if len(fx) != 1 || fx[0].Kind != tc.effect {
			t.Fatalf("%q emitted the wrong effects: %+v", tc.label, fx)
		}
	}
}

func TestHomePreloadOfferDoesNotReplaceLaunch(t *testing.T) {
	m := pollableDownloadModel()
	m, _ = Reduce(m, Event{Kind: DownloadOfferPollRequest})
	m, _ = Reduce(m, Event{Kind: DownloadOfferPolled, Token: m.Download.OfferRequest, Path: m.Managed.Root, Body: "3.0.0", Enabled: true})
	if p := Present(m); p.Primary != "启动游戏" || !p.Enabled {
		t.Fatalf("a preload offer must leave the game launch available: %+v", p)
	}
}

func TestHomePrimaryUpdateReconcilesAndAutoAppliesStagedPreload(t *testing.T) {
	t.Run("exact-match", func(t *testing.T) {
		m := pollableDownloadModel()
		m.Download.Current = "1.4.18"
		m.Download.Kind = DownloadPreload
		m.Download.Phase = DownloadDone
		m.Download.Preload = PreloadStateWaitingRelease
		m.Download.PreloadVersion = "1.5.0"
		m, _ = Reduce(m, Event{Kind: DownloadOfferPollRequest})
		m, _ = Reduce(m, Event{Kind: DownloadOfferPolled, Token: m.Download.OfferRequest, Path: m.Managed.Root, Message: "1.5.0"})
		if p := Present(m); p.Primary != "更新游戏" || !p.Enabled {
			t.Fatalf("formal release did not take primary: %+v", p)
		}
		// Clicking 更新游戏 starts preload reconcile, hides chip, shows progress on primary.
		m, fx := Reduce(m, Event{Kind: Primary})
		requireDownloadEffect(t, m, fx, EffectPreloadCheck)
		if !fx[0].Enabled || m.Download.Preload != PreloadStateReconcile {
			t.Fatalf("update did not reconcile staged preload: %+v %+v", m.Download, fx)
		}
		if chip := PresentPreload(m); chip.Visible {
			t.Fatalf("chip must hide while primary runs preload-backed update: %+v", chip)
		}
		if p := Present(m); p.Primary != "正在核对预下载" {
			t.Fatalf("primary during reconcile = %+v", p)
		}
		// Applicable immediately commits without a second click.
		m, fx = Reduce(m, downloadEvent(m, PreloadAvailableToApply))
		requireDownloadEffect(t, m, fx, EffectPreloadApply)
		if m.Download.Preload != PreloadStateApplying || m.Download.Phase != DownloadCommitting {
			t.Fatalf("did not auto-apply: %+v", m.Download)
		}
		m, _ = Reduce(m, Event{Kind: PreloadApplied, Token: m.Download.Request, Message: "1.5.0"})
		if m.Download.Preload != PreloadStateCompleted || m.Download.Current != "1.5.0" {
			t.Fatalf("apply completion: %+v", m.Download)
		}
	})

	t.Run("delta-supplement-auto-reconciles-and-applies", func(t *testing.T) {
		m := pollableDownloadModel()
		m.Download.Current = "1.4.18"
		m.Download.Kind = DownloadPreload
		m.Download.Phase = DownloadDone
		m.Download.Preload = PreloadStateWaitingRelease
		m.Download.PreloadVersion = "1.5.0"
		m, fx := Reduce(m, Event{Kind: DownloadRequest})
		requireDownloadEffect(t, m, fx, EffectPreloadCheck)
		m, fx = Reduce(m, Event{Kind: DownloadPlanned, Token: m.Download.Request, Message: "1.5.3", Total: 4 << 20, Needed: 64 << 20, Free: 128 << 20})
		requireDownloadEffect(t, m, fx, EffectPreloadRun)
		m, _ = Reduce(m, downloadEvent(m, PreloadStarted))
		if p := Present(m); p.Primary != "暂停下载" || !p.Enabled {
			t.Fatalf("supplemental download must live in primary capsule: %+v", p)
		}
		m, fx = Reduce(m, downloadEvent(m, PreloadStaged))
		requireDownloadEffect(t, m, fx, EffectPreloadCheck)
		m, fx = Reduce(m, downloadEvent(m, PreloadAvailableToApply))
		requireDownloadEffect(t, m, fx, EffectPreloadApply)
	})

	t.Run("stale-falls-back-to-ordinary-update", func(t *testing.T) {
		m := pollableDownloadModel()
		m.Download.Current = "1.4.18"
		m.Download.Kind = DownloadPreload
		m.Download.Phase = DownloadDone
		m.Download.Preload = PreloadStateWaitingRelease
		m.Download.PreloadVersion = "1.5.0"
		m, fx := Reduce(m, Event{Kind: DownloadRequest})
		requireDownloadEffect(t, m, fx, EffectPreloadCheck)
		m, fx = Reduce(m, downloadEvent(m, PreloadStale))
		requireDownloadEffect(t, m, fx, EffectDownloadPlan)
		if m.Download.Kind != DownloadUpdate {
			t.Fatalf("stale preload did not fall back to ordinary update: %+v", m.Download)
		}
	})

	t.Run("older-formal-hotfix-falls-back-without-discarding-preload", func(t *testing.T) {
		m := pollableDownloadModel()
		m.Download.Current = "1.4.17"
		m.Download.Kind = DownloadPreload
		m.Download.Phase = DownloadDone
		m.Download.Preload = PreloadStateWaitingRelease
		m.Download.PreloadVersion = "1.5.0"
		m, fx := Reduce(m, Event{Kind: DownloadRequest})
		requireDownloadEffect(t, m, fx, EffectPreloadCheck)
		m, fx = Reduce(m, Event{Kind: PreloadChecked, Token: m.Download.Request, Message: "1.5.0", CurrentVersion: "1.4.18", Body: "waiting-release", Focused: true})
		requireDownloadEffect(t, m, fx, EffectDownloadPlan)
		if m.Download.Kind != DownloadUpdate || m.Download.Preload != PreloadStateWaitingRelease {
			t.Fatalf("hotfix fallback broke preload state: %+v", m.Download)
		}
	})

	t.Run("lagging-mirror-does-not-fall-back", func(t *testing.T) {
		m := pollableDownloadModel()
		m.Download.Current = "1.4.18"
		m.Download.Kind = DownloadPreload
		m.Download.Phase = DownloadDone
		m.Download.Preload = PreloadStateWaitingRelease
		m.Download.PreloadVersion = "1.5.0"
		m, fx := Reduce(m, Event{Kind: DownloadRequest})
		requireDownloadEffect(t, m, fx, EffectPreloadCheck)
		// The mirror still reports 1.4.17 while 1.4.18 is installed. Only the
		// backend's numeric comparison decides that no intermediate hotfix is due.
		m, fx = Reduce(m, Event{Kind: PreloadChecked, Token: m.Download.Request, Message: "1.5.0", CurrentVersion: "1.4.17", Body: "waiting-release", Focused: false})
		if len(fx) != 0 || m.Download.Kind != DownloadPreload || m.Download.Preload != PreloadStateWaitingRelease || m.Download.Phase != DownloadDone {
			t.Fatalf("a lagging mirror started an ordinary update: %+v %+v", m.Download, fx)
		}
	})
}

func TestPreloadChipActionsAndCopyAcrossLifecycle(t *testing.T) {
	m := pollableDownloadModel()
	m.Download.Current = "1.4.18"
	m.Download.Kind = DownloadPreload
	m.Download.Phase = DownloadDone
	m.Download.Preload = PreloadStateWaitingRelease
	m.Download.PreloadVersion = "1.5.0"
	m.Download.Total, m.Download.Downloaded = 100, 100

	chip := PresentPreload(m)
	if !chip.Visible || chip.Action != PreloadReconcileRequest || !chip.Enabled || chip.ShowBar {
		t.Fatalf("waiting-release chip must be actionable without stale progress bar: %+v", chip)
	}

	// Once formal release is discovered by offer poll, chip reflects pending reconcile.
	m, _ = Reduce(m, Event{Kind: DownloadOfferPollRequest})
	m, _ = Reduce(m, Event{Kind: DownloadOfferPolled, Token: m.Download.OfferRequest, Path: m.Managed.Root, Message: "1.5.3"})
	chip = PresentPreload(m)
	if !chip.Visible || chip.Label != "预下载待核对" || chip.Action != PreloadReconcileRequest || !chip.Enabled {
		t.Fatalf("released-formal chip = %+v", chip)
	}

	// After completing a previous preload, a subsequent preload offer must still show up.
	m2 := pollableDownloadModel()
	m2.Download.Current = "1.5.3"
	m2.Download.Preload = PreloadStateCompleted
	m2.Download.Phase = DownloadDone
	m2, _ = Reduce(m2, Event{Kind: DownloadOfferPollRequest})
	m2, _ = Reduce(m2, Event{Kind: DownloadOfferPolled, Token: m2.Download.OfferRequest, Path: m2.Managed.Root, Message: "1.5.3", Body: "1.6.0", Enabled: true})
	chip = PresentPreload(m2)
	if !chip.Visible || chip.Label != "预下载" || chip.Action != PreloadRequest || !chip.Enabled {
		t.Fatalf("next version preload offer hidden after Completed: %+v", chip)
	}
}

// A paused or unconfirmed ordinary update owns the dock; reconciling preload
// next to it would start a second writable task and lose the paused plan.
func TestPreloadControlsWaitForUnfinishedUpdate(t *testing.T) {
	paused := func() Model {
		m := pollableDownloadModel()
		m.Download.Kind = DownloadUpdate
		m.Download.Phase = DownloadSuspended
		m.Download.Version = "1.4.18"
		m.Download.Preload = PreloadStateWaitingRelease
		m.Download.PreloadVersion = "1.5.0"
		return m
	}
	m := paused()
	if PresentDownload(m).CanReconcile || PresentPreload(m).Visible {
		t.Fatalf("paused update still offered preload handover: %+v %+v", PresentDownload(m), PresentPreload(m))
	}
	if n, fx := Reduce(m, Event{Kind: PreloadReconcileRequest}); len(fx) != 0 || n != m {
		t.Fatalf("preload reconcile disturbed the paused update: %+v %+v", n.Download, fx)
	}
	if n, fx := Reduce(m, Event{Kind: PreloadApplyRequest}); len(fx) != 0 || n != m {
		t.Fatalf("preload apply disturbed the paused update: %+v %+v", n.Download, fx)
	}

	pending := paused()
	pending.Download.Phase = DownloadPreparing
	pending.Download.AwaitingConfirmation = true
	pending.Download.Preload = PreloadStateApplicable
	if PresentDownload(pending).CanApply || PresentPreload(pending).Visible {
		t.Fatalf("unconfirmed update still offered preload apply: %+v %+v", PresentDownload(pending), PresentPreload(pending))
	}
	if n, fx := Reduce(pending, Event{Kind: PreloadApplyRequest}); len(fx) != 0 || n != pending {
		t.Fatalf("preload apply replaced the unconfirmed update plan: %+v %+v", n.Download, fx)
	}
	// The paused update itself must still resume normally.
	m, fx := Reduce(m, Event{Kind: DownloadResume})
	requireDownloadEffect(t, m, fx, EffectDownloadRun)
}
