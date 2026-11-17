// SPDX-License-Identifier: GPL-3.0-or-later

package game

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// resolveLocalPath deliberately does not follow symbolic links or directory
// junctions in the selected path or a discovery candidate. Checking only the drive
// letter, then calling EvalSymlinks, could resolve a locally named link to SMB
// before a remote-target check gets a chance to reject it.
// This is a conservative milestone policy, not a hostile-race-proof authorization
// capability. Root-scoped file opens and use-time validation remain necessary.
func resolveLocalPath(ctx context.Context, path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if err := checkSupportedPath(abs); err != nil {
		return "", err
	}
	volume, err := volumeRoot(abs)
	if err != nil {
		return "", err
	}
	current := volume
	for _, component := range strings.Split(filepath.FromSlash(abs[len(volume):]), string(filepath.Separator)) {
		if component == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return "", err
		}
		// Since Go 1.23, Windows junctions/mount points are ModeIrregular,
		// not ModeSymlink (os/types_windows.go). Reject both before walking
		// another component; a symlink-only guard silently follows junctions.
		if info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 {
			return "", fmt.Errorf("linked or irregular installation path %q is unsupported; select a direct local path", current)
		}
	}
	return abs, nil
}
