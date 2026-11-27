// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

var ErrRecoveryRequired = errors.New("installation recovery required")

// ErrCleanupPending means the ready record is positively published and playable,
// but recorded backup cleanup must be retried before another transaction starts.
var ErrCleanupPending = errors.New("published installation cleanup pending")

type txnCleanupIO struct{ error }

func (e txnCleanupIO) Unwrap() error { return e.error }

var ErrTargetExists = errors.New("installation target exists")
var ErrNoSpace = errors.New("insufficient installation space")
var ErrUnsafeTarget = errors.New("unsafe installation target")

// TxnFile identifies one complete final file. Source is relative to WorkRoot
// (or SourceRoot for Files when explicitly selected for a fresh installation);
// empty Source means an installed file that must already match. Retain copies
// rather than transfers the source. No path is ever linked to the cache.
type TxnFile struct {
	Path, Source, MD5 string
	Size              int64
	Retain            bool
	Origin            string
}

// TxnProgress reports real backend-measured bytes and files processed while
// Commit verifies candidate sources prior to file placement.
type TxnProgress struct {
	BytesDone, BytesTotal int64
	FilesDone, FilesTotal int
}

// TxnInput requires the caller to hold the same gamedl.Acquire(WorkRoot) lease
// used for downloads until Commit returns. Guard must verify no game or official
// patcher process is running. WorkRoot must be inside Root/.oddward on the same
// volume. Files describes the complete candidate set; Bootstrap is restricted to
// the two explicitly allowed bootstrap files. Set BackupRetention to 24 hours for
// the product default; zero explicitly requests immediate cleanup.
// Checkpoint is an optional fault-injection hook. Its errors simulate abrupt
// termination, leaving durable evidence intact for a later Recover call.
type TxnInput struct {
	Root, WorkRoot, TxnID string
	// SourceRoot optionally selects an explicitly confirmed cache for Files sources
	// during fresh installation only. It must be disjoint from Root. Bootstrap
	// and RebuildCleanup remain relative to destination WorkRoot. External sources
	// are always copied and preserved, even on the same volume. The caller must
	// also exclude source-cache writes/GC (hold its lease) through staging.
	// ResumeCommit needs only destination evidence and never reopens SourceRoot.
	// With SourceRoot set, RequiredFreeBytes is the destination reserve; Commit
	// adds only the byte sizes of copies not already verified in staged/final.
	SourceRoot       string
	Candidate        InstallRecord
	Files, Bootstrap []TxnFile
	// RebuildCleanup lists exact intermediate identities eligible for post-publish
	// deletion. Source must be under WorkRoot/rebuild; unrelated files survive.
	RebuildCleanup    []TxnFile
	Guard             func(context.Context) error
	FreeBytes         func(context.Context) (int64, error)
	RequiredFreeBytes int64
	BackupRetention   time.Duration
	Repair            bool
	// AllowMetadataBaseline lets a normal update proceed from an Adopted or
	// ready metadata baseline without hashing unchanged reused files or replaced
	// old files whose verify.json fingerprints are absent. New/replaced
	// candidates and pruned files still undergo full MD5 verification, and
	// verify.json records only genuinely MD5-verified targets.
	AllowMetadataBaseline bool
	// OnCommitStart runs after all baseline and candidate source verifications
	// pass and before any source blob is moved or journal/install state mutates.
	OnCommitStart func() error
	// Progress reports live candidate verification progress during Commit.
	Progress   func(TxnProgress)
	Checkpoint func(string) error
}

// RecoveryStatus is a truthful launch gate. Published identifies positive
// (transaction ID, version) publication evidence; Resume requires explicit work.
type RecoveryStatus struct {
	State, TxnID, Action             string
	LaunchBlocked, Resume, Published bool
}

