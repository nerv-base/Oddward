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
	m.CheckIn.Role = state.CheckInRole{Stamina: 320, StaminaMax: 320, FetchedAt: time.Now()}
	s = NewShell(m)
	defer s.Dispose()
	tt = ui.NewTester(s.View, 1280, 720)
	card, ok := tt.Find("本性像素 320/320，已满")
	if !ok {
		t.Fatal("home stamina card is missing")
	}
	if card.X < 800 || card.Y < 70 || card.Y+card.H > 160 ||
		card.X+card.W > 1280-dockRight {
		t.Fatalf("home stamina card leaves its safe corner: %+v", card)
	}
}
