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
