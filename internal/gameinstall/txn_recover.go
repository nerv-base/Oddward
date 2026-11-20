// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// TransactionStatus reads durable evidence without changing game files. Callers
// must use LaunchBlocked as a launch/download/GC gate until Recover resolves it.
// The caller must hold the installation work-root lease while inspecting state.
func (s *Store) TransactionStatus(ctx context.Context, root, workRoot string) (RecoveryStatus, error) {
	if err := ctx.Err(); err != nil {
		return RecoveryStatus{}, err
	}
	js, err := s.txnReadActive(root, workRoot)
	if err != nil {
		return txnRequired(err)
	}
	if len(js) > 1 {
		return txnRequired(errors.New("multiple active transactions"))
	}
	if len(js) == 0 {
		record, err := s.ReadInstall(root)
		if errors.Is(err, os.ErrNotExist) {
			return RecoveryStatus{State: "absent"}, nil
		}
		if err != nil {
			return txnRequired(err)
		}
		blocked := record.State == "incomplete" || record.State == "installing" || record.State == "recovery_required"
		return RecoveryStatus{State: record.State, TxnID: record.TxnID, Published: record.State == "ready" && record.TxnID != "", LaunchBlocked: blocked, Resume: blocked}, nil
	}
	j := js[0]
	r, err := s.ReadInstall(root)
	if err != nil {
		return txnRequired(err)
	}
	if !txnPublished(j, r) && !txnBaseMatches(j, r) {
		return txnRequired(errors.New("journal conflicts with install record"))
	}
	if j.Phase == "cleanup_pending" {
		if !txnPublished(j, r) {
			return txnRequired(ErrInvalidState)
		}
		roots, err := txnOpenRoots(root, workRoot)
		if err != nil {
			return txnRequired(err)
		}
		defer roots.close()
		if err := txnVerifyCandidate(ctx, roots, j); err != nil {
			return txnRequired(err)
		}
		if err := txnCleanupEvidence(ctx, roots, j); err != nil {
			return txnRequired(err)
		}
		return RecoveryStatus{State: "ready", TxnID: j.TxnID, Published: true, Action: "cleanup_pending"}, nil
	}
	return RecoveryStatus{State: j.Phase, TxnID: j.TxnID, LaunchBlocked: true, Resume: j.Kind == "install" || j.Phase == "staged" || j.Phase == "preparing", Published: txnPublished(j, r)}, nil
}

