// SPDX-License-Identifier: GPL-3.0-or-later
package system

import (
	"context"
	"github.com/nerv-base/Oddward/internal/database/dbtest"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/gameinstall"
	"github.com/nerv-base/Oddward/internal/gameres"
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
	if err = b.preloadRecoveredCommit(ctx, root, uncommitted); err != nil {
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
	if err = b.preloadRecoveredCommit(ctx, root, published); err != nil {
		t.Fatal(err)
	}
	loaded, err = store.Load(ctx, r.Branch, r.Version)
	if err != nil || loaded.State != gameinstall.PreloadCompleted {
		t.Fatalf("recovered publication: %+v %v", loaded, err)
	}
}
