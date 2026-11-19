// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func TestHomeStaminaCardOnlyWithLinkedRole(t *testing.T) {
	s := NewShell(normalFixture(state.ManagedReady))
	tt := ui.NewTester(s.View, 1280, 720)
	if tt.HasText("本性像素") {
		t.Fatal("home shows stamina before a role snapshot exists")
	}
	s.Dispose()

	m := normalFixture(state.ManagedReady)
	m.CheckIn.Linked = true
	m.CheckIn.Loaded = true
	m.CheckIn.Role = state.CheckInRole{
		RoleID: "218020980063", Name: "小小绫波丽", Level: 57, WorldLevel: 6,
		Stamina: 320, StaminaMax: 320,
		CityStamina: 92, CityStaminaMax: 120,
		DayValue: 100, WeekRemain: 2, CharCount: 23,
		FetchedAt: time.Now(),
	}
	s = NewShell(m)
	defer s.Dispose()
	tt = ui.NewTester(s.View, 1280, 720)
	card, ok := tt.Find("本性像素 320/320，已满")
	if !ok {
		t.Fatal("home stamina card is missing")
	}
	if card.X < 800 || card.Y < 60 || card.Y+card.H > 320 ||
		card.X+card.W > 1280-dockRight {
		t.Fatalf("home stamina card leaves its safe corner: %+v", card)
	}
	for _, text := range []string{"都市活力", "今日活跃度", "周本剩余", "刷新体力", "鉴别等级"} {
		if !tt.HasText(text) {
			t.Fatalf("home stamina card is missing %q", text)
		}
	}
	if tt.HasText("轨外之境") {
		t.Fatal("home stamina card still shows the unsupported track progress")
	}
	if err := tt.Click("刷新体力"); err != nil {
		t.Fatalf("home stamina refresh is not clickable: %v", err)
	}
	if s.Model.CheckIn.StatusPhase == state.CheckStatusIdle {
		t.Fatal("home stamina refresh did not request a new status read")
	}

	m.CheckIn.NeedsRelink = true
	s = NewShell(m)
	defer s.Dispose()
	tt = ui.NewTester(s.View, 1280, 720)
	if tt.HasText("本性像素") {
		t.Fatal("home shows a stale stamina card while the grant needs relinking")
	}
}
