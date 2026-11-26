// SPDX-License-Identifier: GPL-3.0-or-later
package gameinstall

import (
	"os"

	"github.com/nerv-base/Oddward/internal/gamelocate"
	"github.com/nerv-base/Oddward/internal/gameres"
)

// ReadOfficialVersion reads the patcher's installed version for an inspected
// installation. It supplies a version hint, never verified resource targets.
func ReadOfficialVersion(found gamelocate.Installation) (string, error) {
	root, err := os.OpenRoot(found.Root)
	if err != nil {
		return "", err
	}
	defer root.Close()
	data, err := stateReadBounded(root, adoptOfficialDir+"config.xml")
	if err != nil {
		return "", err
	}
	cfg, err := gameres.ParseConfig(data)
	if err != nil {
		return "", err
	}
	if cfg.LocalBranch != "" && cfg.LocalBranch != found.Branch {
		return "", ErrStaleState
	}
	return cfg.ResVersion, nil
}
