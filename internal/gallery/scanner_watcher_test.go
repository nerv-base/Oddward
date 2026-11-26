// SPDX-License-Identifier: GPL-3.0-or-later

package gallery

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestIncrementalScanSkipsUnchangedRecordsCorruptAndSwitchesGameRoot(t *testing.T) {
	ctx := context.Background()
	db := openTestDB(t)
	tmp := t.TempDir()

	rootA := filepath.Join(tmp, "GameA", "Client", "WindowsNoEditor")
	rootB := filepath.Join(tmp, "GameB", "Client", "WindowsNoEditor")
	picsRoot := filepath.Join(tmp, "Pictures")

	writeTestFile(t, filepath.Join(rootA, "Selfie", "111", "ScreenShots", "ok.png"), makePNGBytes(t, 40, 24, 1))
	writeTestFile(t, filepath.Join(rootA, "Selfie", "111", "ScreenShots", "broken.png"), []byte("corrupt png bytes"))
	writeTestFile(t, filepath.Join(rootB, "Selfie", "222", "ScreenShots", "b_ok.png"), makePNGBytes(t, 40, 24, 2))

	svc, err := NewService(db, ServiceOptions{
		ContentRootOverride: rootA,
		PicturesDirOverride: picsRoot,
		ArchiveDirOverride:  filepath.Join(tmp, "Archive"),
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()

	rep1, _, err := svc.SyncAndScan(ctx, "")
	if err != nil {
		t.Fatalf("first scan: %v", err)
	}
	if rep1.FilesSeen != 2 || rep1.InvalidCount != 1 {
		t.Fatalf("first scan report unexpected: %+v", rep1)
	}

	// Second scan must skip BOTH unchanged valid and unchanged corrupt files!
	rep2, _, err := svc.SyncAndScan(ctx, "")
	if err != nil {
		t.Fatalf("second scan: %v", err)
	}
	if rep2.Unchanged != 2 || rep2.Added != 0 || rep2.Updated != 0 {
		t.Fatalf("second scan should skip unchanged files: %+v", rep2)
	}

	// Switch game directory to rootB: old UID 111 source disabled, new UID 222 discovered.
	svc.opts.ContentRootOverride = rootB
	if _, _, err := svc.SyncAndScan(ctx, ""); err != nil {
		t.Fatalf("switch root scan: %v", err)
	}
	snap, err := svc.LoadSnapshot(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.UIDs) != 1 || snap.UIDs[0] != "222" {
		t.Fatalf("UIDs after switching game root = %v, want [222]", snap.UIDs)
	}
	if len(snap.Assets) != 1 || snap.Assets[0].FileName != "b_ok.png" {
		t.Fatalf("assets after switching game root = %+v", snap.Assets)
	}
}

func TestWatcherHandlesCreatePartialWriteBurstRenameDeleteAndNewUID(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	db := openTestDB(t)
	tmp := t.TempDir()
	contentRoot := filepath.Join(tmp, "Game", "Client", "WindowsNoEditor")
	selfieDir := filepath.Join(contentRoot, "Selfie")
	uid1Shots := filepath.Join(selfieDir, "100001", "ScreenShots")
	archiveDir := filepath.Join(tmp, "Archive")
	if err := os.MkdirAll(uid1Shots, 0o755); err != nil {
		t.Fatal(err)
	}

	svc, err := NewService(db, ServiceOptions{
		ContentRootOverride: contentRoot,
		PicturesDirOverride: filepath.Join(tmp, "Pictures"),
		ArchiveDirOverride:  archiveDir,
		WatcherOptions:      WatcherOptions{Debounce: 40 * time.Millisecond},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	svc.scanner.stableWait = 20 * time.Millisecond
	svc.scanner.stableRetries = 5

	// Enable auto-archive so watcher exercises full ingest -> archive pipeline.
	cfg := DefaultSettings()
	cfg.AutoArchive = true
	if err := svc.SaveSettings(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	if _, _, err := svc.SyncAndScan(ctx, ""); err != nil {
		t.Fatal(err)
	}

	notifyCh := make(chan struct{}, 32)
	if err := svc.StartWatching(ctx, "", func(bool) {
		select {
		case notifyCh <- struct{}{}:
		default:
		}
	}); err != nil {
		t.Fatalf("StartWatching: %v", err)
	}

	waitFor := func(desc string, pred func(Snapshot) bool) Snapshot {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			snap, err := svc.LoadSnapshot(ctx, Filter{})
			if err == nil && pred(snap) {
				return snap
			}
			select {
			case <-notifyCh:
			case <-time.After(40 * time.Millisecond):
			}
		}
		snap, _ := svc.LoadSnapshot(ctx, Filter{})
		t.Fatalf("timed out waiting for %s; last snapshot assets=%d stats=%+v", desc, len(snap.Assets), snap.Stats)
		return Snapshot{}
	}

	// 1. Simulate partial write in multiple chunks (rapid burst) -> coalesced & waited until stable.
	img1 := makePNGBytes(t, 48, 27, 77)
	p1 := filepath.Join(uid1Shots, "26_11_24_17_00_01aaaa.png")
	f, err := os.Create(p1)
	if err != nil {
		t.Fatal(err)
	}
	mid := len(img1) / 2
	if _, err := f.Write(img1[:mid]); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if _, err := f.Write(img1[mid:]); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	snap := waitFor("partial write indexed and archived", func(s Snapshot) bool {
		return len(s.Assets) == 1 && !s.Assets[0].Invalid && s.Assets[0].HasArchived
	})
	if snap.Assets[0].Width != 48 || snap.Assets[0].Height != 27 {
		t.Fatalf("unexpected watched asset dimensions: %+v", snap.Assets[0])
	}

	// 2. Rename file inside ScreenShots -> new name indexed, old name unavailable.
	p1Renamed := filepath.Join(uid1Shots, "26_11_24_17_00_01renamed.png")
	if err := os.Rename(p1, p1Renamed); err != nil {
		t.Fatal(err)
	}
	waitFor("rename reflected", func(s Snapshot) bool {
		return len(s.Assets) == 1 && s.Assets[0].FileName == "26_11_24_17_00_01renamed.png" && s.Assets[0].HasCached
	})

	// 3. Delete renamed file -> HasCached false, still available via archive!
	if err := os.Remove(p1Renamed); err != nil {
		t.Fatal(err)
	}
	waitFor("delete keeps archived copy available", func(s Snapshot) bool {
		return len(s.Assets) == 1 && !s.Assets[0].HasCached && s.Assets[0].HasArchived && s.Assets[0].Available
	})

	// 4. Create a brand new UID directory while watcher is live -> dynamically discovered!
	img2 := makePNGBytes(t, 64, 36, 88)
	p2 := filepath.Join(selfieDir, "200002", "ScreenShots", "26_11_24_17_01_00bbbb.png")
	writeTestFile(t, p2, img2)
	waitFor("new UID discovered live", func(s Snapshot) bool {
		return len(s.Assets) == 2 && len(s.UIDs) == 2
	})

	// 5. StopWatching must terminate cleanly without hanging.
	done := make(chan struct{})
	go func() {
		svc.StopWatching()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("StopWatching hung instead of cancelling overlapped ReadDirectoryChangesW")
	}
}

func TestWakeDetectorEmitsReconcileOnSleepJump(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var jumped atomic.Bool
	base := time.Date(2026, 11, 24, 17, 0, 0, 0, time.UTC)
	fakeNow := func() time.Time {
		if jumped.Load() {
			return base.Add(10 * time.Minute)
		}
		return base
	}
	dir := t.TempDir()
	w, err := StartWatcher(ctx, []WatchRoot{{Path: dir, Subtree: false}}, WatcherOptions{
		Debounce:           10 * time.Millisecond,
		SleepCheckInterval: 20 * time.Millisecond,
		Now:                fakeNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()

	time.Sleep(35 * time.Millisecond)
	jumped.Store(true)

	select {
	case ev := <-w.Events():
		if ev.Kind != WatchReconcileWake {
			t.Fatalf("got event kind %v, want WatchReconcileWake", ev.Kind)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for sleep/resume WatchReconcileWake event")
	}
}
