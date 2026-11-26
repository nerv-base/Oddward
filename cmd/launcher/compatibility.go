// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"path/filepath"
	"strings"

	"github.com/nerv-base/Oddward/internal/cnlogin/loginui"
	"github.com/nerv-base/Oddward/internal/overlaywin"
)

// resolveLoginUI returns the executable and fixed role arguments for the
// isolated login window helper.
//
// By default it invokes the running launcher image itself with loginui.Mode
// (pure Go + MyGo Native UI). Passing an explicit -login-ui (or
// ODDWARD_LOGIN_UI) path overrides the helper executable.
func resolveLoginUI(executable, override string) (string, []string) {
	override = strings.TrimSpace(override)
	if override == "" {
		// Prefer the kernel's running image path over os.Executable() so a
		// renamed/replaced image still launches the exact matching binary.
		if image, err := overlaywin.CurrentImagePath(); err == nil && image != "" {
			return image, []string{loginui.Mode}
		}
		return executable, []string{loginui.Mode}
	}
	if absolute, err := filepath.Abs(override); err == nil {
		return absolute, nil
	}
	return override, nil
}
