// SPDX-License-Identifier: GPL-3.0-or-later

package state

import "testing"

func pollableDownloadModel() Model { m := downloadModel(); m.Download.Located = true; return m }
func TestDownloadOfferPollingProjectsAvailabilityWithoutPlanningOrChangingTask(t *testing.T) {
	m := pollableDownloadModel()
	m.Download.Version = "current-task"
	m.Download.Preload = PreloadStateAvailable
	m.Download.PreloadVersion = "previous-offer"
	before := m.Download
	m, fx := Reduce(m, Event{Kind: DownloadOfferPollRequest})
	if len(fx) != 1 || fx[0].Kind != EffectDownloadOfferPoll || fx[0].Token != m.Download.OfferRequest || fx[0].Path != m.Managed.Root {
		t.Fatal("poll effect missing independent token/root")
	}
	token := m.Download.OfferRequest
	n, fx := Reduce(m, Event{Kind: DownloadOfferPolled, Token: token, Path: m.Managed.Root, Message: "v2", Body: "v3", Enabled: true})
	if len(fx) != 0 || n.Download.OfferPending || n.Download.OfferVersion != "v2" || n.Download.PreloadOfferVersion != "v3" || n.Download.Request != before.Request || n.Download.Kind != before.Kind || n.Download.Version != before.Version || n.Download.Phase != before.Phase || n.Download.Preload != before.Preload || n.Download.PreloadVersion != before.PreloadVersion {
		t.Fatalf("poll stomped task or initiated work: %+v %+v", n.Download, fx)
	}
	p := PresentDownload(n)
	if p.OfferVersion != "v2" || p.PreloadOfferVersion != "v3" {
		t.Fatalf("passive offers absent: %+v", p)
	}
	n.Managed.Root = `E:\Other`
	p = PresentDownload(n)
	if p.OfferVersion != "" || p.PreloadOfferVersion != "" {
		t.Fatal("offers rebound to unrelated root")
	}
}
func TestDownloadOfferPollResultsFenceRootTokenAndTaskGeneration(t *testing.T) {
	for _, tt := range []struct {
		name          string
		change        func(*Model)
		staleEnvelope bool
	}{
		{"wrong token", func(m *Model) {}, true},
		{"root moved", func(m *Model) { m.Managed.Root = `E:\Other` }, false},
		{"task started and completed", func(m *Model) { m.Download.Request++; m.Download.Phase = DownloadDone }, false},
		{"managed generation changed", func(m *Model) { m.Managed.Request++ }, false},
		{"preload downloading", func(m *Model) { m.Download.Preload = PreloadStateDownloading }, false},
		{"official started", func(m *Model) { m.Download.OfficialRunning = true }, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := pollableDownloadModel()
			m, _ = Reduce(m, Event{Kind: DownloadOfferPollRequest})
			e := Event{Kind: DownloadOfferPolled, Token: m.Download.OfferRequest, Path: m.Managed.Root, Message: "v2", Body: "v3", Enabled: true, CurrentVersion: "stale-local", NeedsAdoption: true}
			tt.change(&m)
			if tt.staleEnvelope {
				e.Token++
			}
			m, fx := Reduce(m, e)
			if len(fx) != 0 || m.Download.OfferVersion != "" || m.Download.PreloadOfferVersion != "" || m.Download.Current != "" || m.Download.offerNeedsAdoption {
				t.Fatal("stale aggregate admitted")
			}
			if m.Download.OfferPending != tt.staleEnvelope {
				t.Fatal("poll pending latch incorrectly handled")
			}
		})
	}
}
func TestLocatedGameOfferUsesObservedVersionWithoutClaimingVerification(t *testing.T) {
	const root = `D:\SyntheticGame`
	m := InitialNormal()
	m, _ = Reduce(m, Event{Kind: ManagedLoad})
	m, _ = Reduce(m, Event{Kind: ManagedLoaded, Token: m.Managed.Request, Path: root})
	m, _ = Reduce(m, Event{Kind: ManagedLocated, Token: m.Managed.Request, Path: root})
	for _, tc := range []struct{ official, primary string }{{"1.5.6", "启动游戏"}, {"1.5.8", "更新游戏"}} {
		n, fx := Reduce(m, Event{Kind: DownloadOfferPollRequest})
		if len(fx) != 1 || fx[0].Kind != EffectDownloadOfferPoll || fx[0].Path != root {
			t.Fatalf("inspected existing root did not trigger a check: %+v", fx)
		}
		n, fx = Reduce(n, Event{Kind: DownloadOfferPolled, Token: n.Download.OfferRequest, Path: root, CurrentVersion: "1.5.6", Message: tc.official})
		if len(fx) != 0 || n.Download.Verified || n.Download.Located || PresentDownload(n).Current != "1.5.6" || Present(n).Primary != tc.primary {
			t.Fatalf("observed 1.5.6, official %s: primary=%q, download=%+v, effects=%+v", tc.official, Present(n).Primary, n.Download, fx)
		}
	}
}

