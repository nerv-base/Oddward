// SPDX-License-Identifier: GPL-3.0-or-later

package state

import "testing"

func updatable() Model {
	m := InitialNormal()
	m.Managed.Phase = ManagedReady
	m.Update = UpdateState{Phase: SelfUpdateIdle, Current: "0.1.0"}
	return m
}

func offer(m Model, version string, newer bool) Model {
	m, _ = Reduce(m, Event{Kind: UpdateChecked, Token: m.Update.Request, Message: version, Body: "修复若干问题", Total: 20 << 20, Enabled: newer})
	return m
}

func TestUpdateCheckIsFencedAndOffersOnlyNewerVersions(t *testing.T) {
	m, fx := Reduce(updatable(), Event{Kind: UpdateCheck})
	if m.Update.Phase != SelfUpdateChecking || len(fx) != 1 || fx[0].Kind != CheckForUpdate || fx[0].Token != m.Update.Request {
		t.Fatalf("check: %+v %+v", m.Update, fx)
	}
	stale, _ := Reduce(m, Event{Kind: UpdateChecked, Token: m.Update.Request - 1, Message: "0.2.0", Enabled: true})
	if stale.Update.Phase != SelfUpdateChecking {
		t.Fatal("stale result applied")
	}
	if got := offer(m, "0.1.0", false); got.Update.Phase != SelfUpdateCurrent {
		t.Fatalf("same version: %+v", got.Update)
	}
	got := offer(m, "0.2.0", true)
	if got.Update.Phase != SelfUpdateAvailable || got.Update.Version != "0.2.0" || got.Update.Notes != "修复若干问题" || got.Update.Size != 20<<20 {
		t.Fatalf("offer: %+v", got.Update)
	}
	failed, _ := Reduce(m, Event{Kind: UpdateCheckFailed, Token: m.Update.Request, Message: "网络不可用"})
	if failed.Update.Phase != SelfUpdateFailed || failed.Update.Error != "网络不可用" {
		t.Fatalf("failed: %+v", failed.Update)
	}
}

func TestUnsupportedBuildsNeverCheck(t *testing.T) {
	m := InitialNormal()
	m.Update = UpdateState{Phase: SelfUpdateUnsupported, Note: "开发构建"}
	if next, fx := Reduce(m, Event{Kind: UpdateCheck, Enabled: true}); next.Update.Phase != SelfUpdateUnsupported || len(fx) != 0 {
		t.Fatalf("%+v %+v", next.Update, fx)
	}
}

func TestSkippedVersionStaysQuietUntilAskedFor(t *testing.T) {
	m := updatable()
	m.Update.Skipped = "0.2.0"
	m, _ = Reduce(m, Event{Kind: UpdateCheck})
	if got := offer(m, "0.2.0", true); got.Update.Phase != SelfUpdateCurrent {
		t.Fatalf("automatic check offered a skipped version: %+v", got.Update)
	}
	if got := offer(m, "0.3.0", true); got.Update.Phase != SelfUpdateAvailable {
		t.Fatal("a newer release than the skipped one must be offered")
	}
	m, _ = Reduce(m, Event{Kind: UpdateCheck, Enabled: true})
	if got := offer(m, "0.2.0", true); got.Update.Phase != SelfUpdateAvailable {
		t.Fatal("a manual check must offer the skipped version")
	}
}

func TestUpdatePanelOffersDownloadSkipAndLater(t *testing.T) {
	m := updatable()
	if next, _ := Reduce(m, Event{Kind: OpenOverlay, Overlay: UpdateDetails}); next.Overlay != NoOverlay {
		t.Fatal("panel opened without an offer")
	}
	m, _ = Reduce(m, Event{Kind: UpdateCheck})
	m = offer(m, "0.2.0", true)
	m, _ = Reduce(m, Event{Kind: OpenOverlay, Overlay: UpdateDetails})
	if m.Overlay != UpdateDetails {
		t.Fatal("panel did not open")
	}
	skipped, fx := Reduce(m, Event{Kind: UpdateSkip})
	if skipped.Update.Phase != SelfUpdateCurrent || skipped.Update.Skipped != "0.2.0" || skipped.Overlay != NoOverlay ||
		len(fx) != 1 || fx[0].Kind != SaveSkippedVersion || fx[0].Path != "0.2.0" {
		t.Fatalf("skip: %+v %+v", skipped.Update, fx)
	}
	later, _ := Reduce(m, Event{Kind: Dismiss})
	if later.Update.Phase != SelfUpdateAvailable || later.Overlay != NoOverlay {
		t.Fatal("later must keep the offer")
	}
	m, fx = Reduce(m, Event{Kind: UpdateDownload})
	if m.Update.Phase != SelfUpdateDownloading || len(fx) != 1 || fx[0].Kind != StageUpdate || fx[0].Token != m.Update.Request {
		t.Fatalf("download: %+v %+v", m.Update, fx)
	}
	m, _ = Reduce(m, Event{Kind: UpdateProgress, Token: m.Update.Request, Downloaded: 5 << 20, Total: 20 << 20})
	if m.Update.Downloaded != 5<<20 {
		t.Fatalf("progress: %+v", m.Update)
	}
	if again, fx := Reduce(m, Event{Kind: UpdateCheck, Enabled: true}); again.Update.Phase != SelfUpdateDownloading || len(fx) != 0 {
		t.Fatal("a check interrupted the download")
	}
	broken, _ := Reduce(m, Event{Kind: UpdateStageFailed, Token: m.Update.Request, Message: "磁盘空间不足"})
	if broken.Update.Phase != SelfUpdateAvailable || broken.Update.Error != "磁盘空间不足" {
		t.Fatalf("stage failure must allow retry: %+v", broken.Update)
	}
	m, _ = Reduce(m, Event{Kind: UpdateStaged, Token: m.Update.Request})
	if m.Update.Phase != SelfUpdateReady || m.Update.Error != "" {
		t.Fatalf("staged: %+v", m.Update)
	}
	m, fx = Reduce(m, Event{Kind: UpdateRestart})
	if len(fx) != 1 || fx[0].Kind != RestartForUpdate {
		t.Fatalf("restart: %+v", fx)
	}
	done, fx := Reduce(m, Event{Kind: UpdateRestarted})
	if !done.Closed || len(fx) == 0 || fx[len(fx)-1].Kind != CloseWindow {
		t.Fatalf("restarted: %+v %+v", done.Closed, fx)
	}
}

func TestUpdateWaitsWhileTheGameOrLoginIsBusy(t *testing.T) {
	m := updatable()
	m, _ = Reduce(m, Event{Kind: UpdateCheck})
	m = offer(m, "0.2.0", true)
	m.Managed.Phase = ManagedRunning
	// Staging only writes a new folder, so it may run beside the game.
	m, fx := Reduce(m, Event{Kind: UpdateDownload})
	if len(fx) != 1 {
		t.Fatal("download refused while the game runs")
	}
	m, _ = Reduce(m, Event{Kind: UpdateStaged, Token: m.Update.Request})
	if !UpdateBlocked(m) {
		t.Fatal("restart not blocked while the game runs")
	}
	if _, fx := Reduce(m, Event{Kind: UpdateRestart}); len(fx) != 0 {
		t.Fatal("restarted while the game runs")
	}
	m.Managed.Phase = ManagedReady
	if UpdateBlocked(m) {
		t.Fatal("still blocked")
	}
	for _, flow := range []Flow{Downloading, Paused, Verifying, Launching, Running} {
		m.Flow = flow
		if !UpdateBlocked(m) {
			t.Fatalf("restart not blocked in flow %v", flow)
		}
	}
}
