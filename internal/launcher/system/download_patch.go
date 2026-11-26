// SPDX-License-Identifier: GPL-3.0-or-later
package system

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nerv-base/Oddward/internal/gamedl"
	"github.com/nerv-base/Oddward/internal/gameinstall"
	"github.com/nerv-base/Oddward/internal/gamepatch"
	"github.com/nerv-base/Oddward/internal/gameres"
	"github.com/nerv-base/Oddward/internal/launcher/state"
	"golang.org/x/sys/windows"
)

const patchChildPurpose = "game-resource-child"

type patchTransferChoice struct {
	transfer  gamedl.Transfer
	alternate gameres.Source
}

// patchDownloadFallback retains every independently verified patch route. Only
// targets with missing/corrupt blobs switch to Full; ordinary Full transfers
// resume independently and can use their planned Pak-to-CAS alternative.
func (b *GameDownloadBackend) patchDownloadFallback(ctx context.Context, p *downloadPlan, e *gamedl.Engine, report func(state.Event), patchErr error) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if patchFailureFatal(patchErr) {
		return false, patchErr
	}
	var choices []patchTransferChoice
	byObject := map[gameres.Object]int{}
	fullTargets := map[gameres.Object]bool{}
	fallback := false
	add := func(id gameres.Object, source, alternate gameres.Source) {
		fullTargets[id] = true
		if i, ok := byObject[id]; ok {
			if choices[i].alternate.Kind == 0 && choices[i].transfer.Source != source {
				choices[i].alternate = source
			}
			return
		}
		byObject[id] = len(choices)
		choices = append(choices, patchTransferChoice{transfer: gamedl.Transfer{Object: id, Source: source}, alternate: alternate})
	}
	for _, target := range p.plan.Targets {
		if err := ctx.Err(); err != nil {
			return fallback, err
		}
		id := gameres.Object{MD5: target.MD5, Size: target.Size}
		if target.Action == gameres.ActionReuseInstalled {
			continue
		}
		ready, err := patchBlobAvailable(ctx, e, id)
		if err != nil {
			return fallback, err
		}
		if ready {
			continue
		}
		if target.Action == gameres.ActionPatch {
			viable, err := patchRouteAvailable(ctx, e, target.Route)
			if err != nil {
				return fallback, err
			}
			if viable {
				continue
			}
			if target.Fallback == nil {
				return fallback, patchErr
			}
			add(id, target.Fallback.Full, target.Fallback.Alternate)
			fallback = true
			continue
		}
		source := target.Full
		if target.Action == gameres.ActionInstallCached {
			source = gameres.Source{Kind: gameres.SourceCAS, Object: id, Size: id.Size}
		}
		if target.Fallback != nil && source.Kind == gameres.SourcePakRange {
			source = target.Fallback.Full
		}
		add(id, source, gameres.Source{})
	}
	transfers := make([]gamedl.Transfer, len(choices))
	for i, c := range choices {
		transfers[i] = c.transfer
	}
	remaining, err := e.Remaining(ctx, transfers)
	if err != nil {
		return fallback, err
	}
	rebuild, err := patchFutureWorking(ctx, p, e, fullTargets)
	if err != nil {
		return fallback, err
	}
	need, err := patchAddBytes(p.margin, remaining, rebuild)
	if err != nil {
		return fallback, err
	}
	if err = b.checkDownloadSpace(p, need); err != nil {
		return fallback, err
	}
	if fallback {
		patchReport(report, state.Event{Kind: state.DownloadFallbackNotice, Body: "fallback-full"})
	}
	for _, choice := range choices {
		if err = b.runPatchDownload(ctx, p, e, choice); err != nil {
			return fallback, err
		}
	}
	return fallback, nil
}

// patchTargets counts the plan targets reconstructed from official patches, so
// the UI can show rebuild progress before the first patch step reports.
func (p *downloadPlan) patchTargets() int {
	count := 0
	for _, target := range p.plan.Targets {
		if target.Action == gameres.ActionPatch {
			count++
		}
	}
	return count
}

