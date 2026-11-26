// SPDX-License-Identifier: GPL-3.0-or-later
package system

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/database/dbtest"
	"github.com/nerv-base/Oddward/internal/gameinstall"
	"github.com/nerv-base/Oddward/internal/gameres"
	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func TestSystemPreloadApplyRequiresPublicationProofAndHandlesSetupFailure(t *testing.T) {
	ctx := context.Background()
	b := &GameDownloadBackend{DB: dbtest.Open(t), StateRoot: t.TempDir()}
	root := preloadSystemBase(t, b)
	d := preloadSystemDelivery(t, "https://synthetic.invalid/clientRes")
	_, snapshot, err := downloadPreloadEvidence(d, []string{d.ConfigResponse.Root}, true)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := gameinstall.StartPreload(ctx, gameinstall.PreloadInput{Advertisement: gameres.Config{PreReleaseStatus: "start", PreReleaseBranch: d.Branch, PreReleaseVersion: d.Config.ResVersion}, Snapshot: snapshot, CurrentBranch: "PC_150", CurrentVersion: "1.0.0", BaselineTrusted: true, Budget: gameres.BudgetInput{VolumeFreeBytes: 8 << 30, CacheFreeBytes: 8 << 30}, Now: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	store, err := b.preloadStore(root)
	if err != nil {
		t.Fatal(err)
	}
	r := plan.Record
	r.State = gameinstall.PreloadApplicable
	if err = store.Save(ctx, r); err != nil {
		t.Fatal(err)
	}
	key, err := gameinstall.InstallKey(root)
	if err != nil {
		t.Fatal(err)
	}
	p := &downloadPlan{root: root, key: key, id: "synthetic-apply", kind: state.DownloadPreload, preload: &r, delivery: d, plan: plan.Plan}
	lease, err := preloadSystemRootLease(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Close()
	if err = b.preloadApplying(ctx, p); err != nil {
		t.Fatal(err)
	}
	install, err := b.installStore()
	if err != nil {
		t.Fatal(err)
	}
	base, err := install.ReadInstall(root)
	if err != nil {
		t.Fatal(err)
	}
	// Same version, even with a matching nonce, cannot invent a publication.
	uncommitted := base
	uncommitted.Version = r.Version
	uncommitted.TxnID = p.id
	if _, err = b.preloadRecoveredCommit(ctx, root, uncommitted); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(ctx, r.Branch, r.Version)
	if err != nil || loaded.State != gameinstall.PreloadApplying {
		t.Fatalf("version-only publication: %+v %v", loaded, err)
	}
	if err = b.preloadApplyFailed(ctx, p); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.Load(ctx, r.Branch, r.Version)
	if err != nil || loaded.State != gameinstall.PreloadStale {
		t.Fatalf("setup failure stuck applying: %+v %v", loaded, err)
	}
	loaded.State = gameinstall.PreloadApplying
	if err = store.Save(ctx, loaded); err != nil {
		t.Fatal(err)
	}
	published := base
	published.State = "ready"
	published.TxnID = p.id
	published.ListHash = r.ListHash
	published.Targets = nil
	for _, target := range r.Targets {
		published.Targets = append(published.Targets, gameinstall.InstallTarget{Path: target.Path, MD5: target.Object.MD5, Size: target.Object.Size})
	}
	if completed, err := b.preloadRecoveredCommit(ctx, root, published); err != nil || !completed {
		t.Fatalf("published record was not completed: %v %v", completed, err)
	}
	loaded, err = store.Load(ctx, r.Branch, r.Version)
	if err != nil || loaded.State != gameinstall.PreloadCompleted {
		t.Fatalf("recovered publication: %+v %v", loaded, err)
	}
}

// A preload failure before the transaction exists leaves the installation and
// its record untouched, so it must stay retryable rather than demand recovery.
func TestSystemPreloadSetupFailureBeforeTransactionStaysRetryable(t *testing.T) {
	ctx := context.Background()
	b := &GameDownloadBackend{DB: dbtest.Open(t), StateRoot: t.TempDir(), free: func(string) (int64, error) { return 8 << 30, nil }, processes: func(context.Context) (downloadProcesses, error) { return downloadProcesses{}, nil }}
	root := preloadSystemBase(t, b)
	d := preloadSystemDelivery(t, "https://synthetic.invalid/clientRes")
	_, snapshot, err := downloadPreloadEvidence(d, []string{d.ConfigResponse.Root}, true)
	if err != nil {
		t.Fatal(err)
	}
	planned, err := gameinstall.StartPreload(ctx, gameinstall.PreloadInput{Advertisement: gameres.Config{PreReleaseStatus: "start", PreReleaseBranch: d.Branch, PreReleaseVersion: d.Config.ResVersion}, Snapshot: snapshot, CurrentBranch: "PC_150", CurrentVersion: "1.0.0", BaselineTrusted: true, Budget: gameres.BudgetInput{VolumeFreeBytes: 8 << 30, CacheFreeBytes: 8 << 30}, Now: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	store, err := b.preloadStore(root)
	if err != nil {
		t.Fatal(err)
	}
	// Stale, not applicable: preloadBeforeApply refuses before any journal exists.
	r := planned.Record
	r.State = gameinstall.PreloadStale
	if err = store.Save(ctx, r); err != nil {
		t.Fatal(err)
	}
	install, err := b.installStore()
	if err != nil {
		t.Fatal(err)
	}
	base, err := install.ReadInstall(root)
	if err != nil {
		t.Fatal(err)
	}
	key, err := gameinstall.InstallKey(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(downloadWork(root), 0700); err != nil {
		t.Fatal(err)
	}
	b.putPlan(root, &downloadPlan{root: root, key: key, id: "synthetic-retry", kind: state.DownloadPreload, delivery: d, base: base, preload: &r, plan: planned.Plan, workRoot: preloadSystemWork(root, r)})
	req := launcher.DownloadRequest{Root: root, Kind: state.DownloadPreload, Effect: state.Effect{Kind: state.EffectPreloadApply, Token: 1}}
	if event := b.HandleDownload(ctx, req, nil); event.Kind != state.PreloadFailed {
		t.Fatalf("pre-transaction preload failure = %+v, want PreloadFailed", event)
	}
	loaded, err := store.Load(ctx, r.Branch, r.Version)
	if err != nil || loaded.State != gameinstall.PreloadStale {
		t.Fatalf("retryable preload record changed: %+v %v", loaded, err)
	}
}

// An unreadable Patch_Windows.json must abort bootstrap instead of silently
// replacing configuration (server-list URLs) that the installer cannot rebuild.
func TestSystemStageBootstrapKeepsUnreadablePatchConfig(t *testing.T) {
	ctx := context.Background()
	b := &GameDownloadBackend{DB: dbtest.Open(t), StateRoot: t.TempDir()}
	root := t.TempDir()
	const configPath = "Client/WindowsNoEditor/PatcherConfig/Patch_Windows.json"
	corrupt := []byte(`{"appId":"1289",`)
	preloadSystemPut(t, root, configPath, corrupt)
	if err := os.MkdirAll(downloadWork(root), 0700); err != nil {
		t.Fatal(err)
	}
	p := &downloadPlan{
		root: root, key: "synthetic", id: "synthetic-bootstrap", kind: state.DownloadUpdate,
		base:     gameinstall.InstallRecord{Root: root, Branch: "PC_140"},
		delivery: gameDelivery{Branch: "PC_140", Config: gameres.Config{ResVersion: "1.5.0", ListHash: strings.Repeat("a", 32)}, ConfigResponse: downloadResponse{Root: "https://synthetic.invalid/clientRes", URL: "https://synthetic.invalid/clientRes/PC_140/Version/Windows/config.xml"}},
	}
	if _, err := b.stageBootstrap(ctx, p); !errors.Is(err, errDownloadBootstrapConfig) {
		t.Fatalf("corrupt Patch_Windows.json was not rejected: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(configPath)))
	if err != nil || !bytes.Equal(data, corrupt) {
		t.Fatalf("existing configuration was modified: %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(downloadWork(root), "bootstrap")); !os.IsNotExist(err) {
		t.Fatalf("bootstrap staging ran before rejecting the configuration: %v", err)
	}
}

func TestSystemMergeBranchBootstrapOnlyToleratesMissingConfig(t *testing.T) {
	root := t.TempDir()
	merged, ok, err := mergeBranchBootstrap(root, "PC_150", "publish_Updata", []string{"https://synthetic.invalid/clientRes"})
	if err != nil || ok || merged != nil {
		t.Fatalf("missing config must allow the template: %+v %v %v", merged, ok, err)
	}
	preloadSystemPut(t, root, "Client/WindowsNoEditor/PatcherConfig/Patch_Windows.json", []byte("not json"))
	if merged, ok, err = mergeBranchBootstrap(root, "PC_150", "publish_Updata", nil); !errors.Is(err, errDownloadBootstrapConfig) || ok || merged != nil {
		t.Fatalf("malformed config was merged: %+v %v %v", merged, ok, err)
	}
}
