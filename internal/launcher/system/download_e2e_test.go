//go:build windows && e2e

// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/game"
	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// This is a destructive-volume-usage test. It is opt-in and writes only to a
// newly named child of an explicitly marked test workspace.
func TestRealGameResourcesFirstInstall(t *testing.T) {
	if testing.Short() || os.Getenv("ODDWARD_E2E_NETWORK") != "1" {
		t.Skip("official CDN and isolated installation require ODDWARD_E2E_NETWORK=1")
	}
	base, err := downloadDirectory(os.Getenv("ODDWARD_E2E_DOWNLOAD_ROOT"), false)
	if err != nil || filepath.Dir(base) == base || !strings.HasPrefix(filepath.Base(base), "Oddward-validation-") {
		t.Fatal("choose a new local Oddward-validation-* test workspace")
	}
	marker, err := os.ReadFile(filepath.Join(base, ".oddward-validation"))
	if err != nil || strings.TrimSpace(string(marker)) != filepath.Base(base) {
		t.Fatal("missing isolated workspace marker")
	}
	root := filepath.Join(base, "fresh")
	stateRoot := filepath.Join(base, "install-state")
	metadataOnly := os.Getenv("ODDWARD_E2E_DOWNLOAD_METADATA_ONLY") == "1"
	resume := os.Getenv("ODDWARD_E2E_DOWNLOAD_RESUME") == "1"
	expected := os.Getenv("ODDWARD_E2E_DOWNLOAD_VERSION")
	if resume && (metadataOnly || expected == "") {
		t.Fatal("resuming a transfer requires the pinned installation version")
	}
	if metadataOnly {
		root = filepath.Join(base, "metadata-probe")
		stateRoot = filepath.Join(t.TempDir(), "state")
	}
	for _, path := range []string{root, stateRoot} {
		if resume {
			if _, err := downloadDirectory(path, false); err != nil {
				t.Fatalf("isolated resume directory unavailable: %v", err)
			}
		} else if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("fresh installation and state paths must not exist")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Hour)
	defer cancel()
	backend := &GameDownloadBackend{StateRoot: stateRoot, Version: "local-e2e"}
	req := launcher.DownloadRequest{Root: root, Kind: state.DownloadInstall, Effect: state.Effect{Kind: state.EffectDownloadPlan}}
	var planned state.Event
	if resume {
		planned, err = backend.recoverDownload(ctx, req, state.Event{})
		if err != nil || planned.Kind != state.DownloadRestored || state.DownloadKind(planned.Index) != state.DownloadInstall {
			t.Fatalf("restored first-install transfer event=%d: %v", planned.Kind, err)
		}
		t.Logf("restored first-install transfer version=%s", planned.Message)
	} else {
		planned, err = backend.planDownload(ctx, req, state.Event{}, func(state.Event) {})
		if err != nil || planned.Kind != state.DownloadPlanned || planned.Enabled {
			t.Fatalf("plan event=%d required=%d free=%d: %v", planned.Kind, planned.Needed, planned.Free, err)
		}
		plan := backend.getPlan(root)
		if plan == nil || planned.Total <= 0 || len(plan.plan.Targets) == 0 {
			t.Fatal("official plan is empty")
		}
		t.Logf("plan version=%s targets=%d network=%d free=%d", planned.Message, len(plan.plan.Targets), planned.Total, planned.Free)
	}
	if expected != "" && expected != planned.Message {
		t.Fatalf("expected version %s, got %s", expected, planned.Message)
	}
	if metadataOnly {
		return
	}
	var lastReport atomic.Int64
	started := time.Now()
	report := func(event state.Event) {
		if event.Kind == state.DownloadProgress {
			elapsed := int64(time.Since(started) / time.Second)
			previous := lastReport.Load()
			if elapsed >= previous+30 && lastReport.CompareAndSwap(previous, elapsed) {
				t.Logf("transfer bytes=%d/%d rate=%d B/s elapsed=%ds", event.Downloaded, event.Total, event.Rate, elapsed)
			}
		}
	}
	req.Effect.Kind = state.EffectDownloadRun
	verified, err := backend.runDownload(ctx, req, state.Event{}, report)
	if err != nil || verified.Kind != state.DownloadVerified {
		t.Fatalf("download event=%d: %v", verified.Kind, err)
	}
	plan := backend.getPlan(root)
	if plan == nil || plan.delivery.Config.ResVersion != planned.Message || len(plan.plan.Targets) == 0 {
		t.Fatal("verified plan does not match the selected first-install manifest")
	}
	t.Logf("download and precommit verification elapsed=%s", time.Since(started))
	if _, err := os.Lstat(filepath.Join(root, "Client")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("installation tree changed before commit: %v", err)
	}
	req.Effect.Kind = state.EffectDownloadCommit
	commitStarted := time.Now()
	committed, err := backend.commitDownload(ctx, req, state.Event{}, report)
	if err != nil || committed.Kind != state.DownloadCommitted || committed.Message != planned.Message {
		t.Fatalf("commit event=%d version=%q: %v", committed.Kind, committed.Message, err)
	}
	installation, err := game.Inspect(ctx, root)
	if err != nil || installation.Branch != bootstrapBranch || installation.AppID != bootstrapAppID {
		t.Fatalf("installation was not recognized: %v", err)
	}
	store, err := backend.installStore()
	if err != nil {
		t.Fatal(err)
	}
	record, err := store.ReadInstall(root)
	if err != nil || record.Version != planned.Message || record.VerifyCoverage != "full" || len(record.Targets) != len(plan.plan.Targets) {
		t.Fatalf("published record version=%s coverage=%s targets=%d: %v", record.Version, record.VerifyCoverage, len(record.Targets), err)
	}
	if _, err := os.Lstat(filepath.Join(root, "NTELauncher")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("official launcher tree unexpectedly exists: %v", err)
	}
	t.Logf("installed version=%s targets=%d bytes=%d commit_elapsed=%s", record.Version, len(record.Targets), plan.delivery.Config.ResSize, time.Since(commitStarted))
}