func (b *GameDownloadBackend) applyDownloadPatches(ctx context.Context, p *downloadPlan, e *gamedl.Engine, report func(state.Event)) (bool, error) {
	var total int64
	seen := map[gameres.Object]bool{}
	for _, target := range p.plan.Targets {
		if target.Action != gameres.ActionPatch {
			continue
		}
		id := gameres.Object{MD5: target.MD5, Size: target.Size}
		if seen[id] {
			continue
		}
		seen[id] = true
		ready, err := patchBlobAvailable(ctx, e, id)
		if err != nil {
			return false, err
		}
		if ready {
			continue
		}
		if target.Route != nil {
			for _, step := range target.Route.Steps {
				var err error
				total, err = patchAddBytes(total, step.New.Size)
				if err != nil {
					return false, err
				}
			}
		}
	}
	var written int64
	var done, files uint16
	for _, target := range p.plan.Targets {
		if target.Action == gameres.ActionPatch {
			files++
		}
	}
	meter := newThroughputMeter()
	progress := func() {
		rate, eta := meter.Snapshot(written, total)
		patchReport(report, state.Event{Kind: state.ApplyProgress, Stage: state.StageApply, Downloaded: written, Total: total, Rate: rate, ETASecs: eta, FilesDone: done, FilesTotal: files})
	}
	progress()
	fallback := false
	for _, target := range p.plan.Targets {
		if target.Action != gameres.ActionPatch {
			continue
		}
		if err := ctx.Err(); err != nil {
			return fallback, err
		}
		id := gameres.Object{MD5: target.MD5, Size: target.Size}
		ready, err := patchBlobAvailable(ctx, e, id)
		if err != nil {
			return fallback, err
		}
		if ready {
			done++
			progress()
			continue
		}
		lastStep := -1
		var lastWritten int64
		var lastReport time.Time
		meter.Start()
		err = b.applyOneDownloadPatch(ctx, p, e, target, func(step int, n, _ int64) {
			if step != lastStep {
				lastStep = step
				lastWritten = 0
			}
			if n <= lastWritten {
				return
			}
			delta := n - lastWritten
			written += delta
			lastWritten = n
			meter.Record(delta)
			now := time.Now()
			if now.Sub(lastReport) >= 125*time.Millisecond {
				lastReport = now
				progress()
			}
		})
		if err == nil {
			done++
			progress()
			continue
		}
		if ctx.Err() != nil {
			return fallback, ctx.Err()
		}
		if patchFailureFatal(err) {
			return fallback, err
		}
		if target.Fallback == nil {
			return fallback, err
		}
		body := "fallback-full"
		if errors.Is(err, gamepatch.ErrSourceMismatch) {
			body = "patch-source-mismatch"
		}
		patchReport(report, state.Event{Kind: state.DownloadFallbackNotice, Body: body})
		fallback = true
		choice := patchTransferChoice{transfer: gamedl.Transfer{Object: id, Source: target.Fallback.Full}, alternate: target.Fallback.Alternate}
		// Network fallback has its own byte semantics. The Apply stage keeps its
		// actual local-write counters; the visible notice explains the extra work.
		if err = b.runPatchDownload(ctx, p, e, choice); err != nil {
			return fallback, err
		}
		done++
		progress()
	}
	return fallback, nil
}

