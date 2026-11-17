// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package netcfg

import "golang.org/x/sys/windows/registry"

// ReadSystemProxy reads the current user's static proxy from Internet
// Settings. PAC scripts (AutoConfigURL) and WPAD are not evaluated; a missing
// key reads as no proxy.
func ReadSystemProxy() SystemProxy {
	k, err := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Internet Settings`, registry.QUERY_VALUE)
	if err != nil {
		return SystemProxy{}
	}
	defer k.Close()
	enabled, _, err := k.GetIntegerValue("ProxyEnable")
	if err != nil || enabled == 0 {
		return SystemProxy{}
	}
	server, _, _ := k.GetStringValue("ProxyServer")
	override, _, _ := k.GetStringValue("ProxyOverride")
	return SystemProxy{Enabled: true, Server: server, Override: override}
}
