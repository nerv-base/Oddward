// SPDX-License-Identifier: GPL-3.0-or-later

// Package state is the launcher's pure state machine.
//
// Model is the complete launcher state; Reduce applies one Event and returns
// the next Model plus the Effects the shell must run. Present maps a Model to
// the copy and affordances the launch button shows. Nothing here performs I/O,
// reads the clock, or depends on the UI framework, so every rule (task flows,
// stale-token rejection, managed phases, news projection, demo
// features) is testable as plain data.
package state
