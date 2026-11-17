// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func newApplicationShell(demo bool) *launcher.Shell {
	model := state.InitialNormal()
	if demo {
		model = state.Initial()
	}
	shell := launcher.NewShell(model)
	shell.FixedWindow = true
	return shell
}
