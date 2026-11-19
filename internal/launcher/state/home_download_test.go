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
	if len(fx) != 1 || fx[0].Kind != EffectDownloadPlan || !m.HomeDownloadsOpen {
		t.Fatalf("update action must open planning with the real download backend: %+v", fx)
	}
	m, _ = Reduce(m, Event{Kind: DownloadPlanned, Token: m.Download.Request, Message: "2.0.0", Total: 8 << 20, Needed: 64 << 20, Free: 128 << 20})
	if !PresentDownload(m).CanConfirm {
		t.Fatal("a verified plan should be ready for confirmation")
	}
	m, fx = Reduce(m, Event{Kind: DownloadConfirm})
	if len(fx) != 1 || fx[0].Kind != EffectDownloadRun || m.HomeDownloadsOpen {
		t.Fatalf("confirmed download must use the compact home status: %+v", fx)
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
	m.HomeDownloadsOpen = true
	m, fx := Reduce(m, Event{Kind: DownloadConfirm})
	requireDownloadEffect(t, m, fx, EffectPreloadRun)
	m, _ = Reduce(m, downloadEvent(m, PreloadStarted))
	if !m.HomeDownloadsOpen || !PresentDownload(m).CanPause || Present(m).Primary != "启动游戏" {
		t.Fatalf("starting a preload must keep its controls alongside the launch action: %+v", m.Download)
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
