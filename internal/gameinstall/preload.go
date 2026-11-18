// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"context"
	"errors"
	"fmt"
	"github.com/nerv-base/Oddward/internal/gameres"
	"regexp"
	"strings"
	"time"
)

type PreloadState string

const (
	PreloadUnavailable      PreloadState = "unavailable"
	PreloadAvailable        PreloadState = "available"
	PreloadPlanning         PreloadState = "planning"
	PreloadDownloading      PreloadState = "downloading"
	PreloadPaused           PreloadState = "paused"
	PreloadVerifying        PreloadState = "verifying"
	PreloadStaged           PreloadState = "staged"
	PreloadWaitingRelease   PreloadState = "waitingRelease"
	PreloadReconcile        PreloadState = "reconcile"
	PreloadApplicable       PreloadState = "applicable"
	PreloadApplying         PreloadState = "applying"
	PreloadCompleted        PreloadState = "completed"
	PreloadStale            PreloadState = "stale"
	PreloadRecoveryRequired PreloadState = "recoveryRequired"
	PreloadFailed           PreloadState = "failed"
)

type PreloadRoute string

const (
	PreloadRouteFull  PreloadRoute = "Full"
	PreloadRoutePatch PreloadRoute = "Patch"
	PreloadRouteMixed PreloadRoute = "Mixed"
)

type PreloadBlobKind string

const (
	PreloadBlobFull  PreloadBlobKind = "full"
	PreloadBlobPatch PreloadBlobKind = "patch"
)

type PreloadFileState string

const (
	PreloadFileDownloading PreloadFileState = "downloading"
	PreloadFileStaged      PreloadFileState = "staged"
)

type PreloadFile struct {
	Path                 string           `json:"path,omitempty"`
	Object               gameres.Object   `json:"object"`
	Kind                 PreloadBlobKind  `json:"kind"`
	State                PreloadFileState `json:"state"`
	VerificationFailures int              `json:"verificationFailures,omitempty"`
}
type PreloadTarget struct {
	Path   string               `json:"path"`
	Object gameres.Object       `json:"object"`
	Action gameres.ActionKind   `json:"action"`
	Source gameres.Object       `json:"source,omitempty"`
	Steps  []gameres.PatchEntry `json:"steps,omitempty"`
}
type PreloadBudget struct {
	DownloadBytes  int64 `json:"downloadBytes"`
	FullBytes      int64 `json:"fullBytes"`
	PatchBytes     int64 `json:"patchBytes"`
	ReusedBytes    int64 `json:"reusedBytes"`
	PeakAllocBytes int64 `json:"peakAllocBytes"`
	ReserveBytes   int64 `json:"reserveBytes"`
}
type PreloadRecord struct {
	TaskID                 string           `json:"taskID,omitempty"`
	ApplyTxnID             string           `json:"applyTxnID,omitempty"`
	ObservedFormalVersions []string         `json:"observedFormalVersions,omitempty"`
	Budget                 PreloadBudget    `json:"budget"`
	Schema                 int              `json:"schema"`
	State                  PreloadState     `json:"state"`
	Branch                 string           `json:"branch"`
	Version                string           `json:"version"`
	ListHash               string           `json:"listHash"`
	Route                  PreloadRoute     `json:"route"`
	SavedAt                time.Time        `json:"savedAt"`
	UpdatedAt              time.Time        `json:"updatedAt"`
	TargetVersionSeenAt    time.Time        `json:"targetVersionSeenAt"`
	Targets                []PreloadTarget  `json:"targets"`
	Files                  []PreloadFile    `json:"files"`
	Reused                 []PreloadTarget  `json:"reused,omitempty"`
	Patches                []gameres.Object `json:"patches,omitempty"`
}

// PreloadManifest is evidence supplied by the system after authenticating the
// official origin and verifying the manifest against Config.ListHash. RouteKnown
// means the route was observed, never guessed from an advertisement or version.
type PreloadManifest struct {
	Branch                                      string
	Config                                      gameres.Config
	Manifest                                    *gameres.ResList
	Graph                                       *gameres.PatchGraph
	Verified, RouteKnown, SameOrigin, Available bool
}
type PreloadInput struct {
	Advertisement                    gameres.Config
	Snapshot                         PreloadManifest
	CurrentBranch, CurrentVersion    string
	BaselineTrusted, OfficialRunning bool
	Local                            gameres.Local
	Capabilities                     gameres.Capabilities
	Availability                     gameres.AvailabilitySnapshot
	Budget                           gameres.BudgetInput
	Now                              time.Time
}
type PreloadVerdict struct {
	State                   PreloadState
	Branch, Version, Reason string
	Notes                   []string
}
type PreloadPlan struct {
	Plan              gameres.Plan
	Record            PreloadRecord
	Verdict           PreloadVerdict
	FullDownloadBytes int64
}

