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
		{"preload staged", func(m *Model) { m.Download.Preload = PreloadStateWaitingRelease }, false},
		{"official started", func(m *Model) { m.Download.OfficialRunning = true }, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := pollableDownloadModel()
			m, _ = Reduce(m, Event{Kind: DownloadOfferPollRequest})
			e := Event{Kind: DownloadOfferPolled, Token: m.Download.OfferRequest, Path: m.Managed.Root, Message: "v2", Body: "v3", Enabled: true}
			tt.change(&m)
			if tt.staleEnvelope {
				e.Token++
			}
			m, fx := Reduce(m, e)
			if len(fx) != 0 || m.Download.OfferVersion != "" || m.Download.PreloadOfferVersion != "" {
				t.Fatal("stale aggregate admitted")
			}
			if m.Download.OfferPending != tt.staleEnvelope {
				t.Fatal("poll pending latch incorrectly handled")
			}
		})
	}
}
func TestDownloadOfferPollOnlyAdmitsIdleAdoptedRoot(t *testing.T) {
	for _, tt := range []struct {
		name   string
		change func(*Model)
	}{
		{"demo", func(m *Model) { m.NormalMode = false }},
		{"no root", func(m *Model) { m.Managed.Root = "" }},
		{"unadopted", func(m *Model) { m.Download.Located = false }},
		{"running", func(m *Model) { m.Download.Phase = DownloadRunning }},
		{"paused", func(m *Model) { m.Download.Phase = DownloadSuspended }},
		{"staged", func(m *Model) { m.Download.Preload = PreloadStateWaitingRelease }},
		{"applicable", func(m *Model) { m.Download.Preload = PreloadStateApplicable }},
		{"stale", func(m *Model) { m.Download.Preload = PreloadStateStale }},
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
