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

// NetworkSettings is the network panel.
const NetworkSettings Overlay = 41

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
	case OpenOverlay:
		if e.Overlay != NetworkSettings {
			return m, nil, false
		}
		if m.Overlay != ConfirmQuit {
			m.Overlay, n.Saved, n.Error = NetworkSettings, false, ""
			m.Bar, m.OverlayFromMedia = MediaBarHidden, false
		}
	case NetworkApply:
		if n.Saving || e.Index > ProxyCustom {
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
