// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"errors"
	"log"

	"github.com/nerv-base/Oddward/internal/buildinfo"
	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/launcher/state"
	"github.com/nerv-base/Oddward/internal/launcher/system"
	"github.com/nerv-base/Oddward/internal/launchsettings"
	"github.com/nerv-base/Oddward/internal/netcfg"
	"github.com/nerv-base/Oddward/internal/selfupdate"
)

// selfUpdate wires the installed build's updater. Development builds, copies
// run outside the installer layout and builds without a signing key report
// why in the version row and never touch the network for updates.
func selfUpdate(s *launcher.Shell, executable string, network *netcfg.Network, store *launchsettings.Store, saved launchsettings.Settings) {
	u := &s.Model.Update
	u.Current, u.Skipped = buildinfo.Version, saved.SkippedVersion
	layout, key, err := updateIdentity(executable, buildinfo.Version, buildinfo.UpdatePublicKey)
	if err != nil {
		u.Phase, u.Note = state.SelfUpdateUnsupported, err.Error()
		return
	}
	u.Phase = state.SelfUpdateIdle
	s.UpdateBackend = &system.SystemUpdater{
		Updater: &selfupdate.Updater{HTTP: network.Client(0), Repository: buildinfo.Repository, PublicKey: key, Layout: layout},
		Store:   store,
	}
	s.Shown = func() {
		// Reaching the first frame proves this version starts; only then is
		// it trusted and are older versions cleaned up.
		if err := selfupdate.MarkHealthy(layout.Dir); err != nil {
			log.Printf("could not mark version healthy: %v", err)
			return
		}
		if err := selfupdate.Prune(layout.Root, layout.Dir); err != nil {
			log.Printf("could not remove old versions: %v", err)
		}
	}
}

var errNoUpdateKey = errors.New("此构建未配置更新签名，不检查更新")

func updateIdentity(executable, version, publicKey string) (selfupdate.Layout, []byte, error) {
	if version == "" {
		return selfupdate.Layout{}, nil, selfupdate.ErrDevelopmentBuild
	}
	key, err := selfupdate.ParsePublicKey(publicKey)
	if err != nil {
		return selfupdate.Layout{}, nil, err
	}
	if key == nil {
		return selfupdate.Layout{}, nil, errNoUpdateKey
	}
	layout, err := selfupdate.Discover(executable, version)
	if err != nil {
		return selfupdate.Layout{}, nil, selfupdate.ErrNotInstalled
	}
	return layout, key, nil
}
