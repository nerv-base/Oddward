// SPDX-License-Identifier: GPL-3.0-or-later

package gameres

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

var (
	ErrTargetUnavailable = errors.New("game resource unavailable")
	ErrNoViableAction    = errors.New("no viable game resource action")
	ErrInsufficientSpace = errors.New("insufficient space for game resources")
)

type ActionKind uint8

const (
	ActionReuseInstalled ActionKind = iota + 1
	ActionInstallCached
	ActionPatch
	ActionFull
)

type SourceKind uint8

const (
	SourceCAS SourceKind = iota + 1
	SourcePakRange
	SourcePatchBlob
)

type Source struct {
	Kind   SourceKind
	Object Object
	Offset int64
	Size   int64
}

type Fallback struct {
	Action    ActionKind
	Full      Source
	Alternate Source // Optional CAS source if the full Pak range fails.
	Reason    string
}

type Target struct {
	Path     string
	MD5      string
	Size     int64
	Action   ActionKind
	Full     Source
	Route    *Route
	Current  Object
	Fallback *Fallback
	Reason   string
}

type Capabilities struct {
	PatchApply    bool
	MaxChainSteps int
	MaxPatchBlob  int64
}

// Local reports an immutable snapshot. Trusted must bind verification to the
// exact file at path, including its file identity, rather than a claimed version.
type Local interface {
	Identity(path string) (Object, bool)
	Trusted(path string, object Object) bool
	Cached(Object) bool
}

type Plan struct {
	Targets        []Target
	DownloadBytes  int64
	FullBytes      int64
	PatchBytes     int64
	ReusedBytes    int64
	OldReadBytes   int64
	RebuildBytes   int64
	CachedBytes    int64
	PeakAllocBytes int64
	Fallbacks      int
	Notes          []string
}

type ObjectStatus uint8

const (
	StatusUnknown ObjectStatus = iota
	StatusReachable
	StatusGone
)

// AvailabilitySnapshot is refreshed by the caller before planning. A missing
// object is unknown. BuildPlan does not sample the clock or probe the CDN.
type AvailabilitySnapshot struct {
	At      time.Time
	TTL     time.Duration
	Objects map[Object]ObjectStatus
}

// BudgetInput describes available space and measured rates. Planning models
// the work root and installation on one volume. A zero rate uses the default.
type BudgetInput struct {
	VolumeFreeBytes      int64
	CacheFreeBytes       int64
	ReserveBytes         int64
	BackupRetention      time.Duration
	ReclaimableNowBytes  int64
	NetBytesPerSecond    int64
	IOBytesPerSecond     int64
	VerifyBytesPerSecond int64
}

const (
	defaultMaxPatchBlob = 8 << 30
	defaultNetRate      = 900_000
	defaultIORate       = 939 << 20
	defaultVerifyRate   = 367 << 20
)

type planResource struct {
	path string
	id   Object
	full Source
}

type actionCost struct {
	network, reads, rebuild, peak int64
	seconds                       float64
}

type candidate struct {
	target Target
	cost   actionCost
}

type actionChoice struct {
	preferred, compact candidate
	searchLimited      bool
}

