//go:build e2e

// SPDX-License-Identifier: GPL-3.0-or-later

package main

// Test builds only. The release does not open an automation socket.
import _ "github.com/egoist/mygo/plugins/automation"
