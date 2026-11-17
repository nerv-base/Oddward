// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !windows

package game

import "errors"

// checkLocalVolume reports that volume classification is a Windows-only capability. The
// supported platform is Windows 10/11 x64, so any other target rejects selections instead
// of guessing which volumes are local.
func checkLocalVolume(string) error {
	return errors.New("volume classification requires Windows")
}
