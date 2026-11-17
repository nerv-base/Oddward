// SPDX-License-Identifier: GPL-3.0-or-later

package state

import "testing"

func TestNetworkPanelSavesThroughTheShell(t *testing.T) {
	m := InitialNormal()
	m, _ = Reduce(m, Event{Kind: OpenOverlay, Overlay: NetworkSettings})
	if m.Overlay != NetworkSettings {
		t.Fatal("panel did not open")
	}
	m, fx := Reduce(m, Event{Kind: NetworkApply, Index: ProxyCustom, Path: "socks5://127.0.0.1:7890", Enabled: true, Message: "https://gh-proxy.com/"})
	n := m.Network
	if !n.Saving || n.Proxy != ProxyCustom || n.ProxyURL != "socks5://127.0.0.1:7890" || !n.Mirror || n.MirrorPrefix != "https://gh-proxy.com/" ||
		len(fx) != 1 || fx[0].Kind != SaveNetwork || fx[0].Token != n.Request {
		t.Fatalf("apply: %+v %+v", n, fx)
	}
	if _, fx := Reduce(m, Event{Kind: NetworkApply}); len(fx) != 0 {
		t.Fatal("second save while saving")
	}
	if stale, _ := Reduce(m, Event{Kind: NetworkSaved, Token: n.Request - 1}); !stale.Network.Saving {
		t.Fatal("stale result applied")
	}
	failed, _ := Reduce(m, Event{Kind: NetworkSaveFailed, Token: n.Request, Message: "代理地址无效"})
	if failed.Network.Saving || failed.Network.Error != "代理地址无效" {
		t.Fatalf("failed: %+v", failed.Network)
	}
	saved, _ := Reduce(m, Event{Kind: NetworkSaved, Token: n.Request})
	if saved.Network.Saving || !saved.Network.Saved || saved.Overlay != NetworkSettings {
		t.Fatalf("saved: %+v", saved.Network)
	}
	if bad, fx := Reduce(saved, Event{Kind: NetworkApply, Index: 9}); len(fx) != 0 || bad.Network.Proxy != ProxyCustom {
		t.Fatal("accepted an unknown proxy mode")
	}
}
