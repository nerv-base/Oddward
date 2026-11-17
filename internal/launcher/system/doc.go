// SPDX-License-Identifier: GPL-3.0-or-later

// Package system binds the launcher's backend interfaces to the real machine.
//
// NativeManagedBackend locates and validates a user-chosen installation
// (gamelocate, game) and plans the native CN login session (cnlogin/session).
// NativeSettingsStore persists launcher preferences (launchsettings).
//
// Only cmd/launcher should construct these; UI tests use fakes of the
// launcher.ManagedBackend / ManagedStore interfaces.
package system