var ErrPreloadBlocked = errors.New("preload blocked")
var preloadBranchPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,63}$`)

func preloadBranchOK(s string) bool {
	return preloadBranchPattern.MatchString(s) && !strings.Contains(s, "..")
}

// preloadCompareVersion compares numeric components without integer overflow.
func preloadCompareVersion(a, b string) (int, bool) {
	parse := func(s string) ([]string, bool) {
		if len(s) == 0 || len(s) > 64 {
			return nil, false
		}
		p := strings.Split(s, ".")
		for n, x := range p {
			if x == "" {
				return nil, false
			}
			for _, c := range x {
				if c < '0' || c > '9' {
					return nil, false
				}
			}
			p[n] = strings.TrimLeft(x, "0")
			if p[n] == "" {
				p[n] = "0"
			}
		}
		return p, true
	}
	x, ok := parse(a)
	y, ok2 := parse(b)
	if !ok || !ok2 {
		return 0, false
	}
	for n := 0; n < max(len(x), len(y)); n++ {
		u, v := "0", "0"
		if n < len(x) {
			u = x[n]
		}
		if n < len(y) {
			v = y[n]
		}
		if len(u) < len(v) {
			return -1, true
		}
		if len(u) > len(v) {
			return 1, true
		}
		if u < v {
			return -1, true
		}
		if u > v {
			return 1, true
		}
	}
	return 0, true
}
func preloadManifestOK(s PreloadManifest) bool {
	return preloadBranchOK(s.Branch) && s.Verified && s.RouteKnown && s.SameOrigin && s.Available && s.Manifest != nil && s.Manifest.Tag == "baseTag" && s.Manifest.Version == s.Config.ResVersion && preloadMD5OK(s.Config.ListHash) && s.Manifest.CheckHash == s.Config.CheckHash
}
func EvaluatePreload(i PreloadInput) PreloadVerdict {
	v := PreloadVerdict{State: PreloadUnavailable, Branch: i.Advertisement.PreReleaseBranch, Version: i.Advertisement.PreReleaseVersion}
	switch {
	case i.Advertisement.PreReleaseStatus == "":
		v.Reason = "not-open"
	case i.Advertisement.PreReleaseStatus != "start":
		v.Reason = "unknown-status"
	case !preloadBranchOK(v.Branch) || !preloadManifestOK(i.Snapshot) || i.Snapshot.Branch != v.Branch || i.Snapshot.Config.ResVersion != v.Version:
		v.Reason = "branch-unresolvable"
	case !i.BaselineTrusted:
		v.Reason = "baseline-untrusted"
	case i.OfficialRunning:
		v.Reason = "official-running"
	default:
		cmp, ok := preloadCompareVersion(v.Version, i.CurrentVersion)
		if !ok || cmp <= 0 {
			v.Reason = "not-newer"
		} else {
			v.State = PreloadAvailable
		}
	}
	if v.State == PreloadAvailable {
		_, err := gameres.BuildPlan(i.Snapshot.Manifest, i.Snapshot.Graph, i.Local, i.Capabilities, preloadAvailability(i.Availability, i.Now), i.Budget)
		if err != nil {
			v.State = PreloadUnavailable
			if errors.Is(err, gameres.ErrInsufficientSpace) {
				v.Reason = "disk-short"
			} else {
				v.Reason = "branch-unresolvable"
			}
		}
	}
	if i.CurrentBranch != "" && i.CurrentBranch != v.Branch && preloadBranchOK(v.Branch) {
		v.Notes = []string{fmt.Sprintf("branch differs: %s → %s", i.CurrentBranch, v.Branch)}
	}
	return v
}

// StartPreload only plans. It never fetches, rebuilds, writes the install tree,
// samples the clock, or publishes an installation version.
func StartPreload(ctx context.Context, i PreloadInput) (PreloadPlan, error) {
	if err := ctx.Err(); err != nil {
		return PreloadPlan{}, err
	}
	i.Local = preloadContextLocal{ctx: ctx, local: i.Local}
	v := EvaluatePreload(i)
	if err := ctx.Err(); err != nil {
		return PreloadPlan{}, err
	}
	if v.State != PreloadAvailable {
		return PreloadPlan{Verdict: v}, fmt.Errorf("%w: %s", ErrPreloadBlocked, v.Reason)
	}
	if i.Now.IsZero() {
		return PreloadPlan{}, fmt.Errorf("%w: missing clock snapshot", ErrPreloadBlocked)
	}
	p, err := gameres.BuildPlan(i.Snapshot.Manifest, i.Snapshot.Graph, preloadContextLocal{ctx: ctx, local: i.Local}, i.Capabilities, preloadAvailability(i.Availability, i.Now), i.Budget)
	if e := ctx.Err(); e != nil {
		return PreloadPlan{}, e
	}
	if err != nil {
		if errors.Is(err, gameres.ErrInsufficientSpace) {
			v.State = PreloadUnavailable
			v.Reason = "disk-short"
		}
		return PreloadPlan{Verdict: v}, err
	}
	r := PreloadRecord{Budget: PreloadBudget{DownloadBytes: p.DownloadBytes, FullBytes: p.DownloadBytes - p.PatchBytes, PatchBytes: p.PatchBytes, ReusedBytes: p.ReusedBytes, PeakAllocBytes: p.PeakAllocBytes, ReserveBytes: i.Budget.ReserveBytes}, Schema: 1, State: PreloadPlanning, Branch: v.Branch, Version: v.Version, ListHash: i.Snapshot.Config.ListHash, Route: PreloadRouteFull, SavedAt: i.Now, UpdatedAt: i.Now, TargetVersionSeenAt: i.Now}
	seenPatch := map[gameres.Object]bool{}
	var patch, full bool
	for _, t := range p.Targets {
		if err = ctx.Err(); err != nil {
			return PreloadPlan{}, err
		}
		id := gameres.Object{MD5: t.MD5, Size: t.Size}
		target := PreloadTarget{Path: t.Path, Object: id, Action: t.Action, Source: t.Current}
		if t.Action == gameres.ActionPatch {
			patch = true
			if t.Route == nil || !i.Capabilities.PatchApply {
				return PreloadPlan{}, ErrPreloadBlocked
			}
			target.Steps = append([]gameres.PatchEntry(nil), t.Route.Steps...)
			for _, step := range target.Steps {
				if !seenPatch[step.Blob] {
					seenPatch[step.Blob] = true
					r.Patches = append(r.Patches, step.Blob)
					state := PreloadFileDownloading
					if i.Local != nil && i.Local.Cached(step.Blob) {
						state = PreloadFileStaged
					}
					r.Files = append(r.Files, PreloadFile{Object: step.Blob, Kind: PreloadBlobPatch, State: state})
				}
			}
		} else if t.Action == gameres.ActionReuseInstalled {
			r.Reused = append(r.Reused, target)
		} else {
			full = true
			state := PreloadFileDownloading
			if t.Action == gameres.ActionInstallCached {
				state = PreloadFileStaged
			}
			r.Files = append(r.Files, PreloadFile{Path: t.Path, Object: id, Kind: PreloadBlobFull, State: state})
		}
		r.Targets = append(r.Targets, target)
	}
	if patch {
		r.Route = PreloadRoutePatch
		if full {
			r.Route = PreloadRouteMixed
		}
	}
	return PreloadPlan{Plan: p, Record: r, Verdict: v, FullDownloadBytes: p.DownloadBytes - p.PatchBytes}, nil
}

type preloadContextLocal struct {
	ctx   context.Context
	local gameres.Local
}

func (l preloadContextLocal) Identity(p string) (gameres.Object, bool) {
	if l.ctx.Err() != nil || l.local == nil {
		return gameres.Object{}, false
	}
	return l.local.Identity(p)
}
func (l preloadContextLocal) Trusted(p string, o gameres.Object) bool {
	return l.ctx.Err() == nil && l.local != nil && l.local.Trusted(p, o)
}
func (l preloadContextLocal) Cached(o gameres.Object) bool {
	return l.ctx.Err() == nil && l.local != nil && l.local.Cached(o)
}
func preloadAvailability(a gameres.AvailabilitySnapshot, now time.Time) gameres.AvailabilitySnapshot {
	if a.At.IsZero() || a.TTL <= 0 || now.Before(a.At) || now.Sub(a.At) > a.TTL {
		a.Objects = nil
	}
	return a
}
