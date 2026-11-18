// SPDX-License-Identifier: GPL-3.0-or-later

package state

import "testing"

func TestDownloadDiskFullBackendPauseIsResumableWithoutUserPauseRequest(t *testing.T) {
	for _, kind := range []DownloadKind{DownloadUpdate, DownloadPreload} {
		m := plannedDownload(t, kind)
		m, _ = Reduce(m, Event{Kind: DownloadConfirm})
		m, _ = Reduce(m, downloadEvent(m, DownloadStarted))
		m.Download.Downloaded = 30
		m.Download.Rate = 50
		m.Download.ETA = 2
		old := m.Download.Request
		m, fx := Reduce(m, Event{Kind: DownloadPaused, Token: old, Enabled: true, Body: "disk-full"})
		if len(fx) != 0 || m.Download.Phase != DownloadSuspended || m.Download.Note != "disk-full" || m.Download.Request == old || m.Download.Downloaded != 30 || m.Download.Rate != 0 || m.Download.ETA != 0 || !PresentDownload(m).CanResume {
			t.Fatalf("disk full did not suspend resumably: %+v %+v", m.Download, fx)
		}
		if kind == DownloadPreload && m.Download.Preload != PreloadStatePaused {
			t.Fatal("preload did not pause")
		}
		before := m
		m, _ = Reduce(m, Event{Kind: DownloadFailed, Token: old, Message: "写入失败"})
		if m != before {
			t.Fatal("old worker error replaced disk-full pause")
		}
		m, fx = Reduce(m, Event{Kind: DownloadResume})
		want := EffectDownloadRun
		if kind == DownloadPreload {
			want = EffectPreloadRun
		}
		requireDownloadEffect(t, m, fx, want)
	}
}
func TestDownloadDiskFullBeforeFirstStartedReportStillPauses(t *testing.T) {
	m := plannedDownload(t, DownloadInstall)
	m, _ = Reduce(m, Event{Kind: DownloadConfirm})
	m, fx := Reduce(m, Event{Kind: DownloadPaused, Token: m.Download.Request, Enabled: true, Body: "disk-full"})
	if len(fx) != 0 || m.Download.Phase != DownloadSuspended || m.Download.Note != "disk-full" || !PresentDownload(m).CanResume {
		t.Fatal("immediate checkpoint failure lost resume")
	}
}

func TestDownloadPauseAcknowledgementCannotInventUnsolicitedOrMutationPause(t *testing.T) {
	m := runningDownload(t)
	for _, e := range []Event{{Kind: DownloadPaused, Token: m.Download.Request}, {Kind: DownloadPaused, Token: m.Download.Request, Body: "disk-full"}, {Kind: DownloadPaused, Token: m.Download.Request, Enabled: true, Body: "network"}} {
		n, fx := Reduce(m, e)
		if n != m || len(fx) != 0 {
			t.Fatal("ordinary acknowledgement invented unsolicited pause")
		}
	}
	for _, phase := range []DownloadPhase{DownloadRebuilding, DownloadCommitting, DownloadRecoveryRequired, DownloadDone} {
		m.Download.Phase = phase
		n, fx := Reduce(m, Event{Kind: DownloadPaused, Token: m.Download.Request, Enabled: true, Body: "disk-full"})
		if n != m || len(fx) != 0 {
			t.Fatalf("phase %d became resumable without safe checkpoint", phase)
		}
	}
}
