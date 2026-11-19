// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nerv-base/Oddward/internal/gamedl"
	"github.com/nerv-base/Oddward/internal/gameinstall"
	"github.com/nerv-base/Oddward/internal/gameres"
	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/launcher/state"
	"github.com/nerv-base/Oddward/internal/launchsettings"
)

const (
	patchApplyAdmitted        = true // SPEC Phase 5B D1–D6 verified with official samples and same-update Full fallback.
	defaultFilesInFlight      = 4
	defaultConnectionsPerFile = 4
)

func downloadWork(root string) string { return filepath.Join(root, ".oddward") }
func (b *GameDownloadBackend) installStore() (*gameinstall.Store, error) {
	return gameinstall.NewStore(filepath.Join(b.StateRoot, "installs"))
}
func (b *GameDownloadBackend) taskRoot(key string) string {
	return filepath.Join(b.StateRoot, "installs", key)
}
func (b *GameDownloadBackend) ensureDownloadTaskRoot(key string) error {
	if _, err := b.installStore(); err != nil {
		return err
	}
	root, err := os.OpenRoot(filepath.Join(b.StateRoot, "installs"))
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.Mkdir(key, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return err
	}
	_, err = downloadDirectory(b.taskRoot(key), false)
	return err
}

func (b *GameDownloadBackend) preferences(ctx context.Context) (launchsettings.Downloads, error) {
	defaults := launchsettings.DefaultDownloads()
	if b.Settings == nil {
		return defaults, nil
	}
	stored, err := b.Settings.Load(ctx)
	if err != nil {
		return defaults, err
	}
	if stored.Downloads != (launchsettings.Downloads{}) {
		return stored.Downloads, nil
	}
	return defaults, nil
}
func (b *GameDownloadBackend) newDownloadEngine(ctx context.Context, p *downloadPlan, lease *gamedl.Lease) (*gamedl.Engine, error) {
	prefs, err := b.preferences(ctx)
	if err != nil {
		return nil, err
	}
	if err := b.ensureDownloadTaskRoot(p.key); err != nil {
		return nil, err
	}
	metadata := b.metadata()
	params := gamedl.Parameters{FilesInFlight: defaultFilesInFlight, ConnectionsPerFile: defaultConnectionsPerFile, RateLimitKiB: int64(prefs.RateLimitKiB)}
	return gamedl.New(gamedl.Options{
		WorkRoot: p.engineRoot(), StateRoot: b.taskRoot(p.key), Client: metadata.client, Resolve: metadata.resolve(p.delivery.Branch), Lease: lease,
		Parameters: params, UserAgent: "Oddward/" + b.Version,
	})
}

type downloadLocal struct {
	installed map[string]gameres.Object
	cached    map[gameres.Object]bool
}

func (l downloadLocal) Identity(path string) (gameres.Object, bool) {
	v, ok := l.installed[strings.ToUpper(path)]
	return v, ok
}
func (l downloadLocal) Trusted(path string, want gameres.Object) bool {
	got, ok := l.Identity(path)
	return ok && got == want
}
func (l downloadLocal) Cached(o gameres.Object) bool { return l.cached[o] }

func canonicalDownloadManifest(manifest gameres.ResList) (gameres.ResList, error) {
	files := append([]gameres.File(nil), manifest.Files...)
	packages := append([]gameres.Package(nil), manifest.Packages...)
	var err error
	for i := range files {
		files[i].Path, err = downloadTargetPath(files[i].Path)
		if err != nil {
			return gameres.ResList{}, err
		}
	}
	for i := range packages {
		packages[i].Entries = append([]gameres.Entry(nil), packages[i].Entries...)
		for j := range packages[i].Entries {
			packages[i].Entries[j].Path, err = downloadTargetPath(packages[i].Entries[j].Path)
			if err != nil {
				return gameres.ResList{}, err
			}
		}
	}
	manifest.Files, manifest.Packages = files, packages
	return manifest, nil
}

