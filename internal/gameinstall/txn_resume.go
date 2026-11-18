// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"context"
	"errors"
)

// ResumeCommit resumes only a durable validated candidate. The caller holds the
// same gamedl.Acquire(workRoot) lease used for Recover and download; Guard is
// mandatory. It rechecks process exclusion, space, baseline and every staged or
// already placed file. No in-memory download plan or source path is trusted.
func (s *Store) ResumeCommit(ctx context.Context, root, workRoot string, guard func(context.Context) error, free func(context.Context) (int64, error), required int64) (RecoveryStatus, error) {
	if err := ctx.Err(); err != nil {
		return RecoveryStatus{}, err
	}
	if guard == nil || required < 0 {
		return RecoveryStatus{}, errors.New("resume requires process guard, nonnegative space requirement and caller-held lease")
	}
	if err := guard(ctx); err != nil {
		return RecoveryStatus{}, err
	}
	roots, err := txnOpenRoots(root, workRoot)
	if err != nil {
		return txnRequired(err)
	}
	defer roots.close()
	journals, err := s.txnReadActive(root, workRoot)
	if err != nil {
		return txnRequired(err)
	}
	if len(journals) != 1 {
		return txnRequired(errors.New("resume requires exactly one durable transaction"))
	}
	j := journals[0]
	record, err := s.ReadInstall(root)
	if err != nil {
		return txnRequired(err)
	}
	if txnPublished(j, record) {
		return s.txnFinish(ctx, roots, j, TxnInput{})
	}
	if !txnBaseMatches(j, record) {
		return txnRequired(errors.New("resume baseline identity changed"))
	}
	if j.Kind == "install" {
		if err := txnValidateFreshResume(ctx, roots, j); err != nil {
			return txnRequired(err)
		}
	} else if j.Phase != "staged" {
		return txnRequired(errors.New("only staged replacements may resume"))
	}
	status, err := s.txnCommit(ctx, roots, j, TxnInput{Root: root, WorkRoot: workRoot, Guard: guard, FreeBytes: free, RequiredFreeBytes: required})
	if err != nil {
		var stop txnStop
		if errors.As(err, &stop) {
			return status, err
		}
		recovered, rerr := s.txnRecoverOne(context.WithoutCancel(ctx), roots, j, guard)
		if rerr != nil {
			return recovered, errors.Join(err, rerr)
		}
	}
	return status, err
}
