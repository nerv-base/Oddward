// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

// Package session drives one native CN login attempt from game start to
// identity delivery.
//
// A Session owns the SDK IPC host, starts the game with that host's pipe in its
// environment, and turns the isolated login window into a delivered game
// identity. It never persists a password and never delivers an identity before
// the pre-login checks allowed it (see laohu.Admission).
//
// The flow is deliberately split from its Windows implementations: Host, Game,
// UI and Accounts are interfaces, so the whole login state machine runs in
// tests with fakes while the real wiring lives in wiring_windows.go.
package session