// BuildPlan selects actions for baseTag resources using the supplied snapshots.
// A selected fallback must be rechecked at execution against free space and
// availability. No partial plan is returned on error.
func BuildPlan(m *ResList, g *PatchGraph, local Local, caps Capabilities, avail AvailabilitySnapshot, budget BudgetInput) (Plan, error) {
	resources, total, err := plannedResources(m)
	if err != nil {
		return Plan{}, err
	}
	if budget.VolumeFreeBytes < 0 || budget.CacheFreeBytes < 0 || budget.ReserveBytes < 0 || budget.ReclaimableNowBytes < 0 ||
		budget.NetBytesPerSecond < 0 || budget.IOBytesPerSecond < 0 || budget.VerifyBytesPerSecond < 0 ||
		caps.MaxChainSteps < 0 || caps.MaxPatchBlob < 0 {
		return Plan{}, fmt.Errorf("%w: invalid planning inputs", ErrNoViableAction)
	}
	if budget.ReserveBytes == 0 {
		budget.ReserveBytes = max(2<<30, total/50)
	}
	free := min(budget.VolumeFreeBytes, budget.CacheFreeBytes)
	free += min(budget.ReclaimableNowBytes, math.MaxInt64-free)
	space := free - budget.ReserveBytes
	if space < 0 {
		return Plan{}, spaceError(uint64(budget.ReserveBytes), uint64(free))
	}
	notes := append([]string(nil), m.Warnings...)
	if len(m.Tags) > 0 {
		notes = append(notes, "optional tags excluded from baseTag plan")
	}
	choices := make([]actionChoice, 0, len(resources))
	selected := make([]candidate, 0, len(resources))
	cachedTargets := make(map[Object]bool)
	remainingVisits := 4 * maxEdgeVisits
	if caps.PatchApply && g != nil && g.byOld == nil && len(g.Edges) <= maxPatchEdges {
		// Index synthetic graphs once, without changing the caller's graph.
		g = &PatchGraph{Edges: g.Edges, byOld: indexPatchEdges(g.Edges)}
	}
	for _, r := range resources {
		choice, err := chooseResource(r, m.CheckHash, g, local, caps, avail, budget, space, &remainingVisits)
		if err != nil {
			return Plan{}, fmt.Errorf("%s: %w", r.path, err)
		}
		if choice.searchLimited {
			notes = append(notes, "patch search limit reached: "+r.path)
		}
		if choice.preferred.target.Action == ActionInstallCached {
			// Only the first target can take the cached allocation by rename.
			if cachedTargets[r.id] {
				choice.preferred.cost.peak = r.id.Size
				choice.compact = choice.preferred
			}
			cachedTargets[r.id] = true
		}
		choices = append(choices, choice)
		selected = append(selected, choice.preferred)
	}
	blobs := make(map[Object]bool)
	peak := peakAllocation(selected, local, blobs)
	if peak <= space {
		return summarizePlan(selected, notes, local, avail, peak), nil
	}
	notes = append(notes, "space budget: reconsidered per-target actions")
	// Try the compact actions, then restore preferred actions in manifest
	// order wherever the exact whole-plan peak permits it. Shared blobs and
	// the single rebuild slot can make a larger individual action cheaper.
	for i, choice := range choices {
		selected[i] = choice.compact
	}
	peak = peakAllocation(selected, local, blobs)
	for i, choice := range choices {
		if choice.compact.target.Action == choice.preferred.target.Action && choice.compact.target.Route == choice.preferred.target.Route {
			continue
		}
		selected[i] = choice.preferred
		updated := peakAllocation(selected, local, blobs)
		if updated > space && updated >= peak {
			selected[i] = choice.compact
		} else {
			peak = updated
		}
	}
	if peak > space {
		peak = tryMixedActions(choices, selected, local, blobs, space, peak)
	}
	if peak > space {
		return Plan{}, spaceError(uint64(peak)+uint64(budget.ReserveBytes), uint64(free))
	}
	return summarizePlan(selected, notes, local, avail, peak), nil
}

// When neither an individual switch nor the compact plan fits, a group may
// still share a blob or rebuild slot. Try combinations for small groups only.
func tryMixedActions(choices []actionChoice, selected []candidate, local Local, blobs map[Object]bool, space, peak int64) int64 {
	const maxMixedChoices = 8
	var varying []int
	for i, choice := range choices {
		if choice.compact.target.Action != choice.preferred.target.Action || choice.compact.target.Route != choice.preferred.target.Route {
			varying = append(varying, i)
			if len(varying) > maxMixedChoices {
				return peak
			}
		}
	}
	for mask := 1<<len(varying) - 1; mask > 0; mask-- {
		for bit, i := range varying {
			selected[i] = choices[i].compact
			if mask&(1<<bit) != 0 {
				selected[i] = choices[i].preferred
			}
		}
		updated := peakAllocation(selected, local, blobs)
		if updated <= space {
			return updated
		}
		peak = min(peak, updated)
	}
	return peak
}

