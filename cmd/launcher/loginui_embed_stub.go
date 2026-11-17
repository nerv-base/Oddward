// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !loginui_embed

package main

// embeddedLoginUI is empty in development builds, where the WPF helper is read
// from login-compat next to the executable.
func embeddedLoginUI() (string, error) { return "", nil }
