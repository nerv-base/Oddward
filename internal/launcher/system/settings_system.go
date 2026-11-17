// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"context"

	"github.com/nerv-base/Oddward/internal/launcher"
	"github.com/nerv-base/Oddward/internal/launchsettings"
)

// NativeSettingsStore persists the managed game choice and consent revision in
// the launcher settings file.
type NativeSettingsStore struct {
	Store *launchsettings.Store
}

func (s NativeSettingsStore) Load(ctx context.Context) (launcher.ManagedPreferences, error) {
	settings, err := s.Store.Load(ctx)
	if err != nil {
		return launcher.ManagedPreferences{}, err
	}
	return launcher.ManagedPreferences{GameRoot: settings.GameRoot, RiskVersion: settings.RiskVersion}, nil
}

func (s NativeSettingsStore) Save(ctx context.Context, preferences launcher.ManagedPreferences) error {
	return s.Store.Update(ctx, func(settings *launchsettings.Settings) {
		settings.GameRoot = preferences.GameRoot
		settings.RiskVersion = preferences.RiskVersion
	})
}
