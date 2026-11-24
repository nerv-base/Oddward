// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/nerv-base/Oddward/internal/cnlogin/loginui"
	"github.com/nerv-base/Oddward/internal/overlaywin"
)

// loginUIExecutable resolves an explicit helper path or the development WPF
// sidecar next to the executable.
func loginUIExecutable(executable, override string) string {
	if override != "" {
		if absolute, err := filepath.Abs(override); err == nil {
			return absolute
		}
		return override
	}
	return filepath.Join(filepath.Dir(executable), "login-compat", "Oddward.LoginUi.exe")
}

// resolveLoginUI returns the executable and fixed role arguments for the
// isolated login window helper.
//
// By default it invokes the running launcher image itself with loginui.Mode
// (pure Go + MyGo Native UI, zero .NET dependency). Passing -login-ui=wpf
// (or ODDWARD_LOGIN_UI=wpf) or an explicit helper path selects the temporary
// Phase 3 WPF fallback helper.
func resolveLoginUI(executable, override string) (string, []string, error) {
	override = strings.TrimSpace(override)
	if override == "" {
		// Prefer the kernel's running image path over os.Executable() so a
		// renamed/replaced image still launches the exact matching binary.
		if image, err := overlaywin.CurrentImagePath(); err == nil && image != "" {
			return image, []string{loginui.Mode}, nil
		}
		return executable, []string{loginui.Mode}, nil
	}
	if strings.EqualFold(override, "wpf") {
		path, err := embeddedLoginUI()
		if err == nil && path != "" {
			return path, nil, nil
		}
		sidecar := loginUIExecutable(executable, "")
		if info, statErr := os.Stat(sidecar); statErr == nil && info.Mode().IsRegular() {
			return sidecar, nil, nil
		}
		return "", nil, err
	}
	return loginUIExecutable(executable, override), nil, nil
}
