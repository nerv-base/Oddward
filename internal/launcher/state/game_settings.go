// SPDX-License-Identifier: GPL-3.0-or-later

package state

// GameSettingsVisible also covers the directory picker opened inside the dialog.
func (m Model) GameSettingsVisible() bool {
	return m.Overlay == GameSettings || m.Overlay == LocateGame && m.locateFromSettings
}
