// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"path/filepath"
)

// loginUIExecutable resolves the authored WPF login window helper. Release
// builds embed it in the launcher; development builds may also read it next to
// the executable, and -login-ui overrides either source.
func loginUIExecutable(executable, override string) string {
	if override != "" {
		if absolute, err := filepath.Abs(override); err == nil {
			return absolute
		}
		return override
	}
	return filepath.Join(filepath.Dir(executable), "login-compat", "Oddward.LoginUi.exe")
}

// resolveLoginUI prefers an explicit override, then the helper embedded in a
// release build, and finally the development sidecar next to the executable.
func resolveLoginUI(executable, override string) string {
	if override != "" {
		return loginUIExecutable(executable, override)
	}
	if path, err := embeddedLoginUI(); err == nil && path != "" {
		return path
	}
	return loginUIExecutable(executable, "")
}