func (b *GameDownloadBackend) applyOneDownloadPatch(ctx context.Context, p *downloadPlan, e *gamedl.Engine, target gameres.Target, report func(step int, written, total int64)) (result error) {
	if target.Route == nil {
		return gameres.ErrNoViableAction
	}
	var store *gameinstall.Store
	if b.DB != nil && b.StateRoot != "" {
		store, _ = b.installStore()
	}
	steps := make([]gamepatch.Step, 0, len(target.Route.Steps))
	for i, s := range target.Route.Steps {
		blob, err := e.BlobPath(s.Blob)
		if err != nil {
			return err
		}
		step := gamepatch.Step{
			PatchPath:     filepath.Join(p.engineRoot(), filepath.FromSlash(blob)),
			Old:           s.Old,
			Patch:         s.Blob,
			New:           s.New,
			PatchVerified: true,
		}
		if i == 0 {
			step.OldPath = filepath.Join(p.root, filepath.FromSlash(target.Path))
			if store != nil {
				step.OldVerified = store.TargetFingerprintMatches(p.root, gameinstall.InstallTarget{Path: target.Path, MD5: s.Old.MD5, Size: s.Old.Size})
			}
		}
		steps = append(steps, step)
	}
	working, err := patchChainWorking(target.Route)
	if err != nil {
		return err
	}
	need, err := patchAddBytes(p.margin, working)
	if err != nil {
		return err
	}
	if err = b.checkDownloadSpace(p, need); err != nil {
		return err
	}
	started := time.Now()
	output, measured, err := gamepatch.ApplyChainMeasured(ctx, steps, filepath.Join(p.engineRoot(), "cache", "tmp"), report)
	elapsed := time.Since(started)
	if err != nil {
		return err
	}
	if ioBytes, countErr := patchAddBytes(measured.SourceVerifiedBytes, measured.WrittenBytes); countErr == nil && ioBytes > 0 {
		// The planner estimates source plus output bytes. Decoder rereads,
		// patch reads and step checks are reflected in the measured wall time.
		_ = b.recordDownloadPerf(ctx, p.engineRoot(), PerfSample{RebuildIOBytes: ioBytes, RebuildDuration: elapsed})
	}
	defer func() {
		if err := os.Remove(output); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
		// Remove only this generated directory, and only if empty.
		if err := os.Remove(filepath.Dir(output)); err != nil && !errors.Is(err, os.ErrNotExist) {
			result = errors.Join(result, err)
		}
	}()
	relative, err := filepath.Rel(p.engineRoot(), output)
	if err != nil {
		return err
	}
	want := gameres.Object{MD5: target.MD5, Size: target.Size}
	if len(steps) == 0 || steps[len(steps)-1].New != want {
		return gamepatch.ErrOutputMismatch
	}
	return e.PublishVerified(ctx, want, filepath.ToSlash(relative))
}