// Recover requires the caller-held download/work-root lease and a process Guard.
// Unpublished replacement commits roll back; positively published candidates only
// finish cleanup. Fresh interruptions keep verified files and require explicit
// Commit with the original transaction ID. Ambiguous evidence is never guessed.
func (s *Store) Recover(ctx context.Context, root, workRoot string, guard func(context.Context) error) (status RecoveryStatus, resultErr error) {
	defer func() {
		if resultErr != nil {
			return
		}
		key, err := InstallKey(root)
		if err != nil {
			resultErr = err
			return
		}
		action := status.Action
		if action == "" {
			action = "none"
		}
		receipt := struct {
			Schema        int       `json:"schema"`
			Time          time.Time `json:"time"`
			TxnID         string    `json:"txnID,omitempty"`
			Action        string    `json:"action"`
			State         string    `json:"state"`
			Published     bool      `json:"published"`
			LaunchBlocked bool      `json:"launchBlocked"`
		}{2, time.Now().UTC(), status.TxnID, action, status.State, status.Published, status.LaunchBlocked}
		if err := s.stateWrite(key+"/recovery.json", receipt, nil); err != nil {
			resultErr = err
		}
	}()
	if err := ctx.Err(); err != nil {
		return RecoveryStatus{}, err
	}
	if guard == nil {
		return RecoveryStatus{}, errors.New("recovery requires process guard and caller-held lease")
	}
	if err := guard(ctx); err != nil {
		return RecoveryStatus{}, err
	}
	r, err := txnOpenRoots(root, workRoot)
	if err != nil {
		return txnRequired(err)
	}
	defer r.close()
	js, err := s.txnReadActive(root, workRoot)
	if err != nil {
		return txnRequired(err)
	}
	if len(js) > 1 {
		return txnRequired(errors.New("multiple active transactions"))
	}
	if len(js) == 0 {
		record, readErr := s.ReadInstall(root)
		if readErr == nil && (record.State == "installing" || record.State == "incomplete") {
			record.State = "incomplete"
			if err := s.writeInstall(ctx, record); err != nil {
				return txnRequired(err)
			}
			return RecoveryStatus{State: "incomplete", LaunchBlocked: true, Resume: true, Action: "resume"}, nil
		}
		if err := s.txnExpireArchives(ctx, r, root, workRoot); err != nil {
			var ioFailure txnCleanupIO
			if errors.As(err, &ioFailure) && readErr == nil && record.State == "ready" {
				return RecoveryStatus{State: "ready", TxnID: record.TxnID, Published: true, Action: "cleanup_pending"}, fmt.Errorf("%w: %w", ErrCleanupPending, err)
			}
			return txnRequired(err)
		}
		return s.TransactionStatus(ctx, root, workRoot)
	}
	return s.txnRecoverOne(ctx, r, js[0], guard)
}
func (s *Store) txnReadActive(root, work string) ([]*txnJournal, error) {
	return s.txnReadAt(root, work, "txn")
}
func (s *Store) txnReadAt(root, work, folder string) ([]*txnJournal, error) {
	key, err := InstallKey(root)
	if err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(s.stateRoot)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	dir := key + "/" + folder
	if err := txnSafe(r, dir, false); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	f, err := r.Open(dir)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return nil, err
	}
	var out []*txnJournal
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			return nil, ErrInvalidState
		}
		id := strings.TrimSuffix(e.Name(), ".json")
		if !txnIDValid(id) {
			return nil, ErrInvalidState
		}
		name := dir + "/" + e.Name()
		if err := txnSafe(r, name, false); err != nil {
			return nil, err
		}
		data, err := stateReadBounded(r, name)
		if err != nil {
			return nil, err
		}
		var j txnJournal
		if err := stateDecodeJSON(data, &j); err != nil {
			return nil, err
		}
		if j.TxnID != id {
			return nil, ErrInvalidState
		}
		if err := txnValidateJournal(&j, root, work); err != nil {
			return nil, err
		}
		out = append(out, &j)
	}
	return out, nil
}
func txnValidateJournal(j *txnJournal, root, work string) error {
	key, err := InstallKey(root)
	if err != nil || j.Schema != 2 || j.InstallKey != key || !txnIDValid(j.TxnID) || !strings.EqualFold(filepath.Clean(j.Root), filepath.Clean(root)) || !strings.EqualFold(filepath.Clean(j.WorkRoot), filepath.Clean(work)) || j.CreatedAt.IsZero() || j.Retention < 0 || j.Retention > 168*time.Hour {
		return ErrInvalidState
	}
	if j.Kind != "update" && j.Kind != "install" {
		return ErrInvalidState
	}
	switch j.Phase {
	case "preparing", "staged", "committing", "committed", "rolling_back", "cleanup_pending":
	default:
		return ErrInvalidState
	}
	if err := stateValidateRecord(j.Base); err != nil {
		return err
	}
	if err := stateValidateRecord(j.Candidate); err != nil {
		return err
	}
	if j.Base.InstallKey != key || j.Candidate.InstallKey != key || j.Candidate.TxnID != j.TxnID || j.Candidate.State != "ready" || j.Candidate.VerifyCoverage != "full" || j.Candidate.VerifiedTargets != len(j.Candidate.Targets) || len(j.Files) == 0 || len(j.Files) > 131074 {
		return ErrInvalidState
	}
	targetMap := map[string]InstallTarget{}
	for _, t := range j.Candidate.Targets {
		targetMap[t.Path] = t
	}
	baseMap := map[string]InstallTarget{}
	for _, t := range j.Base.Targets {
		baseMap[t.Path] = t
	}
	seen := map[string]bool{}
	parents := map[string]bool{}
	found := 0
	for _, e := range j.Files {
		switch e.Step {
		case "none", "staged", "backed_up", "placed", "pruned":
		default:
			return ErrInvalidState
		}
		if e.Target == nil && e.Old == nil || e.Prune && (e.Target != nil || e.Old == nil || e.Reuse) || e.Reuse && e.Target == nil {
			return ErrInvalidState
		}
		key := strings.ToUpper(e.Path)
		if seen[key] {
			return ErrInvalidState
		}
		seen[key] = true
		for _, t := range []*InstallTarget{e.Old, e.Target} {
			if t != nil {
				if err := txnTargetValid(*t); err != nil || t.Path != e.Path {
					return ErrInvalidState
				}
			}
		}
		if e.Target != nil {
			if t, ok := targetMap[e.Path]; ok {
				if t != *e.Target {
					return ErrInvalidState
				}
				found++
			} else if j.Kind != "install" || !txnBootstrapPath(e.Path) {
				return ErrInvalidState
			}
		}
		if e.Prune {
			if t, ok := baseMap[e.Path]; !ok || t != *e.Old {
				return ErrInvalidState
			}
			if _, exists := targetMap[e.Path]; exists {
				return ErrInvalidState
			}
		}
		for p := filepath.ToSlash(filepath.Dir(e.Path)); p != "."; p = filepath.ToSlash(filepath.Dir(p)) {
			parents[p] = true
		}
	}
	if found != len(j.Candidate.Targets) {
		return ErrInvalidState
	}
	for p := range seen {
		for parent := filepath.ToSlash(filepath.Dir(p)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
			if seen[parent] {
				return ErrInvalidState
			}
		}
	}
	dirs := map[string]bool{}
	for _, p := range j.CreatedDirs {
		if !parents[p] || dirs[p] {
			return ErrInvalidState
		}
		dirs[p] = true
	}
	cleanupSeen := map[string]bool{}
	for _, target := range j.RebuildCleanup {
		if err := txnRebuildTargetValid(target); err != nil {
			return err
		}
		key := strings.ToUpper(target.Path)
		if cleanupSeen[key] {
			return ErrInvalidState
		}
		cleanupSeen[key] = true
	}
	return nil
}
func (s *Store) txnRecoverOne(ctx context.Context, r *txnRoots, j *txnJournal, guard func(context.Context) error) (RecoveryStatus, error) {
	record, err := s.ReadInstall(j.Root)
	if err != nil {
		return txnRequired(err)
	}
	if txnPublished(j, record) {
		if err := guard(ctx); err != nil {
			return txnRequired(err)
		}
		return s.txnFinish(ctx, r, j, TxnInput{})
	}
	if !txnBaseMatches(j, record) {
		return txnRequired(errors.New("neither exact base nor transaction publication matches"))
	}
	if j.Kind == "install" {
		if err := txnValidateFreshResume(ctx, r, j); err != nil {
			return txnRequired(err)
		}
		record.State = "incomplete"
		if err := s.writeInstall(ctx, record); err != nil {
			return txnRequired(err)
		}
		return RecoveryStatus{State: "incomplete", TxnID: j.TxnID, LaunchBlocked: true, Resume: true, Action: "resume"}, nil
	}
	if j.Phase == "preparing" {
		if err := txnValidateBeforeCommit(ctx, r, j); err != nil {
			return txnRequired(err)
		}
		if err := s.txnClear(ctx, r, j, TxnInput{}, false); err != nil {
			return txnRequired(err)
		}
		return RecoveryStatus{State: record.State, TxnID: j.TxnID, Action: "discarded"}, nil
	}
	if j.Phase == "staged" {
		if err := txnValidateBeforeCommit(ctx, r, j); err != nil {
			return txnRequired(err)
		}
		return RecoveryStatus{State: j.Phase, TxnID: j.TxnID, LaunchBlocked: true, Resume: true, Action: "resume"}, nil
	}
	if j.Phase == "committed" {
		return txnRequired(errors.New("committed journal without matching publication"))
	}
	// Validate every old and candidate hash before the first rollback mutation.
	// Missing or conflicting backup evidence keeps the entire journal in place.
	if err := txnValidateRollback(ctx, r, j); err != nil {
		return txnRequired(err)
	}
	if err := guard(ctx); err != nil {
		return txnRequired(err)
	}
	j.Phase = "rolling_back"
	if err := s.txnSave(j); err != nil {
		return txnRequired(err)
	}
	for i := len(j.Files) - 1; i >= 0; i-- {
		e := j.Files[i]
		if e.Reuse {
			continue
		}
		backup := txnBackup(j, i)
		oldPresent := false
		if e.Old != nil {
			oldPresent = txnVerify(ctx, r.install, e.Path, *e.Old) == nil
		}
		if oldPresent {
			continue
		}
		if e.Target != nil {
			if _, err := r.install.Lstat(e.Path); err == nil {
				stage := txnStage(j, i)
				if err := txnSafe(r.work, stage, true); err != nil {
					return txnRequired(err)
				}
				if err := r.work.MkdirAll(filepath.Dir(stage), 0700); err != nil {
					return txnRequired(err)
				}
				if err := r.install.Rename(e.Path, r.workRel+"/"+stage); err != nil {
					return txnRequired(err)
				}
			} else if !errors.Is(err, os.ErrNotExist) {
				return txnRequired(err)
			}
		}
		if e.Old != nil {
			if err := txnSafe(r.install, e.Path, true); err != nil {
				return txnRequired(err)
			}
			if err := r.install.Rename(r.workRel+"/"+backup, e.Path); err != nil {
				return txnRequired(err)
			}
		}
	}
	for i := len(j.CreatedDirs) - 1; i >= 0; i-- {
		if err := txnSafe(r.install, j.CreatedDirs[i], true); err != nil {
			return txnRequired(err)
		}
		if err := r.install.Remove(j.CreatedDirs[i]); err != nil && !errors.Is(err, os.ErrNotExist) {
			return txnRequired(err)
		}
	}
	if err := s.txnClear(ctx, r, j, TxnInput{}, false); err != nil {
		return txnRequired(err)
	}
	return RecoveryStatus{State: record.State, TxnID: j.TxnID, Action: "rolled_back"}, nil
}
func txnValidateFreshResume(ctx context.Context, r *txnRoots, j *txnJournal) error {
	for i, e := range j.Files {
		if e.Prune || e.Old != nil {
			return ErrInvalidState
		}
		actual, err := txnIdentity(ctx, r.install, e.Path)
		if err == nil {
			if e.Target == nil || actual != *e.Target {
				return ErrTargetExists
			}
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := txnVerify(ctx, r.work, txnStage(j, i), *e.Target); err != nil {
			return err
		}
	}
	return nil
}
func txnValidateRollback(ctx context.Context, r *txnRoots, j *txnJournal) error {
	for i, e := range j.Files {
		actual, err := txnIdentity(ctx, r.install, e.Path)
		present := err == nil
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if e.Reuse {
			if !present || actual != *e.Target {
				return errors.New("reused file changed")
			}
			continue
		}
		backup, berr := txnIdentity(ctx, r.work, txnBackup(j, i))
		backupPresent := berr == nil
		if berr != nil && !errors.Is(berr, os.ErrNotExist) {
			return berr
		}
		if backupPresent {
			if e.Old == nil {
				return errors.New("unexpected backup")
			}
			backup.Path = e.Path
			if backup != *e.Old {
				return errors.New("backup checksum changed")
			}
		}
		stage, serr := txnIdentity(ctx, r.work, txnStage(j, i))
		stagePresent := serr == nil
		if serr != nil && !errors.Is(serr, os.ErrNotExist) {
			return serr
		}
		if stagePresent {
			stage.Path = e.Path
			if e.Target == nil || stage != *e.Target {
				return errors.New("candidate staging changed")
			}
		}
		if e.Old != nil {
			if present && actual == *e.Old {
				if backupPresent {
					return errors.New("both old file and backup exist")
				}
				if e.Target != nil && !stagePresent {
					return errors.New("candidate stage missing before placement")
				}
				continue
			}
			if !backupPresent {
				return fmt.Errorf("backup missing: %s", e.Path)
			}
		} else if backupPresent {
			return ErrInvalidState
		}
		if present && (e.Target == nil || actual != *e.Target) {
			return errors.New("placed candidate changed")
		}
		if present && stagePresent {
			return errors.New("both candidate and staging exist")
		}
		if e.Target != nil && !present && !stagePresent {
			return errors.New("candidate and staging both missing")
		}
	}
	return nil
}
func (s *Store) txnFinish(ctx context.Context, r *txnRoots, j *txnJournal, in TxnInput) (RecoveryStatus, error) {
	if err := txnVerifyCandidate(ctx, r, j); err != nil {
		return txnRequired(err)
	}
	record, err := s.ReadInstall(j.Root)
	if err != nil || !txnPublished(j, record) {
		return txnRequired(errors.New("publication identity changed during cleanup"))
	}
	j.Phase = "committed"
	if j.PublishedAt.IsZero() {
		j.PublishedAt = record.PublishedAt
	}
	if j.PublishedAt.IsZero() {
		return txnRequired(ErrInvalidState)
	}
	if j.RetainUntil.IsZero() {
		j.RetainUntil = j.PublishedAt.Add(j.Retention)
	}
	if err := s.txnSave(j); err != nil {
		return txnRequired(err)
	}
	if j.Retention > 0 && time.Now().Before(j.RetainUntil) {
		// Archive only after successful publication+verification. Archived committed
		// backups do not gate a subsequent update and are never rollback evidence.
		if err := s.stateWrite(j.InstallKey+"/retained/"+j.TxnID+".json", j, nil); err != nil {
			return txnRequired(err)
		}
		if err := s.txnRemoveJournal(j); err != nil {
			return txnRequired(err)
		}
		return RecoveryStatus{State: "ready", TxnID: j.TxnID, Published: true, Action: "retained"}, nil
	}
	if err := s.txnClear(ctx, r, j, in, true); err != nil {
		var ioFailure txnCleanupIO
		if errors.As(err, &ioFailure) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return s.txnCleanupPending(j, err)
		}
		return txnRequired(err)
	}
	return RecoveryStatus{State: "ready", TxnID: j.TxnID, Published: true, Action: "cleaned"}, nil
}
func (s *Store) txnRemoveJournal(j *txnJournal) error {
	r, err := os.OpenRoot(s.stateRoot)
	if err != nil {
		return err
	}
	defer r.Close()
	err = r.Remove(txnJournalName(j))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
func (s *Store) txnCleanupPending(j *txnJournal, err error) (RecoveryStatus, error) {
	j.Phase = "cleanup_pending"
	if e := s.txnSave(j); e != nil {
		err = errors.Join(err, e)
	}
	return RecoveryStatus{State: "ready", TxnID: j.TxnID, Published: true, Action: "cleanup_pending"}, fmt.Errorf("%w: %w", ErrCleanupPending, err)
}
func txnCleanupEvidence(ctx context.Context, r *txnRoots, j *txnJournal) error {
	for i, e := range j.Files {
		if e.Old != nil && !e.Reuse {
			if err := txnVerify(ctx, r.work, txnBackup(j, i), *e.Old); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
		if e.Target != nil {
			if err := txnVerify(ctx, r.work, txnStage(j, i), *e.Target); err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
		}
	}
	for _, target := range j.RebuildCleanup {
		if err := txnVerify(ctx, r.work, target.Path, target); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
func (s *Store) txnClear(ctx context.Context, r *txnRoots, j *txnJournal, in TxnInput, published bool) error {
	deletedBackup, deletedPrune := false, false
	if published {
		if err := txnCleanupEvidence(ctx, r, j); err != nil {
			return err
		}
	}
	for i, e := range j.Files {
		if err := ctx.Err(); err != nil {
			return err
		}
		if published && e.Old != nil && !e.Reuse {
			name := txnBackup(j, i)
			err := txnVerify(ctx, r.work, name, *e.Old)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err == nil {
				if err := r.work.Remove(name); err != nil {
					return txnCleanupIO{err}
				}
				if e.Prune && !deletedPrune {
					deletedPrune = true
					if err := txnCheckpoint(in, "after-first-pruned-delete"); err != nil {
						return err
					}
				} else if !e.Prune && !deletedBackup {
					deletedBackup = true
					if err := txnCheckpoint(in, "after-first-backup-delete"); err != nil {
						return err
					}
				}
			}
		}
		stage := txnStage(j, i)
		if e.Target != nil {
			err := txnVerify(ctx, r.work, stage, *e.Target)
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return err
			}
			if err == nil {
				if err := r.work.Remove(stage); err != nil {
					return txnCleanupIO{err}
				}
			}
		}
	}
	if err := txnCheckpoint(in, "before-rebuild-cleanup"); err != nil {
		return err
	}
	if published {
		if err := txnCleanupRebuild(ctx, r, j); err != nil {
			return err
		}
	}
	for _, dir := range []string{"staged", "backup", "pruned", "rebuild", ""} {
		name := txnDir(j)
		if dir != "" {
			name += "/" + dir
		}
		if err := txnSafe(r.work, name, true); err != nil {
			return err
		}
		if err := r.work.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
			return txnCleanupIO{err}
		}
	}
	if err := s.txnRemoveJournal(j); err != nil {
		return txnCleanupIO{err}
	}
	return nil
}