func (b *GameDownloadBackend) planDownload(ctx context.Context, req launcher.DownloadRequest, event state.Event, report func(state.Event)) (state.Event, error) {
	if req.Kind == state.DownloadPreload {
		return b.planPreload(ctx, req, event, report)
	}
	event.Kind = state.DownloadPlanFailed
	if err := b.guard(ctx, false); err != nil {
		return event, err
	}
	store, err := b.installStore()
	if err != nil {
		return event, err
	}
	var base gameinstall.InstallRecord
	root, branch := req.Root, bootstrapBranch
	if req.Kind == state.DownloadInstall {
		root, err = downloadDirectory(root, true)
		if err != nil {
			return event, err
		}
		base, err = store.ReadInstall(root)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return event, err
		}
		if base.Root != "" {
			return event, gameinstall.ErrRecoveryRequired
		}
		if err = checkFreshDownloadTree(root); err != nil {
			return event, err
		}
	} else {
		root, err = downloadDirectory(root, false)
		if err != nil {
			return event, err
		}
		base, err = store.ReadInstall(root)
		if errors.Is(err, os.ErrNotExist) {
			return event, errors.New("请先接管并校验现有游戏安装")
		}
		if err != nil {
			return event, err
		}
		if base.BaselineMismatch || len(base.Targets) == 0 {
			return event, errors.New("现有安装缺少可验证的资源清单，请重新接管")
		}
		branch = base.Branch
	}
	delivery, err := b.metadata().delivery(ctx, branch, "")
	if err != nil {
		return event, err
	}
	if base.Root != "" && (base.Version != delivery.Config.ResVersion || base.ListHash != delivery.Config.ListHash) && base.VerifyCoverage != "full" {
		return event, errors.New("更新前请完成现有安装的全量校验")
	}
	cache := ""
	if req.Kind == state.DownloadInstall {
		cache = req.CacheRoot
	}
	p, err := b.buildDownloadPlan(ctx, root, req.Kind, delivery, base, cache)
	if err != nil {
		return event, err
	}
	return b.presentDownloadPlan(p, event, report)
}

func downloadCacheCandidates(manifest gameres.ResList, graph *gameres.PatchGraph) map[gameres.Object]bool {
	candidates := map[gameres.Object]bool{}
	for _, f := range manifest.Files {
		candidates[gameres.Object{MD5: f.MD5, Size: f.Size}] = true
	}
	for _, pak := range manifest.Packages {
		for _, e := range pak.Entries {
			candidates[gameres.Object{MD5: e.MD5, Size: e.Size}] = true
		}
	}
	if graph != nil {
		for _, edge := range graph.Edges {
			if edge.Section == "" {
				candidates[edge.Blob] = true
			}
		}
	}
	return candidates
}

