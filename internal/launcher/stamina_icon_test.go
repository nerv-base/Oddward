// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"testing"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func TestBundledStaminaIconsDecode(t *testing.T) {
	s := NewShell(state.Model{})
	if s.StaminaIcon == nil {
		t.Fatal("bundled stamina icon did not decode")
	}
	if s.CityStaminaIcon == nil {
		t.Fatal("bundled city stamina icon did not decode")
	}
}
