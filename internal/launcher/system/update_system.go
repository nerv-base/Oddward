// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"context"
	"errors"
	"sync"

	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/launcher/state"
	"github.com/nerv-base/Oddward/internal/launchsettings"
	"github.com/nerv-base/Oddward/internal/netcfg"
	"github.com/nerv-base/Oddward/internal/selfupdate"
)

// SystemUpdater adapts selfupdate to the launcher. Stage works on the
// manifest the latest successful Check verified, never on UI-supplied data.
type SystemUpdater struct {
	Updater *selfupdate.Updater
	Store   *launchsettings.Store

	mu       sync.Mutex
	manifest *selfupdate.Manifest
}

func (u *SystemUpdater) Check(ctx context.Context) (launcher.UpdateOffer, error) {
	m, newer, err := u.Updater.Check(ctx)
	if err != nil {
		return launcher.UpdateOffer{}, err
	}
	u.mu.Lock()
	u.manifest = &m
	u.mu.Unlock()
	return launcher.UpdateOffer{Version: m.Version, Notes: m.Notes, Size: m.Package.Size, Newer: newer}, nil
}

func (u *SystemUpdater) Stage(ctx context.Context, progress func(done, total int64)) error {
	u.mu.Lock()
	m := u.manifest
	u.mu.Unlock()
	if m == nil {
		return errors.New("尚未检查到可用的更新")
	}
	_, err := u.Updater.Stage(ctx, *m, progress)
	return err
}

func (u *SystemUpdater) Restart() error { return u.Updater.Layout.Restart() }

func (u *SystemUpdater) SaveSkipped(ctx context.Context, version string) error {
	return u.Store.Update(ctx, func(s *launchsettings.Settings) { s.SkippedVersion = version })
}

// SystemNetwork validates, persists and then applies the preference to the
// shared transport, so a setting that could not be saved never takes effect.
type SystemNetwork struct {
	Network *netcfg.Network
	Store   *launchsettings.Store
}

func (n SystemNetwork) Apply(ctx context.Context, v state.NetworkState) error {
	s := NetcfgSettings(v)
	if err := s.Validate(); err != nil {
		return err
	}
	saved := launchsettings.Network{Proxy: string(s.Proxy), ProxyURL: s.ProxyURL, Mirror: s.Mirror, MirrorPrefix: s.MirrorPrefix}
	if err := n.Store.Update(ctx, func(doc *launchsettings.Settings) { doc.Network = saved }); err != nil {
		return errors.New("网络设置未保存：" + state.BriefError(err))
	}
	return n.Network.Set(s)
}

// NetcfgSettings converts the panel state; an empty prefix means the default.
func NetcfgSettings(v state.NetworkState) netcfg.Settings {
	s := netcfg.Settings{Proxy: netcfg.System, ProxyURL: v.ProxyURL, Mirror: v.Mirror, MirrorPrefix: v.MirrorPrefix}
	switch v.Proxy {
	case state.ProxyDirect:
		s.Proxy = netcfg.Direct
	case state.ProxyCustom:
		s.Proxy = netcfg.Custom
	}
	if s.MirrorPrefix == "" {
		s.MirrorPrefix = netcfg.DefaultMirrorPrefix
	}
	return s
}

// SavedNetwork reads the persisted preference; anything unusable falls back
// to the defaults rather than blocking start-up.
func SavedNetwork(n launchsettings.Network) (netcfg.Settings, state.NetworkState) {
	v := state.NetworkState{ProxyURL: n.ProxyURL, Mirror: n.Mirror, MirrorPrefix: n.MirrorPrefix}
	switch netcfg.Mode(n.Proxy) {
	case netcfg.Direct:
		v.Proxy = state.ProxyDirect
	case netcfg.Custom:
		v.Proxy = state.ProxyCustom
	}
	s := NetcfgSettings(v)
	if s.Validate() != nil {
		s = netcfg.Default()
		v = state.NetworkState{MirrorPrefix: s.MirrorPrefix}
	}
	v.MirrorPrefix = s.MirrorPrefix
	return s, v
}
