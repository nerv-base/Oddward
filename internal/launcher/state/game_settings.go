// SPDX-License-Identifier: GPL-3.0-or-later

package state

// GameSettingsSection identifies a tab in the home game's settings dialog.
type GameSettingsSection uint8

const (
	GameSettingsBasic GameSettingsSection = iota
	GameSettingsDownloads
)

func (s GameSettingsSection) Label() string {
	switch s {
	case GameSettingsDownloads:
		return "下载设置"
	default:
		return "基本信息"
	}
}

// GameSettingsVisible also covers the directory picker opened inside the dialog.
func (m Model) GameSettingsVisible() bool {
	return m.Overlay == GameSettings || m.Overlay == LocateGame && m.locateFromSettings
}