func (b *GameDownloadBackend) buildDownloadPlan(ctx context.Context, root string, kind state.DownloadKind, delivery gameDelivery, base gameinstall.InstallRecord, cache string) (*downloadPlan, error) {
	manifest, err := canonicalDownloadManifest(delivery.Manifest)
	if err != nil {
		return nil, err
	}
	key, err := gameinstall.InstallKey(root)
	if err != nil {
		return nil, err
	}
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	p := &downloadPlan{root: root, key: key, id: hex.EncodeToString(nonce[:]), kind: kind, delivery: delivery, base: base}
	if cache != "" {
		if kind != state.DownloadInstall || base.Root != "" {
			return nil, gameres.ErrPathUnsafe
		}
		p.externalCache, p.workRoot, err = b.externalDownloadCache(root, cache, key)
		if err != nil {
			return nil, err
		}
	}
	local := downloadLocal{installed: map[string]gameres.Object{}, cached: map[gameres.Object]bool{}}
	if base.Root != "" {
		store, err := b.installStore()
		if err != nil {
			return nil, err
		}
		trusted, err := store.TrustedTargets(ctx, root)
		if err != nil {
			return nil, err
		}
		for _, target := range trusted {
			local.installed[strings.ToUpper(target.Path)] = gameres.Object{MD5: target.MD5, Size: target.Size}
		}
	}
	caps := gameres.Capabilities{PatchApply: patchApplyAdmitted}
	var cacheGraph *gameres.PatchGraph
	if caps.PatchApply && len(local.installed) > 0 {
		cacheGraph = delivery.Graph
	}
	if _, err = os.Lstat(p.engineRoot()); err == nil {
		engine, err := b.newDownloadEngine(ctx, p, nil)
		if err != nil {
			return nil, err
		}
		defer engine.Close()
		for object := range downloadCacheCandidates(manifest, cacheGraph) {
			if err = ctx.Err(); err != nil {
				return nil, err
			}
			blob, err := engine.OpenBlob(ctx, object)
			if err == nil {
				local.cached[object] = true
				blob.Close()
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	space, err := b.space(root)
	if err != nil {
		return nil, err
	}
	margin := max(2<<30, delivery.Config.ResSize/50)
	rates, _ := b.downloadPerfRates(ctx, root)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	budget := gameres.BudgetInput{
		VolumeFreeBytes: space, CacheFreeBytes: space, ReserveBytes: margin, BackupRetention: 24 * time.Hour,
		NetBytesPerSecond: rates.NetBytesPerSecond, VerifyBytesPerSecond: rates.HashBytesPerSecond, IOBytesPerSecond: rates.IOBytesPerSecond,
	}
	objects := downloadProbeCandidates(manifest, delivery.Graph, local, caps.PatchApply)
	availability := b.downloadAvailability(ctx, delivery.Branch, objects, time.Now())
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.plan, err = gameres.BuildPlan(&manifest, delivery.Graph, local, caps, availability, budget)
	if errors.Is(err, gameres.ErrInsufficientSpace) {
		budget.VolumeFreeBytes, budget.CacheFreeBytes = math.MaxInt64, math.MaxInt64
		p.plan, err = gameres.BuildPlan(&manifest, delivery.Graph, local, caps, availability, budget)
	}
	if err != nil {
		return nil, err
	}
	p.need, p.free, p.margin = p.plan.PeakAllocBytes+margin, space, margin
	if p.externalCache != "" {
		if err := b.budgetExternalDownload(p); err != nil {
			return nil, err
		}
	}
	return p, nil
}

func (b *GameDownloadBackend) presentDownloadPlan(p *downloadPlan, event state.Event, report func(state.Event)) (state.Event, error) {
	b.putPlan(p.root, p)
	var patch, full bool
	for _, target := range p.plan.Targets {
		patch = patch || target.Action == gameres.ActionPatch
		full = full || target.Action == gameres.ActionFull
	}
	route := state.RouteAllReuse
	switch {
	case patch && full:
		route = state.RouteMixed
	case patch:
		route = state.RoutePatch
	case full:
		route = state.RouteFull
	}
	report(state.Event{Kind: state.DownloadRoutePlanned, Index: uint8(route), Total: p.plan.DownloadBytes, PatchBytes: p.plan.PatchBytes, Reused: p.plan.ReusedBytes})
	event.Kind = state.DownloadPlanned
	event.Message = p.delivery.Config.ResVersion
	event.Total, event.Reused, event.FilesTotal = p.plan.DownloadBytes, p.plan.ReusedBytes, uint16(len(p.plan.Targets))
	event.Needed, event.Free, event.Enabled = p.need, p.free, p.need > p.free
	if p.externalCache != "" {
		event.Body, event.Focused = "cross-volume", p.cacheBudget
	}
	return event, nil
}

func checkFreshDownloadTree(root string) error {
	dir, err := os.Open(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer dir.Close()
	names, err := dir.Readdirnames(2)
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if len(names) == 0 {
		return nil
	}
	if len(names) == 1 && names[0] == ".oddward" {
		_, err := downloadDirectory(downloadWork(root), false)
		return err
	}
	return errors.New("首次安装要求空目录")
}
