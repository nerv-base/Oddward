// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func TestGameDataPageShowsRoleHomeDetails(t *testing.T) {
	m := normalFixture(state.ManagedReady)
	m.CheckIn = state.CheckInState{
		AccountID: "acct", RoleID: "218020980063", Loaded: true, Linked: true,
		StatusPhase: state.CheckStatusReady,
		Role: state.CheckInRole{
			RoleID: "218020980063", Name: "小小绫波丽", Server: "天空之城",
			Level: 61, WorldLevel: 12, Stamina: 187, StaminaMax: 240,
			CityStamina: 92, CityStaminaMax: 120, DayValue: 100, WeekRemain: 2,
			LoginDays: 128, CharCount: 24, Achievement: 512, AchievementTotal: 900,
			FetchedAt: time.Now(),
			Details: &state.CheckInDetails{
				Areas: []state.CheckInArea{{ID: "area-1", Name: "新港", Progress: 12, Total: 30}},
				Characters: []state.CheckInCharacter{{
					ID: "1019", Name: "合成角色", ElementType: "CHARACTER_ELEMENT_TYPE_PSYCHE",
					Quality: "ITEM_QUALITY_ORANGE", Level: 40, MixLevel: 3, Likeability: 7, Awaken: 2,
				}},
				RealEstate: state.CheckInCollection{Own: 3, Total: 9, ShowName: "小屋"},
				Vehicle:    state.CheckInCollection{Own: 2, Total: 5, ShowName: "载具"},
			},
		},
	}
	s := NewShell(m)
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1280, 720)
	if err := tt.Click("游戏数据"); err != nil || s.Model.Page != state.GameData {
		t.Fatalf("game data page unreachable: %v page=%v", err, s.Model.Page)
	}
	tt.Frame()
	for _, want := range []string{"游戏数据", "角色概览", "角色收集", "区域探索", "房产与载具", "合成角色", "新港", "小屋", "载具"} {
		if !tt.HasText(want) {
			t.Fatalf("game data page missing %q", want)
		}
	}
}