func TestOfferedUpdatePreparesBaselineOnlyAfterClick(t *testing.T) {
	for _, tc := range []struct {
		name          string
		adopt, verify bool
	}{{"official install", true, false}, {"adopted metadata", false, true}, {"fully verified", false, false}} {
		t.Run(tc.name, func(t *testing.T) {
			m := downloadModel()
			m, _ = Reduce(m, Event{Kind: DownloadOfferPollRequest})
			m, fx := Reduce(m, Event{Kind: DownloadOfferPolled, Token: m.Download.OfferRequest, Path: m.Managed.Root, Message: "1.5.8", CurrentVersion: "1.5.6", NeedsAdoption: tc.adopt, NeedsVerification: tc.verify})
			if len(fx) != 0 || Present(m).Primary != "更新游戏" {
				t.Fatalf("offer performed work or failed to appear: %+v", fx)
			}
			m, fx = Reduce(m, Event{Kind: Primary})
			if tc.adopt {
				requireDownloadEffect(t, m, fx, EffectInstallAdopt)
				if Present(m).Enabled {
					t.Fatal("update button remains active while adopting")
				}
				m, fx = Reduce(m, Event{Kind: InstallAdopted, Token: m.Download.Request, Message: "1.5.6"})
			}
			if tc.adopt || tc.verify {
				requireDownloadEffect(t, m, fx, EffectInstallVerify)
				if m.Download.Phase != DownloadVerifying || Present(m).Primary != "检查游戏文件" {
					t.Fatalf("verification hidden: %+v", Present(m))
				}
				m, fx = Reduce(m, Event{Kind: InstallVerified, Token: m.Download.Request, Message: "1.5.6"})
			}
			requireDownloadEffect(t, m, fx, EffectDownloadPlan)
			if m.Download.Kind != DownloadUpdate {
				t.Fatalf("planned kind %v", m.Download.Kind)
			}
		})
	}
}

func TestFailedOfferedUpdateRetriesOnlyItsFailedBaselineStep(t *testing.T) {
	m := downloadModel()
	m, _ = Reduce(m, Event{Kind: DownloadOfferPollRequest})
	m, _ = Reduce(m, Event{Kind: DownloadOfferPolled, Token: m.Download.OfferRequest, Path: m.Managed.Root, Message: "1.5.8", CurrentVersion: "1.5.6", NeedsAdoption: true})
	m, _ = Reduce(m, Event{Kind: Primary})
	for _, step := range []struct {
		failure, success EventKind
		retry            EffectKind
	}{
		{InstallAdoptFailed, InstallAdopted, EffectInstallAdopt}, {InstallVerifyFailed, InstallVerified, EffectInstallVerify},
	} {
		token := m.Download.Request
		failed, fx := Reduce(m, Event{Kind: step.failure, Token: token, Message: "无法验证"})
		if len(fx) != 0 || Present(failed).Primary != "重试更新" || failed.Download.Error != "无法验证" {
			t.Fatalf("failed preparation: %+v, %+v", Present(failed), fx)
		}
		stale, fx := Reduce(failed, Event{Kind: step.success, Token: token, Message: "1.5.6"})
		if stale != failed || len(fx) != 0 {
			t.Fatal("late success advanced a failed update")
		}
		m, fx = Reduce(failed, Event{Kind: Primary})
		requireDownloadEffect(t, m, fx, step.retry)
		m, _ = Reduce(m, Event{Kind: step.success, Token: m.Download.Request, Message: "1.5.6"})
	}
	if m.Download.Kind != DownloadUpdate || m.Download.Stage != StagePlanning {
		t.Fatal("retry did not resume formal update")
	}
}

func TestChangingLocatedRootDiscardsObservedVersion(t *testing.T) {
	m := downloadModel()
	m, _ = Reduce(m, Event{Kind: DownloadOfferPollRequest})
	m, _ = Reduce(m, Event{Kind: DownloadOfferPolled, Token: m.Download.OfferRequest, Path: m.Managed.Root, Message: "1.5.8", CurrentVersion: "1.5.6", NeedsAdoption: true})
	const other = `E:\OtherGame`
	m.Overlay = LocateGame
	m, _ = Reduce(m, Event{Kind: ManagedSelectPath, Path: other})
	m, _ = Reduce(m, Event{Kind: ManagedLocated, Token: m.Managed.Request, Path: other})
	if p := PresentDownload(m); p.Current != "" || p.OfferVersion != "" {
		t.Fatalf("previous installation visible on another root: %+v", p)
	}
}

