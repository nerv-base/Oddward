// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"context"
	"errors"
	"os"
	"time"
)

// Expired backup cleanup remains under the same caller-held lease as Recover.
// A later successful update can supersede retained backups; in that case verify
// the complete currently published target set before deleting the older archive.
// An archive is never authority to change the installation or roll back files.
func (s *Store) txnExpireArchives(ctx context.Context, r *txnRoots, root, work string) error {
	archives, err := s.txnReadAt(root, work, "retained")
	if err != nil {
		return err
	}
	for _, j := range archives {
		if j.Phase != "committed" || j.PublishedAt.IsZero() || j.RetainUntil.IsZero() {
			return ErrInvalidState
		}
		if time.Now().Before(j.RetainUntil) {
			continue
		}
		current, err := s.ReadInstall(root)
		if err != nil {
			return err
		}
		if current.State != "ready" || current.TxnID == "" || current.VerifyCoverage != "full" || current.VerifiedTargets != len(current.Targets) {
			return errors.New("retained backup cleanup requires verified publication")
		}
		for _, target := range current.Targets {
			if err := txnVerify(ctx, r.install, target.Path, target); err != nil {
				return err
			}
		}
		if err := s.txnClear(ctx, r, j, TxnInput{}, true); err != nil {
			return err
		}
		sr, err := os.OpenRoot(s.stateRoot)
		if err != nil {
			return err
		}
		name := j.InstallKey + "/retained/" + j.TxnID + ".json"
		err = sr.Remove(name)
		sr.Close()
		if err != nil {
			return txnCleanupIO{err}
		}
	}
	return nil
}