// All downloaded blobs can coexist with staged files. Final outputs persist
// while the intermediates for one chain at a time share the rebuild slot.
func peakAllocation(selected []candidate, local Local, blobs map[Object]bool) int64 {
	clear(blobs)
	var retained, finalPatchBytes, peakPatchBytes int64
	for _, c := range selected {
		if c.target.Action != ActionPatch {
			retained += c.cost.peak
			continue
		}
		route := *c.target.Route
		peakPatchBytes = max(peakPatchBytes, finalPatchBytes+patchWorkingBytes(route))
		finalPatchBytes += c.target.Size
		for _, step := range route.Steps {
			if !blobs[step.Blob] && !isCached(local, step.Blob) {
				retained += step.Blob.Size
				blobs[step.Blob] = true
			}
		}
	}
	return retained + max(peakPatchBytes, finalPatchBytes)
}

func summarizePlan(selected []candidate, notes []string, local Local, avail AvailabilitySnapshot, peak int64) Plan {
	p := Plan{PeakAllocBytes: peak, Notes: append([]string(nil), notes...)}
	patchBlobs, downloadedCAS, noted := map[Object]bool{}, map[Object]bool{}, map[Object]bool{}
	for _, chosen := range selected {
		t, cost := chosen.target, chosen.cost
		switch t.Action {
		case ActionReuseInstalled:
			p.ReusedBytes += t.Size
		case ActionInstallCached:
			p.CachedBytes += t.Size
		case ActionFull:
			p.FullBytes += t.Size
			if t.Full.Kind == SourcePakRange {
				p.DownloadBytes += cost.network
			} else if !downloadedCAS[t.Full.Object] {
				downloadedCAS[t.Full.Object] = true
				p.DownloadBytes += cost.network
			}
			p.noteUnknown(t.Full.Object, avail, noted)
		case ActionPatch:
			t.Route.cloneBlocks()
			p.FullBytes += t.Size
			p.OldReadBytes += cost.reads
			p.RebuildBytes += cost.rebuild
			for _, step := range t.Route.Steps {
				if !isCached(local, step.Blob) && !patchBlobs[step.Blob] {
					patchBlobs[step.Blob] = true
					p.PatchBytes += step.Blob.Size
					p.noteUnknown(step.Blob, avail, noted)
				}
			}
		}
		if t.Fallback != nil {
			p.Fallbacks++
			p.noteUnknown(t.Fallback.Full.Object, avail, noted)
		}
		p.Targets = append(p.Targets, t)
	}
	p.DownloadBytes += p.PatchBytes
	return p
}

func plannedResources(m *ResList) ([]planResource, int64, error) {
	if m == nil || m.Tag != "baseTag" {
		return nil, 0, fmt.Errorf("%w: baseTag resource list required", ErrNoViableAction)
	}
	var resources []planResource
	var total int64
	seen := make(targetSet)
	add := func(path string, id Object, source Source) error {
		original := path
		path, err := NormalizePath(path)
		if err != nil {
			return err
		}
		if path != original || !validObject(id, maxFileBytes) {
			return fmt.Errorf("%w: invalid normalized target", ErrMalformedManifest)
		}
		dual, err := seen.add(path, id, source.Kind == SourcePakRange)
		if err != nil || dual {
			return err
		}
		resources = append(resources, planResource{path, id, source})
		total += id.Size
		if total > maxTargetBytes {
			return ErrTooLarge
		}
		return nil
	}
	for _, f := range m.Files {
		id := Object{f.MD5, f.Size}
		if err := add(f.Path, id, Source{Kind: SourceCAS, Object: id, Size: id.Size}); err != nil {
			return nil, 0, err
		}
	}
	for _, p := range m.Packages {
		for _, e := range p.Entries {
			if !validObject(p.Pak, maxPakBytes) || e.Pak != p.Pak || e.Size <= 0 || e.Offset < 0 || e.Offset > p.Pak.Size-e.Size {
				return nil, 0, fmt.Errorf("%w: invalid package range", ErrMalformedManifest)
			}
			if err := add(e.Path, Object{e.MD5, e.Size}, Source{SourcePakRange, p.Pak, e.Offset, e.Size}); err != nil {
				return nil, 0, err
			}
		}
	}
	if len(resources) == 0 {
		return nil, 0, ErrNoViableAction
	}
	return resources, total, seen.validate()
}