func TestDownloadOfferPollOnlyAdmitsIdleExistingRoot(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*Model)
	}{
		{"demo", func(m *Model) { m.NormalMode = false }},
		{"no root", func(m *Model) { m.Managed.Root = "" }},
		{"fresh install destination", func(m *Model) { m.Download.Located = false; m.Download.Kind = DownloadInstall }},
		{"running", func(m *Model) { m.Download.Phase = DownloadRunning }},
		{"paused", func(m *Model) { m.Download.Phase = DownloadSuspended }},
		{"preload downloading", func(m *Model) { m.Download.Preload = PreloadStateDownloading }},
		{"preload reconciling", func(m *Model) { m.Download.Preload = PreloadStateReconcile }},
		{"preload applying", func(m *Model) { m.Download.Preload = PreloadStateApplying }},
		{"game", func(m *Model) { m.Download.GameRunning = true }},
		{"official", func(m *Model) { m.Download.OfficialRunning = true }},
		{"login", func(m *Model) { m.Managed.Phase = ManagedPreparing }},
		{"in flight", func(m *Model) { m.Download.OfferPending = true }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := pollableDownloadModel()
			tt.change(&m)
			if CanPollDownloadOffer(m) {
				t.Fatal("busy/unbound model admitted")
			}
			n, fx := Reduce(m, Event{Kind: DownloadOfferPollRequest})
			if n != m || len(fx) != 0 {
				t.Fatal("blocked poll changed state")
			}
		})
	}
}
func TestDownloadOfferPollFailurePreservesKnownOfferAndClearsPending(t *testing.T) {
	m := pollableDownloadModel()
	m.Download.OfferVersion = "v2"
	m, _ = Reduce(m, Event{Kind: DownloadOfferPollRequest})
	m, fx := Reduce(m, Event{Kind: DownloadOfferPollFailed, Token: m.Download.OfferRequest, Path: m.Managed.Root, Message: "网络失败"})
	if len(fx) != 0 || m.Download.OfferPending || m.Download.OfferVersion != "v2" || m.Download.Error != "" || !CanPollDownloadOffer(m) {
		t.Fatal("background poll failure became foreground error or blocked next poll")
	}
}

func TestDownloadOfferPollingEmitsOnlyCheckEffectForAdoptedIdleRoot(t *testing.T) {
	m := downloadModel()
	m.Download.Located = true
	before := m.Download.Request
	m, fx := Reduce(m, Event{Kind: EventKind(314)})
	if len(fx) != 1 || fx[0].Kind != EffectKind(276) || fx[0].Token == 0 || fx[0].Path != m.Managed.Root || m.Download.Request != before || m.Download.Phase != DownloadIdle || m.Download.Kind != DownloadNone {
		t.Fatalf("poll must only check idle config: %+v %+v", m.Download, fx)
	}
}

func TestDownloadOfferPollContinuesWhilePreloadWaitsForRelease(t *testing.T) {
	for _, tc := range []struct {
		name    string
		preload PreloadState
		phase   DownloadPhase
	}{
		{"waiting-release-done", PreloadStateWaitingRelease, DownloadDone},
		{"waiting-release-restored", PreloadStateWaitingRelease, DownloadIdle},
		{"staged", PreloadStateStaged, DownloadIdle},
		{"applicable", PreloadStateApplicable, DownloadDone},
		{"stale", PreloadStateStale, DownloadStale},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := pollableDownloadModel()
			m.Download.Current = "1.4.18"
			m.Download.Kind = DownloadPreload
			m.Download.Phase = tc.phase
			m.Download.Preload = tc.preload
			m.Download.PreloadVersion = "1.5.0"
			if !CanPollDownloadOffer(m) {
				t.Fatalf("poll blocked in %s", tc.name)
			}
			m, fx := Reduce(m, Event{Kind: DownloadOfferPollRequest})
			if len(fx) != 1 || fx[0].Kind != EffectDownloadOfferPoll {
				t.Fatalf("expected offer poll effect, got %+v", fx)
			}
			m, fx = Reduce(m, Event{Kind: DownloadOfferPolled, Token: m.Download.OfferRequest, Path: m.Managed.Root, Message: "1.5.3"})
			if len(fx) != 0 || m.Download.Preload != tc.preload || PresentDownload(m).OfferVersion != "1.5.3" {
				t.Fatalf("formal offer lost or clobbered preload: %+v %+v", m.Download, PresentDownload(m))
			}
			if p := Present(m); p.Primary != "更新游戏" || !p.Enabled {
				t.Fatalf("primary did not switch to formal update: %+v", p)
			}
		})
	}
}
