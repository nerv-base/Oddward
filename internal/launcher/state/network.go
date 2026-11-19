// SPDX-License-Identifier: GPL-3.0-or-later

package state

// Proxy modes, in the order the network panel lists them.
const (
	ProxySystem uint8 = iota
	ProxyDirect
	ProxyCustom
)

// NetworkState is the saved network preference the launcher uses for
// updates, announcements and component downloads.
type NetworkState struct {
	Proxy        uint8
	ProxyURL     string
	Mirror       bool
	MirrorPrefix string
	Saving       bool
	Saved        bool
	Error        string
	Request      uint64
}

// LauncherSettingsSection identifies a category on the launcher settings page.
type LauncherSettingsSection uint8

const (
	SettingsGeneral LauncherSettingsSection = iota
	SettingsAppearance
	SettingsNetwork
	SettingsFiles
	SettingsAbout
)

// Valid reports whether the section exists; event indexes arrive as numbers.
func (s LauncherSettingsSection) Valid() bool { return s <= SettingsAbout }

const (
	// NetworkApply carries the edited form: Index proxy mode, Path proxy URL,
	// Enabled mirror, Message mirror prefix.
	NetworkApply EventKind = 240 + iota
	NetworkSaved
	NetworkSaveFailed
)

// SaveNetwork asks the shell to validate, apply and persist Model.Network.
const SaveNetwork EffectKind = 240

func reduceNetwork(m Model, e Event) (Model, []Effect, bool) {
	n := &m.Network
	switch e.Kind {
	case SelectLauncherSettingsSection:
		section := LauncherSettingsSection(e.Index)
		if m.Page == LauncherSettings && m.Overlay == NoOverlay && section.Valid() {
			m.SettingsSection = section
		}
	case NetworkApply:
		if m.Page != LauncherSettings || m.Overlay != NoOverlay || m.SettingsSection != SettingsNetwork || n.Saving || e.Index > ProxyCustom {
			break
		}
		n.Request++
		n.Proxy, n.ProxyURL, n.Mirror, n.MirrorPrefix = e.Index, e.Path, e.Enabled, e.Message
		n.Saving, n.Saved, n.Error = true, false, ""
		return m, []Effect{{Kind: SaveNetwork, Token: n.Request}}, true
	case NetworkSaved, NetworkSaveFailed:
		if e.Token != n.Request || !n.Saving {
			break
		}
		n.Saving = false
		if e.Kind == NetworkSaved {
			n.Saved = true
		} else {
			n.Error = e.Message
			if n.Error == "" {
				n.Error = "网络设置未保存"
			}
		}
	default:
		return m, nil, false
	}
	return m, nil, true
}