func chooseResource(r planResource, checkHash string, g *PatchGraph, local Local, caps Capabilities, avail AvailabilitySnapshot, b BudgetInput, space int64, remainingVisits *int) (actionChoice, error) {
	t := Target{Path: r.path, MD5: r.id.MD5, Size: r.id.Size}
	var current Object
	var trusted bool
	if local != nil {
		if found, ok := local.Identity(r.path); ok {
			current = found
			trusted = validObject(current, maxFileBytes) && local.Trusted(r.path, current)
		}
	}
	if trusted && current == r.id {
		t.Action = ActionReuseInstalled
		t.Reason = fmt.Sprintf("ReuseInstalled %s: verified installed file equals target", r.id)
		c := candidate{target: t}
		return actionChoice{preferred: c, compact: c}, nil
	}
	if isCached(local, r.id) {
		t.Action = ActionInstallCached
		t.Reason = fmt.Sprintf("InstallCached %s: stage verified cached bytes", r.id)
		c := candidate{target: t}
		return actionChoice{preferred: c, compact: c}, nil
	}
	full := fullCandidate(t, r, current, avail, b)
	fullGone := avail.Objects[full.target.Full.Object] == StatusGone
	var possible []candidate
	var limited bool
	if !fullGone {
		possible = append(possible, full)
	}
	if caps.PatchApply && trusted && checkHash != "" && g != nil {
		limit := caps.MaxPatchBlob
		if limit == 0 {
			limit = defaultMaxPatchBlob
		}
		var routes []Route
		routes, limited = g.routes(current, r.id, func(o Object) bool { return isCached(local, o) }, caps.MaxChainSteps, func(e PatchEntry) bool {
			return e.Section == "" && (e.New != r.id || e.Version == checkHash) &&
				(e.Blob.Size <= limit && (isCached(local, e.Blob) || avail.Objects[e.Blob] != StatusGone))
		}, remainingVisits)
		for _, route := range routes {
			var total int64
			for _, step := range route.Steps {
				total += step.Blob.Size
			}
			if total <= limit {
				possible = append(possible, patchCandidate(t, r, route, full.target, fullGone, b))
			}
		}
	}
	if len(possible) == 0 {
		if limited {
			return actionChoice{}, fmt.Errorf("%w: patch search limit reached", ErrTargetUnavailable)
		}
		return actionChoice{}, ErrTargetUnavailable
	}
	fastest, compact := math.Inf(1), possible[0]
	for _, option := range possible {
		if option.cost.peak < compact.cost.peak || option.cost.peak == compact.cost.peak && option.cost.seconds < compact.cost.seconds {
			compact = option
		}
		if option.cost.peak <= space {
			fastest = min(fastest, option.cost.seconds)
		}
	}
	if math.IsInf(fastest, 1) {
		return actionChoice{}, spaceError(uint64(compact.cost.peak), uint64(space))
	}
	var chosen candidate
	for _, option := range possible {
		if option.cost.peak > space || option.cost.seconds > fastest*1.05 {
			continue
		}
		if chosen.target.Action == 0 || option.cost.network < chosen.cost.network ||
			(option.cost.network == chosen.cost.network && option.target.Action == ActionPatch && chosen.target.Action == ActionFull) {
			chosen = option
		}
	}
	return actionChoice{preferred: chosen, compact: compact, searchLimited: limited}, nil
}

