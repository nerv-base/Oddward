// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nerv-base/Oddward/internal/gamedl"
	"github.com/nerv-base/Oddward/internal/gameinstall"
	"github.com/nerv-base/Oddward/internal/gameres"
	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func (b *GameDownloadBackend) commitDownload(ctx context.Context, req launcher.DownloadRequest, event state.Event, report func(state.Event)) (state.Event, error) {
	event.Kind = state.DownloadRecoveryNeeded
	p := b.getPlan(req.Root)
	if p == nil {
		return event, errDownloadPlanMissing
	}
	guard := func(ctx context.Context) error { return b.guard(ctx, true) }
	if err := guard(ctx); err != nil {
		return event, err
	}
	exclusion, err := acquireDownloadMutation(p.root)
	if err != nil {
		return event, err
	}
	defer exclusion.Close()
	lease, err := gamedl.Acquire(downloadWork(p.root))
	if err != nil {
		return event, err
	}
	defer lease.Close()
	if err = b.checkPlanBase(ctx, p); err != nil {
		return event, err
	}
	if p.kind == state.DownloadPreload {
		if err = b.preloadBeforeApply(ctx, p); err != nil {
			return event, err
		}
	}
	engine, err := b.openPlanEngine(ctx, p, lease)
	if err != nil {
		return event, err
	}
	defer engine.Close()
	store, err := b.installStore()
	if err != nil {
		return event, err
	}
	candidate := gameinstall.InstallRecord{
		Schema: 2, InstallKey: p.key, Root: p.root, Mode: "oddward", State: "ready", Branch: p.delivery.Branch, Version: p.delivery.Config.ResVersion, ListHash: p.delivery.Config.ListHash,
		ManifestDigest: p.delivery.Config.ListHash, CreatedAt: time.Now().UTC(), VerifyCoverage: "none",
	}
	if p.kind == state.DownloadInstall {
		candidate.Mode = "fresh"
	}
	input := gameinstall.TxnInput{Root: p.root, WorkRoot: downloadWork(p.root), TxnID: p.id, Guard: guard, BackupRetention: 24 * time.Hour,
		FreeBytes: func(context.Context) (int64, error) { return b.space(p.root) }, RequiredFreeBytes: p.margin,
		Repair: p.base.Root != "" && p.base.Version == candidate.Version && p.base.ListHash == candidate.ListHash,
	}
	// Reused installation files and all staged sources are verified again by
	// Commit before any replacement. Move a shared blob only on its last use.
	remaining := make(map[gameres.Object]int)
	for _, t := range p.plan.Targets {
		if t.Action != gameres.ActionReuseInstalled {
			remaining[gameres.Object{MD5: t.MD5, Size: t.Size}]++
		}
	}
	prefix := "."
	if p.externalCache != "" {
		input.SourceRoot = p.engineRoot()
	} else {
		prefix, err = filepath.Rel(input.WorkRoot, p.engineRoot())
		if err != nil || strings.HasPrefix(prefix, "..") {
			return event, gameres.ErrPathUnsafe
		}
	}
	for _, t := range p.plan.Targets {
		file := gameinstall.TxnFile{Path: t.Path, MD5: t.MD5, Size: t.Size, Origin: "installed"}
		object := gameres.Object{MD5: t.MD5, Size: t.Size}
		if t.Action != gameres.ActionReuseInstalled {
			path, err := engine.BlobSource(ctx, object)
			if err != nil {
				return event, err
			}
			file.Source = filepath.ToSlash(filepath.Join(prefix, filepath.FromSlash(path)))
			file.Origin = "blob"
			remaining[object]--
			file.Retain = input.SourceRoot == "" && remaining[object] > 0
			if file.Retain {
				input.RequiredFreeBytes += file.Size
			}
		}
		input.Files = append(input.Files, file)
		candidate.Targets = append(candidate.Targets, gameinstall.InstallTarget{Path: file.Path, MD5: file.MD5, Size: file.Size})
	}
	input.Candidate = candidate
	if p.kind == state.DownloadInstall {
		input.Bootstrap, err = b.stageBootstrap(ctx, p)
		if err != nil {
			return event, err
		}
	}
	if p.kind == state.DownloadPreload {
		if err := b.preloadApplying(ctx, p); err != nil {
			return event, err
		}
	}
	report(state.Event{Kind: state.DownloadCommitStart})
	status, err := store.Commit(ctx, input)
	if err != nil && !(status.Published && !status.LaunchBlocked && errors.Is(err, gameinstall.ErrCleanupPending)) {
		if p.kind == state.DownloadPreload {
			err = errors.Join(err, b.preloadApplyFailed(context.WithoutCancel(ctx), p))
		}
		return event, err
	}
	if !status.Published || status.LaunchBlocked {
		return event, gameinstall.ErrRecoveryRequired
	}
	event.Kind, event.Message = state.DownloadCommitted, candidate.Version
	if p.kind == state.DownloadPreload {
		event.Kind = state.PreloadApplied
		if err = b.preloadCommitted(ctx, p); err != nil {
			return event, err
		}
	}
	// The installation record is already published. Cache retirement and
	// collection are maintenance, so a failure here cannot undo the commit.
	if e := retirePatchDownloads(ctx, p, engine); e == nil {
		_ = engine.Retire(ctx, p.id)
	}
	_ = engine.Close()
	b.putPlan(p.root, nil)
	b.collectDownloadCache(ctx, p, lease)
	return event, nil
}

