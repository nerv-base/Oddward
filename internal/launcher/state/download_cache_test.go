// SPDX-License-Identifier: GPL-3.0-or-later

package state

import (
	"strings"
	"testing"
)

func TestFreshInstallLocationPreservesInstallIntentWithoutScratchChoice(t *testing.T) {
	m := downloadModel()
	m, _ = Reduce(m, Event{Kind: InstallLocateRequest, Enabled: true})
	m, _ = Reduce(m, Event{Kind: InstallLocated, Token: m.Download.Request, Path: `D:\FreshSynthetic`})
	if m.Download.Kind != DownloadInstall || m.Download.Located || m.Download.Verified || m.Download.Current != "" {
		t.Fatalf("fresh directory selection lost installation intent: %+v", m.Download)
	}
}

func TestDownloadCacheSelectionValidatesThenRequiresExplicitReplan(t *testing.T) {
	m := plannedDownload(t, DownloadInstall)
	before := m.Download
	n, fx := Reduce(m, Event{Kind: DownloadCacheSelect})
	requireDownloadEffect(t, n, fx, EffectInstallCacheLocate)
	if n.Download.Request == before.Request || PresentDownload(n).CanConfirm {
		t.Fatal("cache validation did not fence the prior confirmation")
	}
	n, fx = Reduce(n, Event{Kind: DownloadCacheLocated, Token: n.Download.Request, Path: `E:\SyntheticScratch`})
	if len(fx) != 0 || n.Download.CacheRoot != `E:\SyntheticScratch` || n.Download.Phase != DownloadIdle || n.Download.AwaitingConfirmation {
		t.Fatalf("scratch selection started work or retained old plan: %+v %+v", n.Download, fx)
	}
	if PresentDownload(n).CacheRoot != `E:\SyntheticScratch` {
		t.Fatal("validated scratch directory was not presented")
	}
	n, fx = Reduce(n, Event{Kind: DownloadRequest, Enabled: true})
	requireDownloadEffect(t, n, fx, EffectDownloadPlan)
	n, fx = Reduce(n, Event{Kind: DownloadPlanned, Token: n.Download.Request, Message: "v2", Body: "cross-volume", Needed: 40, Free: 80})
	p := PresentDownload(n)
	if len(fx) != 0 || !p.CanConfirm || !strings.Contains(p.StorageNote, "跨卷") || p.BudgetVolume != "安装卷" {
		t.Fatalf("cross-volume plan was not explicitly offered: %+v", p)
	}
	n, _ = Reduce(n, Event{Kind: DownloadConfirm})
	if n.Download.CacheRoot != `E:\SyntheticScratch` {
		t.Fatal("confirmation lost validated scratch path")
	}
}

func TestDownloadCachePickerCancelAndFailureRetainPriorPlan(t *testing.T) {
	for _, failed := range []bool{false, true} {
		m := plannedDownload(t, DownloadInstall)
		before := m.Download
		m, _ = Reduce(m, Event{Kind: DownloadCacheSelect})
		e := Event{Kind: DownloadCacheLocated, Token: m.Download.Request}
		if failed {
			e.Kind = DownloadCacheLocateFailed
			e.Message = "暂存目录必须位于另一磁盘"
		}
		m, fx := Reduce(m, e)
		if len(fx) != 0 || m.Download.Phase != before.Phase || m.Download.Version != before.Version || m.Download.CacheRoot != before.CacheRoot || !PresentDownload(m).CanConfirm {
			t.Fatalf("cancel/failure replaced prior confirmed plan: %+v", m.Download)
		}
		if failed && m.Download.Error != e.Message {
			t.Fatal("backend validation error was hidden")
		}
	}
}

func TestDownloadCacheSelectionOnlyPermittedForInactiveFreshInstall(t *testing.T) {
	for _, kind := range []string{"existing", "running", "paused", "preload", "official", "game", "recovery"} {
		t.Run(kind, func(t *testing.T) {
			m := plannedDownload(t, DownloadInstall)
			switch kind {
			case "existing":
				m.Download.Current = "v1"
			case "running":
				m.Download.Phase = DownloadRunning
			case "paused":
				m.Download.Phase = DownloadSuspended
			case "preload":
				m.Download.Kind = DownloadPreload
			case "official":
				m.Download.OfficialRunning = true
			case "game":
				m.Download.GameRunning = true
			case "recovery":
				m.Download.Phase = DownloadRecoveryRequired
			}
			n, fx := Reduce(m, Event{Kind: DownloadCacheSelect})
			if n != m || len(fx) != 0 || PresentDownload(m).CanSelectCache {
				t.Fatal("scratch selection allowed while fresh-install configuration is locked")
			}
		})
	}
}

func TestDownloadCacheRejectsStaleSelectionAndLabelsSourceBudget(t *testing.T) {
	m := plannedDownload(t, DownloadInstall)
	m, _ = Reduce(m, Event{Kind: DownloadCacheSelect})
	stale, _ := Reduce(m, Event{Kind: DownloadCacheLocated, Token: m.Download.Request - 1, Path: `E:\stale`})
	if stale != m {
		t.Fatal("stale directory validation was accepted")
	}
	m, _ = Reduce(m, Event{Kind: DownloadCacheLocated, Token: m.Download.Request, Path: `E:\SyntheticScratch`})
	moved := m
	moved.Managed.Root = `F:\OtherSyntheticInstall`
	if PresentDownload(moved).CacheRoot != "" || DownloadCacheRoot(moved) != "" {
		t.Fatal("scratch choice leaked to another installation")
	}
	m, _ = Reduce(m, Event{Kind: DownloadRequest, Enabled: true})
	m, _ = Reduce(m, Event{Kind: DownloadPlanned, Token: m.Download.Request, Message: "v2", Body: "cross-volume", Focused: true, Enabled: true, Needed: 80, Free: 40})
	p := PresentDownload(m)
	if p.BudgetVolume != "下载暂存卷" || p.CanConfirm || !strings.Contains(p.StorageNote, "跨卷") {
		t.Fatalf("short source budget confused with installation space: %+v", p)
	}
}