// runPatchDownload gives each object/source pair a stable child task identity.
// Children cannot be mistaken for independently resumable installation tasks.
func (b *GameDownloadBackend) runPatchDownload(ctx context.Context, p *downloadPlan, e *gamedl.Engine, choice patchTransferChoice) error {
	sources := []gameres.Source{choice.transfer.Source}
	if choice.alternate.Kind != 0 && choice.alternate != choice.transfer.Source {
		sources = append(sources, choice.alternate)
	}
	var last error
	for _, source := range sources {
		tr := choice.transfer
		tr.Source = source
		remaining, err := e.Remaining(ctx, []gamedl.Transfer{tr})
		if err != nil {
			return err
		}
		need, err := patchAddBytes(p.margin, remaining)
		if err != nil {
			return err
		}
		if err = b.checkDownloadSpace(p, need); err != nil {
			return err
		}
		encoded, err := json.Marshal(struct {
			Object gameres.Object
			Source gameres.Source
		}{tr.Object, tr.Source})
		if err != nil {
			return err
		}
		digest := sha256.Sum256(encoded)
		id := p.id + "-" + hex.EncodeToString(digest[:12])
		if len(id) > 64 {
			return gameres.ErrNoViableAction
		}
		task := gamedl.Task{ID: id, InstallKey: p.key, Purpose: patchChildPurpose, Branch: p.delivery.Branch, TargetVersion: p.delivery.Config.ResVersion, ListHash: p.delivery.Config.ListHash, Kind: gamedl.TaskUpdateFiles, State: gamedl.StatePlanning, Transfers: []gamedl.Transfer{tr}}
		if p.kind == state.DownloadInstall {
			task.Kind = gamedl.TaskInstallFiles
		}
		if p.kind == state.DownloadPreload {
			task.Kind = gamedl.TaskPreloadFiles
		}
		last = e.Run(ctx, task, nil)
		if last == nil {
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if patchFailureFatal(last) {
			return last
		}
	}
	return last
}

func retirePatchDownloads(ctx context.Context, p *downloadPlan, e *gamedl.Engine) error {
	tasks, err := e.Store().List(ctx)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if task.Purpose != patchChildPurpose || !strings.HasPrefix(task.ID, p.id+"-") || task.InstallKey != p.key {
			continue
		}
		if err = e.Retire(ctx, task.ID); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func patchBlobAvailable(ctx context.Context, e *gamedl.Engine, id gameres.Object) (bool, error) {
	f, err := e.OpenBlob(ctx, id)
	if err == nil {
		return true, f.Close()
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	var taskErr *gamedl.TaskError
	if errors.As(err, &taskErr) && taskErr.Kind == gamedl.ErrChecksum {
		return false, nil
	}
	return false, err
}
func patchRouteAvailable(ctx context.Context, e *gamedl.Engine, route *gameres.Route) (bool, error) {
	if route == nil || len(route.Steps) == 0 {
		return false, nil
	}
	for _, step := range route.Steps {
		ready, err := patchBlobAvailable(ctx, e, step.Blob)
		if err != nil || !ready {
			return ready, err
		}
	}
	return true, nil
}
func patchChainWorking(route *gameres.Route) (int64, error) {
	if route == nil {
		return 0, nil
	}
	var peak int64
	for i, step := range route.Steps {
		working := step.New.Size
		if i > 0 {
			var err error
			working, err = patchAddBytes(working, step.Old.Size)
			if err != nil {
				return 0, err
			}
		}
		if working < 0 {
			return 0, gameres.ErrInsufficientSpace
		}
		peak = max(peak, working)
	}
	return peak, nil
}
func patchFutureWorking(ctx context.Context, p *downloadPlan, e *gamedl.Engine, full map[gameres.Object]bool) (int64, error) {
	var peak, retained int64
	seen := map[gameres.Object]bool{}
	for _, target := range p.plan.Targets {
		id := gameres.Object{MD5: target.MD5, Size: target.Size}
		if target.Action != gameres.ActionPatch || full[id] || seen[id] {
			continue
		}
		seen[id] = true
		ready, err := patchBlobAvailable(ctx, e, id)
		if err != nil {
			return 0, err
		}
		if ready {
			continue
		}
		working, err := patchChainWorking(target.Route)
		if err != nil {
			return 0, err
		}
		combined, err := patchAddBytes(retained, working)
		if err != nil {
			return 0, err
		}
		peak = max(peak, combined)
		retained, err = patchAddBytes(retained, id.Size)
		if err != nil {
			return 0, err
		}
	}
	return max(peak, retained), nil
}
func patchAddBytes(values ...int64) (int64, error) {
	var total int64
	for _, n := range values {
		if n < 0 || n > math.MaxInt64-total {
			return 0, gameres.ErrInsufficientSpace
		}
		total += n
	}
	return total, nil
}
func patchFailureFatal(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, os.ErrPermission) || errors.Is(err, windows.ERROR_DISK_FULL) || errors.Is(err, windows.ERROR_HANDLE_DISK_FULL) || errors.Is(err, gameres.ErrInsufficientSpace) {
		return true
	}
	var taskErr *gamedl.TaskError
	if errors.As(err, &taskErr) {
		switch taskErr.Kind {
		case gamedl.ErrNetwork, gamedl.ErrHTTP, gamedl.ErrRangeMismatch, gamedl.ErrChecksum, gamedl.ErrPatchRejected, gamedl.ErrPatchSourceMismatch, gamedl.ErrPatchBlobMissing, gamedl.ErrRebuildVerifyFailed, gamedl.ErrPatchUnsupported, gamedl.ErrRangeRejected:
			return false
		default:
			return true
		}
	}
	for _, recoverable := range []error{os.ErrNotExist, gamepatch.ErrSourceMismatch, gamepatch.ErrPatchMismatch, gamepatch.ErrOutputMismatch, gamepatch.ErrUnsupportedPatch, gamepatch.ErrCorruptPatch, gamepatch.ErrInvalidChain} {
		if errors.Is(err, recoverable) {
			return false
		}
	}
	return true
}
func patchReport(report func(state.Event), event state.Event) {
	if report != nil {
		report(event)
	}
}
