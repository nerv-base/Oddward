// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func TestAboutShowsVersionAndCredits(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	s.Model.Page, s.Model.SettingsSection = state.LauncherSettings, state.SettingsAbout
	tt := ui.NewTester(s.View, 1024, 631)
	for _, label := range []string{"《异环》非官方启动器", "当前版本", "GPL-3.0-or-later", "游戏素材及塔吉多物品图标的版权归各自权利人所有。"} {
		if !tt.HasText(label) {
			t.Errorf("About is missing %q", label)
		}
	}
}
