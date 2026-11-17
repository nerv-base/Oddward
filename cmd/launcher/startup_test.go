// SPDX-License-Identifier: GPL-3.0-or-later

package main

import (
	"testing"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func TestActualApplicationEntryDefaultsToManagedNotMock(t *testing.T) {
	s := newApplicationShell(false)
	defer s.Dispose()
	if !s.Model.NormalMode || s.Model.Flow == state.Checking || !s.FixedWindow {
		t.Fatalf("ordinary entry is still a fake/resizable caller: normal=%v flow=%v fixed=%v", s.Model.NormalMode, s.Model.Flow, s.FixedWindow)
	}
	if s.Model.Managed.Phase != state.ManagedLoading {
		t.Fatal("ordinary entry did not begin read-only location restore")
	}
}
func TestActualApplicationDemoRequiresExplicitOptIn(t *testing.T) {
	s := newApplicationShell(true)
	defer s.Dispose()
	if s.Model.NormalMode || s.Model.Flow != state.Checking || !s.FixedWindow {
		t.Fatal("explicit developer preview should be clearly separate and still fixed-size")
	}
}