type txnEntry struct {
	Path        string         `json:"path"`
	Target      *InstallTarget `json:"target,omitempty"`
	Old         *InstallTarget `json:"old,omitempty"`
	Origin      string         `json:"origin,omitempty"`
	Step        string         `json:"step"`
	Reuse       bool           `json:"reuse,omitempty"`
	Prune       bool           `json:"prune,omitempty"`
	MetaOnly    bool           `json:"metaOnly,omitempty"`
	OldMetaOnly bool           `json:"oldMetaOnly,omitempty"`
}
type txnJournal struct {
	Schema         int             `json:"schema"`
	TxnID          string          `json:"txnID"`
	InstallKey     string          `json:"installKey"`
	Root           string          `json:"root"`
	WorkRoot       string          `json:"workRoot"`
	Kind           string          `json:"kind"`
	Base           InstallRecord   `json:"base"`
	Candidate      InstallRecord   `json:"candidate"`
	Phase          string          `json:"phase"`
	Files          []txnEntry      `json:"files"`
	RebuildCleanup []InstallTarget `json:"rebuildCleanup,omitempty"`
	CreatedDirs    []string        `json:"createdDirs,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
	PublishedAt    time.Time       `json:"publishedAt,omitempty"`
	RetainUntil    time.Time       `json:"retainUntil,omitempty"`
	Retention      time.Duration   `json:"retention"`
}
type txnStop struct{ error }

func (e txnStop) Unwrap() error { return e.error }

func txnCheckpoint(in TxnInput, point string) error {
	if in.Checkpoint != nil {
		if err := in.Checkpoint(point); err != nil {
			return txnStop{err}
		}
	}
	return nil
}
func txnRequired(err error) (RecoveryStatus, error) {
	return RecoveryStatus{State: "recovery_required", LaunchBlocked: true}, fmt.Errorf("%w: %w", ErrRecoveryRequired, err)
}
func txnPublished(j *txnJournal, r InstallRecord) bool {
	return !r.BaselineMismatch && r.State == "ready" && r.TxnID == j.TxnID && r.Version == j.Candidate.Version && r.InstallKey == j.InstallKey && r.ListHash == j.Candidate.ListHash && r.ManifestDigest == j.Candidate.ManifestDigest && r.Branch == j.Candidate.Branch && r.Mode == j.Candidate.Mode && r.RootFileID == j.Candidate.RootFileID && txnSameTargets(r.Targets, j.Candidate.Targets) && r.VerifyCoverage == j.Candidate.VerifyCoverage && r.VerifiedTargets == j.Candidate.VerifiedTargets
}
func txnBaseMatches(j *txnJournal, r InstallRecord) bool {
	return r.BaselineMismatch == j.Base.BaselineMismatch && r.InstallKey == j.Base.InstallKey && r.RootFileID == j.Base.RootFileID && r.Version == j.Base.Version && r.TxnID == j.Base.TxnID && r.ListHash == j.Base.ListHash && r.ManifestDigest == j.Base.ManifestDigest && r.Branch == j.Base.Branch && r.Mode == j.Base.Mode && txnSameTargets(r.Targets, j.Base.Targets) && (r.State == j.Base.State || j.Kind == "install" && (r.State == "installing" || r.State == "incomplete"))
}
func txnJournalName(j *txnJournal) string  { return j.InstallKey + "/txn/" + j.TxnID + ".json" }
func txnDir(j *txnJournal) string          { return "txn/" + j.TxnID }
func txnStage(j *txnJournal, i int) string { return fmt.Sprintf("%s/staged/%d", txnDir(j), i) }
func txnBackup(j *txnJournal, i int) string {
	kind := "backup"
	if j.Files[i].Prune {
		kind = "pruned"
	}
	return fmt.Sprintf("%s/%s/%d", txnDir(j), kind, i)
}
func (s *Store) txnSave(j *txnJournal) error { return s.stateWrite(txnJournalName(j), j, nil) }

func (s *Store) Commit(ctx context.Context, in TxnInput) (RecoveryStatus, error) {
	if err := ctx.Err(); err != nil {
		return RecoveryStatus{}, err
	}
	if in.Guard == nil {
		return RecoveryStatus{}, errors.New("transaction requires process guard and caller-held lease")
	}
	if err := in.Guard(ctx); err != nil {
		return RecoveryStatus{}, err
	}
	roots, err := txnOpenRoots(in.Root, in.WorkRoot)
	if err != nil {
		return RecoveryStatus{}, err
	}
	defer roots.close()
	if err := txnValidateInput(in); err != nil {
		return RecoveryStatus{}, err
	}
	journals, err := s.txnReadActive(in.Root, in.WorkRoot)
	if err != nil {
		return txnRequired(err)
	}
	if len(journals) > 1 {
		return txnRequired(errors.New("multiple active transactions"))
	}
	if len(journals) == 1 {
		j := journals[0]
		record, err := s.ReadInstall(in.Root)
		if err != nil {
			return txnRequired(err)
		}
		if txnPublished(j, record) {
			if _, err = s.txnFinish(ctx, roots, j, TxnInput{}, nil); err != nil {
				return txnRequired(err)
			}
		} else if j.TxnID == in.TxnID && txnBaseMatches(j, record) && (j.Kind == "install" || j.Phase == "staged") {
			if !txnSameTargets(j.Candidate.Targets, in.Candidate.Targets) || j.Candidate.ListHash != in.Candidate.ListHash || j.Candidate.ManifestDigest != in.Candidate.ManifestDigest || j.Candidate.Version != in.Candidate.Version || j.Candidate.Branch != in.Candidate.Branch {
				return txnRequired(errors.New("resume candidate changed"))
			}
			if j.Kind == "install" {
				if err := txnValidateFreshResume(ctx, roots, j); err != nil {
					return txnRequired(err)
				}
			}
			if in.OnCommitStart != nil {
				if err := in.OnCommitStart(); err != nil {
					return RecoveryStatus{}, err
				}
			}
			return s.txnCommit(ctx, roots, j, in, nil)
		} else {
			return txnRequired(errors.New("pending transaction requires recovery"))
		}
	}
	base, err := s.ReadInstall(in.Root)
	fresh := errors.Is(err, os.ErrNotExist)
	if err != nil && !fresh {
		return txnRequired(err)
	}
	if !fresh && base.Mode == "fresh" && (base.State == "installing" || base.State == "incomplete") && in.Candidate.Mode == "fresh" {
		if base.Version != in.Candidate.Version || base.ListHash != in.Candidate.ListHash || base.ManifestDigest != in.Candidate.ManifestDigest || !txnSameTargets(base.Targets, in.Candidate.Targets) {
			return txnRequired(errors.New("incomplete installation candidate changed"))
		}
		// Without a journal, explicit restart is permitted only before any game
		// file was placed. Verified staging is reusable under the same task ID.
		if err := txnEmptyRoot(roots.install); err != nil {
			return txnRequired(err)
		}
		fresh = true
	}
	if fresh && in.Candidate.Mode != "fresh" {
		return RecoveryStatus{}, errors.New("new installation requires fresh mode")
	}
	branchSwitch := !fresh && in.Candidate.Branch != base.Branch
	if !fresh && !branchSwitch && len(in.Bootstrap) > 0 {
		return RecoveryStatus{}, ErrTargetExists
	}
	if !fresh && in.TxnID == base.TxnID {
		return RecoveryStatus{}, errors.New("transaction ID must differ from baseline")
	}
	if !fresh && (base.BaselineMismatch || in.Candidate.BaselineMismatch) {
		return txnRequired(ErrStaleState)
	}
	metadataBaseline := in.AllowMetadataBaseline && (base.State == Adopted || base.State == "ready") && base.VerifyCoverage == "metadata" && len(base.Targets) > 0
	if !fresh && base.State != "ready" && base.State != "verified" && !metadataBaseline {
		exactRepair := in.Repair && (base.State == Detected || base.State == Adopted) && base.Version == in.Candidate.Version && base.Branch == in.Candidate.Branch && base.ListHash != "" && base.ListHash == in.Candidate.ListHash && base.ManifestDigest != "" && base.ManifestDigest == in.Candidate.ManifestDigest && txnSameTargets(base.Targets, in.Candidate.Targets)
		if !exactRepair {
			return txnRequired(errors.New("baseline is not ready or verified, or exact repair candidate"))
		}
	}
	if !fresh && !in.Repair && !metadataBaseline && (base.VerifyCoverage != "full" || base.VerifiedTargets != len(base.Targets)) {
		return RecoveryStatus{}, errors.New("baseline requires full verification")
	}
	if fresh {
		if err := txnEmptyRoot(roots.install); err != nil {
			return RecoveryStatus{}, err
		}
	}
	if in.SourceRoot != "" {
		if !fresh {
			return RecoveryStatus{}, ErrUnsafeTarget
		}
		if err := txnOpenSource(roots, in.SourceRoot); err != nil {
			return RecoveryStatus{}, err
		}
	}
	ledger := newTxnLedger()
	if err := txnCheckSpace(ctx, in, roots, ledger); err != nil {
		return RecoveryStatus{}, err
	}
	j := &txnJournal{Schema: 2, TxnID: in.TxnID, InstallKey: in.Candidate.InstallKey, Root: in.Root, WorkRoot: in.WorkRoot, Base: base, Candidate: in.Candidate, Kind: "update", Phase: "preparing", CreatedAt: time.Now().UTC(), Retention: in.BackupRetention}
	if fresh {
		j.Kind = "install"
		j.Base = InstallRecord{}
	}
	j.Candidate.State = "ready"
	if !fresh {
		j.Candidate.RootFileID = base.RootFileID
	}
	j.Candidate.TxnID = j.TxnID
	j.Candidate.BaseVersion = base.Version
	j.Candidate.VerifyCoverage = "full"
	j.Candidate.VerifiedTargets = len(in.Candidate.Targets)
	if fresh {
		j.Candidate.BaseVersion = ""
	}
	if err := s.txnPrepare(ctx, roots, j, in, ledger); err != nil {
		return RecoveryStatus{}, err
	}
	if fresh {
		base = j.Candidate
		base.State = "installing"
		base.TxnID = ""
		base.BaseVersion = ""
		base.VerifyCoverage = "none"
		base.VerifiedTargets = 0
		base.PublishedAt = time.Time{}
		if err := s.writeInstall(ctx, base); err != nil {
			return RecoveryStatus{}, err
		}
		base, err = s.ReadInstall(in.Root)
		if err != nil {
			return txnRequired(err)
		}
		j.Base = base
		j.Candidate.RootFileID = base.RootFileID
	}
	if err := txnCheckpoint(in, "before-journal"); err != nil {
		return RecoveryStatus{}, err
	}
	if err := s.txnSave(j); err != nil {
		return RecoveryStatus{}, err
	}
	if err := txnCheckpoint(in, "after-journal"); err != nil {
		return RecoveryStatus{}, err
	}
	j.Phase = "staged"
	if err := s.txnSave(j); err != nil {
		return txnRequired(err)
	}
	if err := txnCheckpoint(in, "after-staged"); err != nil {
		return RecoveryStatus{}, err
	}
	status, err := s.txnCommit(ctx, roots, j, in, ledger)
	if err != nil {
		var stop txnStop
		if errors.As(err, &stop) {
			return status, err
		}
		recovered, rerr := s.txnRecoverOne(context.WithoutCancel(ctx), roots, j, in.Guard)
		if rerr != nil {
			return recovered, errors.Join(err, rerr)
		}
	}
	return status, err
}
func txnValidateInput(in TxnInput) error {
	if in.SourceRoot != "" {
		if in.Candidate.Mode != "fresh" || in.Repair || len(in.RebuildCleanup) > 0 {
			return ErrUnsafeTarget
		}
		if err := txnSourceRootValid(in.SourceRoot, in.Root); err != nil {
			return err
		}
	}
	if !txnIDValid(in.TxnID) || in.BackupRetention < 0 || in.BackupRetention > 168*time.Hour || in.RequiredFreeBytes < 0 {
		return ErrInvalidState
	}
	key, err := InstallKey(in.Root)
	if err != nil || key != in.Candidate.InstallKey || !strings.EqualFold(filepath.Clean(in.Root), filepath.Clean(in.Candidate.Root)) {
		return ErrInvalidState
	}
	if err := stateValidateRecord(in.Candidate); err != nil {
		return err
	}
	if len(in.Files) == 0 || len(in.Files) != len(in.Candidate.Targets) || len(in.Bootstrap) > 2 {
		return ErrInvalidState
	}
	all := append(append([]TxnFile{}, in.Files...), in.Bootstrap...)
	seen := map[string]bool{}
	for i, f := range all {
		if err := txnTargetValid(InstallTarget{Path: f.Path, MD5: f.MD5, Size: f.Size}); err != nil {
			return err
		}
		key := strings.ToUpper(f.Path)
		if seen[key] {
			return ErrUnsafeTarget
		}
		seen[key] = true
		if f.Source != "" {
			if err := txnSourceValid(f.Source); err != nil {
				return err
			}
		}
		if i < len(in.Files) {
			t := in.Candidate.Targets[i]
			if f.Path != t.Path || f.MD5 != t.MD5 || f.Size != t.Size {
				return ErrInvalidState
			}
		} else if !txnBootstrapPath(f.Path) {
			return ErrUnsafeTarget
		}
	}
	for p := range seen {
		for parent := filepath.ToSlash(filepath.Dir(p)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
			if seen[parent] {
				return ErrUnsafeTarget
			}
		}
	}
	return nil
}
func txnSameTargets(a, b []InstallTarget) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (s *Store) txnPrepare(ctx context.Context, r *txnRoots, j *txnJournal, in TxnInput, ledger *txnLedger) error {
	for _, file := range in.RebuildCleanup {
		target := InstallTarget{Path: file.Source, MD5: file.MD5, Size: file.Size}
		if err := txnRebuildTargetValid(target); err != nil {
			return err
		}
		if err := txnVerifyLedger(ctx, r.work, target.Path, target, ledger, nil); err != nil {
			return err
		}
		j.RebuildCleanup = append(j.RebuildCleanup, target)
	}
	old := map[string]InstallTarget{}
	rebuildSeen := map[string]bool{}
	for _, target := range j.RebuildCleanup {
		key := strings.ToUpper(target.Path)
		if rebuildSeen[key] {
			return ErrInvalidState
		}
		rebuildSeen[key] = true
	}
	for _, t := range j.Base.Targets {
		old[strings.ToUpper(t.Path)] = t
	}
	// Seed install-side stamps from verify.json so verified reused and old files
	// need only a fresh handle/link/mtime/FileID check instead of full rehashing.
	if j.Kind != "install" {
		s.txnSeedVerifyInto(r.install, j.Base, ledger)
	}
	all := append(append([]TxnFile{}, in.Files...), in.Bootstrap...)
	// Inspect installed paths in parallel (using ledger hits for verified files,
	// and safe handle stat(size) for unverified owned files when AllowMetadataBaseline is set).
	type installProbe struct {
		actual      InstallTarget
		exists      bool
		metaOnly    bool
		oldMetaOnly bool
		stamp       txnStamp
	}
	installProbes := make([]installProbe, len(all))
	if err := txnRunParallel(ctx, len(all), func(pctx context.Context, i int) error {
		f := all[i]
		key := strings.ToUpper(f.Path)
		declared, owned := old[key]
		if in.AllowMetadataBaseline && owned && !txnBootstrapPath(f.Path) {
			st, serr := txnStatStamp(r.install, f.Path)
			if errors.Is(serr, os.ErrNotExist) {
				return nil
			}
			if serr != nil {
				return serr
			}
			if md5Hex, ok := ledger.get(st); ok {
				installProbes[i] = installProbe{actual: InstallTarget{Path: f.Path, MD5: md5Hex, Size: st.Size}, exists: true, stamp: st}
				return nil
			}
			if f.Source == "" && st.Size == declared.Size && declared.MD5 == f.MD5 && declared.Size == f.Size {
				installProbes[i] = installProbe{actual: declared, exists: true, metaOnly: true, stamp: st}
				return nil
			}
			if f.Source != "" && st.Size == declared.Size {
				installProbes[i] = installProbe{actual: declared, exists: true, oldMetaOnly: true, stamp: st}
				return nil
			}
		}
		actual, st, err := txnIdentityLedger(pctx, r.install, f.Path, ledger, nil)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		installProbes[i] = installProbe{actual: actual, exists: true, stamp: st}
		return nil
	}); err != nil {
		return err
	}
	targetSet := map[string]bool{}
	for i, f := range all {
		if err := ctx.Err(); err != nil {
			return err
		}
		t := InstallTarget{Path: f.Path, MD5: f.MD5, Size: f.Size}
		e := txnEntry{Path: f.Path, Target: &t, Origin: f.Origin, Reuse: f.Source == "", Step: "staged"}
		key := strings.ToUpper(f.Path)
		targetSet[key] = true
		probe := installProbes[i]
		actual, exists := probe.actual, probe.exists
		declared, owned := old[key]
		if exists {
			if owned {
				if actual != declared && !in.Repair && !(in.AllowMetadataBaseline && f.Source != "") {
					return fmt.Errorf("baseline changed: %s", f.Path)
				}
				e.Old = &actual
				e.OldMetaOnly = probe.oldMetaOnly
			} else if actual == t {
				e.Reuse = true
			} else if txnBootstrapPath(f.Path) && j.Kind == "update" && j.Candidate.Branch != j.Base.Branch {
				e.Old = &actual
			} else {
				return fmt.Errorf("%w: %s", ErrTargetExists, f.Path)
			}
		} else if owned && !in.Repair && !(in.AllowMetadataBaseline && f.Source != "") {
			return fmt.Errorf("baseline missing: %s", f.Path)
		}
		if e.Reuse {
			if !exists || actual != t {
				return fmt.Errorf("reused target mismatch: %s", f.Path)
			}
			e.MetaOnly = probe.metaOnly
		} else if f.Source == "" {
			return ErrInvalidState
		}
		j.Files = append(j.Files, e)
	}
	verifiedTargets := 0
	for i := 0; i < len(in.Files); i++ {
		if !j.Files[i].MetaOnly {
			verifiedTargets++
		}
	}
	j.Candidate.VerifiedTargets = verifiedTargets
	if verifiedTargets == len(in.Candidate.Targets) {
		j.Candidate.VerifyCoverage = "full"
	} else {
		j.Candidate.VerifyCoverage = "metadata"
	}
	// Only an exact old-owned, unchanged file absent from the new set is pruned.
	// User edits, unrelated files and tag resources are never inferred as deletable.
	var pruneCandidates []InstallTarget
	for _, t := range j.Base.Targets {
		if !targetSet[strings.ToUpper(t.Path)] {
			pruneCandidates = append(pruneCandidates, t)
		}
	}
	pruneMatches := make([]bool, len(pruneCandidates))
	if err := txnRunParallel(ctx, len(pruneCandidates), func(pctx context.Context, i int) error {
		t := pruneCandidates[i]
		actual, _, err := txnIdentityLedger(pctx, r.install, t.Path, ledger, nil)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		pruneMatches[i] = actual == t
		return nil
	}); err != nil {
		return err
	}
	for i, t := range pruneCandidates {
		if pruneMatches[i] {
			copy := t
			j.Files = append(j.Files, txnEntry{Path: t.Path, Old: &copy, Prune: true, Step: "none"})
		}
	}
	// Record metadata-matched handle stamps in the per-commit ledger after prune
	// checks so intra-commit move/validation verifies handle stability without
	// rehashing untouched or replaced old files.
	for _, probe := range installProbes {
		if probe.metaOnly || probe.oldMetaOnly {
			ledger.put(probe.stamp, probe.actual.MD5)
		}
	}

	// Phase A: verify every candidate source (or existing stage) in parallel
	// BEFORE calling OnCommitStart or moving any blob out of cache/blobs/.
	type verifyJob struct {
		root   *os.Root
		path   string
		kind   string // "stage", "external", "source"
		target InstallTarget
		entry  int
	}
	type jobKey struct {
		kind, path string
	}
	stageExists := make([]bool, len(j.Files))
	var jobs []verifyJob
	seenJob := map[jobKey]int{}
	type checkBinding struct {
		jobIdx int
		entry  int
		kind   string
		want   InstallTarget
	}
	var bindings []checkBinding
	for i, e := range j.Files {
		if e.Prune || e.Reuse {
			continue
		}
		f := all[i]
		stage := txnStage(j, i)
		if err := txnSafe(r.work, stage, true); err != nil {
			return err
		}
		_, lerr := r.work.Lstat(stage)
		if lerr == nil {
			stageExists[i] = true
			k := jobKey{"stage", stage}
			idx, ok := seenJob[k]
			if !ok {
				idx = len(jobs)
				seenJob[k] = idx
				jobs = append(jobs, verifyJob{root: r.work, path: stage, kind: "stage", target: *e.Target, entry: i})
			}
			bindings = append(bindings, checkBinding{jobIdx: idx, entry: i, kind: "stage", want: *e.Target})
			continue
		}
		if !errors.Is(lerr, os.ErrNotExist) {
			return lerr
		}
		if in.SourceRoot != "" && i < len(in.Files) {
			if r.source == nil {
				return ErrUnsafeTarget
			}
			k := jobKey{"external", f.Source}
			idx, ok := seenJob[k]
			if !ok {
				idx = len(jobs)
				seenJob[k] = idx
				jobs = append(jobs, verifyJob{root: r.source, path: f.Source, kind: "external", target: *e.Target, entry: i})
			}
			bindings = append(bindings, checkBinding{jobIdx: idx, entry: i, kind: "external", want: *e.Target})
			continue
		}
		k := jobKey{"source", f.Source}
		idx, ok := seenJob[k]
		if !ok {
			idx = len(jobs)
			seenJob[k] = idx
			jobs = append(jobs, verifyJob{root: r.work, path: f.Source, kind: "source", target: *e.Target, entry: i})
		}
		bindings = append(bindings, checkBinding{jobIdx: idx, entry: i, kind: "source", want: *e.Target})
	}
	var totalBytes int64
	for _, job := range jobs {
		totalBytes += job.target.Size
	}
	var progMu sync.Mutex
	var bytesDone int64
	var filesDone int
	lastReport := time.Now()
	emitProgress := func(force bool) {
		if in.Progress == nil || totalBytes <= 0 {
			return
		}
		now := time.Now()
		if !force && now.Sub(lastReport) < 125*time.Millisecond {
			return
		}
		lastReport = now
		done := min(bytesDone, totalBytes)
		in.Progress(TxnProgress{BytesDone: done, BytesTotal: totalBytes, FilesDone: filesDone, FilesTotal: len(jobs)})
	}
	if len(jobs) > 0 && in.Progress != nil && totalBytes > 0 {
		emitProgress(true)
	}
	verified := make([]InstallTarget, len(jobs))
	if err := txnRunParallel(ctx, len(jobs), func(pctx context.Context, idx int) error {
		job := jobs[idx]
		var fileRead int64
		got, _, err := txnIdentityLedger(pctx, job.root, job.path, ledger, func(n int64) {
			fileRead += n
			if in.Progress == nil {
				return
			}
			progMu.Lock()
			bytesDone += n
			emitProgress(false)
			progMu.Unlock()
		})
		if err != nil {
			if job.kind == "external" {
				return fmt.Errorf("external source identity: %w", err)
			}
			return err
		}
		got.Path = job.target.Path
		if got != job.target {
			switch job.kind {
			case "stage":
				return fmt.Errorf("%w: staged file mismatch", ErrRecoveryRequired)
			case "external":
				return fmt.Errorf("external source identity: checksum mismatch: %s", job.path)
			default:
				return fmt.Errorf("candidate checksum mismatch: %s", job.target.Path)
			}
		}
		verified[idx] = got
		if in.Progress != nil {
			progMu.Lock()
			if rem := job.target.Size - fileRead; rem > 0 {
				bytesDone += rem
			}
			filesDone++
			emitProgress(false)
			progMu.Unlock()
		}
		return nil
	}); err != nil {
		return err
	}
	for _, b := range bindings {
		got := verified[b.jobIdx]
		got.Path = b.want.Path
		if got != b.want {
			switch b.kind {
			case "stage":
				return fmt.Errorf("%w: staged file mismatch", ErrRecoveryRequired)
			case "external":
				return fmt.Errorf("external source identity: checksum mismatch: %s", b.want.Path)
			default:
				return fmt.Errorf("candidate checksum mismatch: %s", b.want.Path)
			}
		}
	}
	if len(jobs) > 0 && in.Progress != nil && totalBytes > 0 {
		progMu.Lock()
		bytesDone = totalBytes
		filesDone = len(jobs)
		emitProgress(true)
		progMu.Unlock()
	}

	// Phase B: all baseline and candidate source checks passed with zero mutations.
	if in.OnCommitStart != nil {
		if err := in.OnCommitStart(); err != nil {
			return err
		}
	}

	// Phase C: stage verified candidates into txn/<id>/staged/*. Same-volume
	// renames preserve NTFS FileID so post-rename verification hits the ledger.
	for i, e := range j.Files {
		if e.Prune || e.Reuse || stageExists[i] {
			continue
		}
		f := all[i]
		stage := txnStage(j, i)
		if err := txnSafe(r.work, stage, true); err != nil {
			return err
		}
		if err := r.work.MkdirAll(filepath.Dir(stage), 0700); err != nil {
			return err
		}
		if in.SourceRoot != "" && i < len(in.Files) {
			if err := txnStageExternal(ctx, r, j, i, f, in, ledger); err != nil {
				return err
			}
			continue
		}
		if f.Retain {
			if err := txnCopyHashed(ctx, r.work, f.Source, stage, *e.Target, ledger); err != nil {
				return err
			}
			continue
		}
		if err := r.work.Rename(f.Source, stage); err != nil {
			return err
		}
		if err := txnVerifyLedger(ctx, r.work, stage, *e.Target, ledger, nil); err != nil {
			return fmt.Errorf("staging checksum mismatch: %s: %w", e.Path, err)
		}
	}
	return nil
}
func (s *Store) txnCommit(ctx context.Context, r *txnRoots, j *txnJournal, in TxnInput, ledger *txnLedger) (RecoveryStatus, error) {
	blocked := RecoveryStatus{State: "committing", TxnID: j.TxnID, LaunchBlocked: true}
	if err := ctx.Err(); err != nil {
		return blocked, err
	}
	if in.Guard == nil {
		return blocked, errors.New("process guard required")
	}
	if err := in.Guard(ctx); err != nil {
		return blocked, err
	}
	current, err := s.ReadInstall(j.Root)
	if err != nil || !txnBaseMatches(j, current) {
		return txnRequired(errors.New("baseline publication changed before commit"))
	}
	if err := txnCheckSpace(ctx, in, r, ledger); err != nil {
		return blocked, err
	}
	if err := txnValidateBeforeCommit(ctx, r, j, ledger); err != nil {
		return txnRequired(err)
	}
	j.Phase = "committing"
	if err := s.txnSave(j); err != nil {
		return blocked, err
	}
	for _, dir := range []string{"backup", "pruned"} {
		name := txnDir(j) + "/" + dir
		if err := txnSafe(r.work, name, true); err != nil {
			return blocked, err
		}
		if err := r.work.MkdirAll(name, 0700); err != nil {
			return blocked, err
		}
	}
	if err := txnCheckpoint(in, "after-backup-directory"); err != nil {
		return blocked, err
	}
	for i, e := range j.Files {
		if !e.Prune {
			continue
		}
		if err := txnMoveOld(ctx, r, j, i, ledger); err != nil {
			return blocked, err
		}
		if err := txnCheckpoint(in, "after-prune-rename"); err != nil {
			return blocked, err
		}
		if err := s.txnSave(j); err != nil {
			return blocked, err
		}
		if err := txnCheckpoint(in, "after-prune"); err != nil {
			return blocked, err
		}
	}
	backed := false
	for i, e := range j.Files {
		if e.Prune || e.Reuse || e.Old == nil {
			continue
		}
		if err := txnMoveOld(ctx, r, j, i, ledger); err != nil {
			return blocked, err
		}
		if err := txnCheckpoint(in, "after-backup-rename"); err != nil {
			return blocked, err
		}
		if err := s.txnSave(j); err != nil {
			return blocked, err
		}
		if !backed {
			backed = true
			if err := txnCheckpoint(in, "after-first-backup"); err != nil {
				return blocked, err
			}
		}
	}
	if err := txnCheckpoint(in, "after-all-backups"); err != nil {
		return blocked, err
	}
	placed := false
	for i, e := range j.Files {
		if e.Prune || e.Reuse {
			continue
		}
		if err := ctx.Err(); err != nil {
			return blocked, err
		}
		existing, _, xerr := txnIdentityLedger(ctx, r.install, e.Path, ledger, nil)
		if xerr == nil {
			if j.Kind == "install" && existing == *e.Target {
				continue
			}
			return blocked, fmt.Errorf("%w: %s", ErrTargetExists, e.Path)
		}
		if !errors.Is(xerr, os.ErrNotExist) {
			return blocked, xerr
		}
		stage := txnStage(j, i)
		if err := txnVerifyLedger(ctx, r.work, stage, *e.Target, ledger, nil); err != nil {
			return blocked, err
		}
		if err := s.txnCreateParents(r, j, e.Path); err != nil {
			return blocked, err
		}
		if err := txnSafe(r.install, e.Path, true); err != nil {
			return blocked, err
		}
		if err := r.install.Rename(r.workRel+"/"+stage, e.Path); err != nil {
			return blocked, err
		}
		if err := txnCheckpoint(in, "after-placement-rename"); err != nil {
			return blocked, err
		}
		j.Files[i].Step = "placed"
		if err := s.txnSave(j); err != nil {
			return blocked, err
		}
		if !placed {
			placed = true
			if err := txnCheckpoint(in, "after-first-placement"); err != nil {
				return blocked, err
			}
		}
	}
	if err := txnCheckpoint(in, "after-all-placements"); err != nil {
		return blocked, err
	}
	if err := txnVerifyCandidate(ctx, r, j, ledger); err != nil {
		return txnRequired(err)
	}
	if err := in.Guard(ctx); err != nil {
		return blocked, err
	}
	j.Candidate.PublishedAt = time.Now().UTC()
	if err := s.writeInstallCheckpoint(ctx, j.Candidate, func() error { return txnCheckpoint(in, "before-install-rename") }); err != nil {
		return blocked, err
	}
	published := RecoveryStatus{State: "ready", TxnID: j.TxnID, Published: true, Action: "published"}
	if err := txnCheckpoint(in, "after-install-rename"); err != nil {
		return published, err
	}
	j.Phase = "committed"
	j.PublishedAt = j.Candidate.PublishedAt
	j.RetainUntil = j.PublishedAt.Add(j.Retention)
	if err := s.txnSave(j); err != nil {
		return published, err
	}
	if err := txnCheckpoint(in, "after-committed"); err != nil {
		return published, err
	}
	return s.txnFinish(ctx, r, j, in, ledger)
}
func txnMoveOld(ctx context.Context, r *txnRoots, j *txnJournal, i int, ledger *txnLedger) error {
	e := j.Files[i]
	if e.Old == nil {
		return ErrInvalidState
	}
	if err := txnVerifyLedger(ctx, r.install, e.Path, *e.Old, ledger, nil); err != nil {
		return err
	}
	backup := txnBackup(j, i)
	if err := txnSafe(r.work, backup, true); err != nil {
		return err
	}
	if _, err := r.work.Lstat(backup); !errors.Is(err, os.ErrNotExist) {
		return errors.New("backup destination already exists")
	}
	if err := r.install.Rename(e.Path, r.workRel+"/"+backup); err != nil {
		return err
	}
	j.Files[i].Step = "backed_up"
	if e.Prune {
		j.Files[i].Step = "pruned"
	}
	return nil
}
func (s *Store) txnCreateParents(r *txnRoots, j *txnJournal, name string) error {
	parts := strings.Split(name, "/")
	path := ""
	for _, part := range parts[:len(parts)-1] {
		if path != "" {
			path += "/"
		}
		path += part
		if err := txnSafe(r.install, path, true); err != nil {
			return err
		}
		st, err := r.install.Lstat(path)
		if err == nil {
			if !st.IsDir() {
				return ErrUnsafeTarget
			}
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		j.CreatedDirs = append(j.CreatedDirs, path)
		if err := s.txnSave(j); err != nil {
			return err
		}
		if err := r.install.Mkdir(path, 0700); err != nil {
			return err
		}
	}
	return nil
}