func (b *GameDownloadBackend) stageBootstrap(ctx context.Context, p *downloadPlan) ([]gameinstall.TxnFile, error) {
	branch := p.delivery.Config.UpdateBranch
	if branch == "" {
		branch = bootstrapUpdateBranch
	}
	if _, err := gameres.ConfigURL(p.delivery.ConfigResponse.Root, branch); err != nil {
		return nil, err
	}
	cdn := []string{p.delivery.ConfigResponse.Root}
	config := struct {
		AppID        string   `json:"appId"`
		AppVersion   string   `json:"appVersion"`
		MinVersion   string   `json:"minVersion"`
		Branch       string   `json:"branchName"`
		UpdateBranch string   `json:"updateBranchName"`
		ResourceURLs []string `json:"gameResUrl"`
		AssetURLs    []string `json:"gameUpdateAssetUrl"`
	}{bootstrapAppID, "0.0", "0.0", p.delivery.Branch, branch, cdn, cdn}
	provenance := struct {
		URL          string    `json:"url"`
		Observed     time.Time `json:"observedAt"`
		ETag         string    `json:"etag,omitempty"`
		LastModified string    `json:"lastModified,omitempty"`
		ListHash     string    `json:"listHash"`
		Version      string    `json:"resVersion"`
	}{p.delivery.ConfigResponse.URL, p.delivery.ConfigResponse.ObservedAt, p.delivery.ConfigResponse.ETag, p.delivery.ConfigResponse.LastModified, p.delivery.Config.ListHash, p.delivery.Config.ResVersion}
	root, err := os.OpenRoot(downloadWork(p.root))
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if err := root.Mkdir("bootstrap", 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return nil, err
	}
	if _, err := downloadDirectory(filepath.Join(downloadWork(p.root), "bootstrap"), false); err != nil {
		return nil, err
	}
	scratch, err := root.OpenRoot("bootstrap")
	if err != nil {
		return nil, err
	}
	defer scratch.Close()
	return stageBootstrapFiles(ctx, scratch, p.id, config, provenance)
}

func stageBootstrapFiles(ctx context.Context, scratch *os.Root, taskID string, config, provenance any) ([]gameinstall.TxnFile, error) {
	var files []gameinstall.TxnFile
	for _, item := range []struct {
		name  string
		value any
	}{{"Patch_Windows.json", config}, {"Patch_Windows.source.json", provenance}} {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		payload, err := json.Marshal(item.value)
		if err != nil {
			return nil, err
		}
		hash := md5.Sum(payload)
		digest := hex.EncodeToString(hash[:])
		name := taskID + "-" + item.name + "-" + digest
		if err := stageBootstrapFile(scratch, name, payload); err != nil {
			return nil, err
		}
		files = append(files, gameinstall.TxnFile{Path: downloadContent + "PatcherConfig/" + item.name, Source: "bootstrap/" + name, MD5: digest, Size: int64(len(payload)), Origin: "bootstrap"})
	}
	return files, nil
}

// A failed preflight can leave verified bootstrap files in place. Publish each
// source only after it is complete so a retry may safely reuse identical bytes.
func stageBootstrapFile(scratch *os.Root, name string, payload []byte) error {
	info, err := scratch.Lstat(name)
	if err == nil {
		if !info.Mode().IsRegular() || downloadReparse(info) || info.Size() != int64(len(payload)) {
			return gameres.ErrPathUnsafe
		}
		existing, err := scratch.Open(name)
		if err != nil {
			return err
		}
		defer existing.Close()
		opened, err := existing.Stat()
		if err != nil {
			return err
		}
		if !os.SameFile(info, opened) {
			return gameres.ErrPathUnsafe
		}
		data, err := io.ReadAll(io.LimitReader(existing, int64(len(payload))+1))
		if err != nil {
			return err
		}
		if !bytes.Equal(data, payload) {
			return errors.New("staged bootstrap identity mismatch")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary := "bootstrap-" + rand.Text() + ".tmp"
	f, err := scratch.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer scratch.Remove(temporary)
	_, writeErr := f.Write(payload)
	if writeErr == nil {
		writeErr = f.Sync()
	}
	if err := errors.Join(writeErr, f.Close()); err != nil {
		return err
	}
	return scratch.Rename(temporary, name)
}