func fullCandidate(t Target, r planResource, current Object, avail AvailabilitySnapshot, b BudgetInput) candidate {
	t.Action, t.Full = ActionFull, r.full
	cas := Source{Kind: SourceCAS, Object: r.id, Size: r.id.Size}
	if t.Full.Kind == SourcePakRange && avail.Objects[r.id] == StatusReachable {
		if avail.Objects[t.Full.Object] == StatusGone {
			t.Full = cas
		} else {
			t.Fallback = &Fallback{Action: ActionFull, Full: cas, Reason: "use target CAS if Pak range fails"}
		}
	}
	c := actionCost{network: r.id.Size, peak: r.id.Size}
	c.seconds = estimateSeconds(c.network, r.id.Size, r.id.Size, b)
	t.Reason = fmt.Sprintf("Full: current=%s target=%s source=%s download=%d peak=%d estimate=%.3fs", current, r.id, t.Full, c.network, c.peak, c.seconds)
	return candidate{t, c}
}

func patchCandidate(t Target, r planResource, route Route, full Target, fullGone bool, b BudgetInput) candidate {
	t.Action, t.Route, t.Current = ActionPatch, &route, route.Source
	reason := "download the full target if reconstruction fails"
	if fullGone {
		reason = "full source is currently gone; recheck availability before trying"
	}
	t.Fallback = &Fallback{Action: ActionFull, Full: full.Full, Reason: reason}
	if full.Fallback != nil {
		t.Fallback.Alternate = full.Fallback.Full
	}
	c := actionCost{network: route.PatchBytes, peak: route.PatchBytes + patchWorkingBytes(route)}
	var chain []string
	var allBlobs int64
	seen := make(map[Object]bool)
	for _, step := range route.Steps {
		c.reads += step.Old.Size
		c.rebuild += step.New.Size
		if !seen[step.Blob] {
			allBlobs += step.Blob.Size
			seen[step.Blob] = true
		}
		chain = append(chain, fmt.Sprintf("%s -> %s via %s", step.Old, step.New, step.Blob))
	}
	c.seconds = estimateSeconds(c.network, c.reads+c.rebuild, r.id.Size, b)
	t.Reason = fmt.Sprintf("Patch: current=%s target=%s steps=[%s] patch=%d full=%d cached=%d old-read=%d rebuild=%d peak=%d estimate=%.3fs fallback=Full %s (%s)",
		t.Current, r.id, strings.Join(chain, ", "), c.network, r.id.Size, allBlobs-c.network, c.reads, c.rebuild, c.peak, c.seconds, full.Full, reason)
	return candidate{t, c}
}

// The original installed file is already accounted for in free space. Later
// intermediate inputs coexist with their output until the step is verified.
func patchWorkingBytes(route Route) int64 {
	var peak int64
	for i, step := range route.Steps {
		working := step.New.Size
		if i > 0 {
			working += step.Old.Size
		}
		peak = max(peak, working)
	}
	return peak
}

func estimateSeconds(network, disk, verify int64, b BudgetInput) float64 {
	netRate, ioRate, verifyRate := b.NetBytesPerSecond, b.IOBytesPerSecond, b.VerifyBytesPerSecond
	if netRate == 0 {
		netRate = defaultNetRate
	}
	if ioRate == 0 {
		ioRate = defaultIORate
	}
	if verifyRate == 0 {
		verifyRate = defaultVerifyRate
	}
	return float64(network)/float64(netRate) + float64(disk)/float64(ioRate) + float64(verify)/float64(verifyRate)
}

func isCached(local Local, object Object) bool { return local != nil && local.Cached(object) }

func (p *Plan) noteUnknown(o Object, avail AvailabilitySnapshot, noted map[Object]bool) {
	if avail.Objects[o] == StatusUnknown && !noted[o] {
		p.Notes = append(p.Notes, "availability unknown: "+o.String())
		noted[o] = true
	}
}

func (s Source) String() string {
	if s.Kind == SourcePakRange {
		return fmt.Sprintf("PakRange %s at %d+%d", s.Object, s.Offset, s.Size)
	}
	return "CAS " + s.Object.String()
}

func spaceError(need, available uint64) error {
	return fmt.Errorf("%w: need %d bytes, have %d bytes", ErrInsufficientSpace, need, available)
}
