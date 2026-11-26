// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func txnRebuildTargetValid(t InstallTarget) error {
	if err := txnSourceValid(t.Path); err != nil {
		return err
	}
	if !strings.HasPrefix(t.Path, "rebuild/") || !stateValidMD5(t.MD5) || t.Size <= 0 || t.Size > 32<<30 {
		return ErrInvalidState
	}
	return nil
}
func txnCleanupRebuild(ctx context.Context, r *txnRoots, j *txnJournal, ledger *txnLedger) error {
	// Check every recorded surviving intermediate before deleting any of them.
	// Missing files are expected after an interrupted cleanup; modified files are
	// evidence of a conflict and remain untouched.
	for _, target := range j.RebuildCleanup {
		if err := txnVerifyLedger(ctx, r.work, target.Path, target, ledger, nil); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	for _, target := range j.RebuildCleanup {
		if err := txnSafe(r.work, target.Path, true); err != nil {
			return err
		}
		if err := r.work.Remove(target.Path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return txnCleanupIO{err}
		}
		for parent := filepath.ToSlash(filepath.Dir(target.Path)); parent != "." && parent != "rebuild"; parent = filepath.ToSlash(filepath.Dir(parent)) {
			if err := txnSafe(r.work, parent, true); err != nil {
				return err
			}
			// Only remove empty directories. Other tasks and unrelated files may share
			// rebuild/, so an occupied parent is deliberately preserved.
			dir, err := r.work.Open(parent)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			entries, err := dir.ReadDir(1)
			dir.Close()
			if len(entries) > 0 {
				break
			}
			if err != nil && !errors.Is(err, os.ErrNotExist) && !errors.Is(err, io.EOF) {
				return err
			}
			if err := r.work.Remove(parent); err != nil && !errors.Is(err, os.ErrNotExist) {
				return txnCleanupIO{err}
			}
		}
	}
	return nil
}
