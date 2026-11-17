// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"context"
	"testing"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func TestOrdinaryEntryDoesNotStartMockClock(t *testing.T) {
	m := state.InitialNormal()
	if !m.NormalMode || state.Active(m.Flow) {
		t.Fatalf("ordinary entry is still a demo: normal=%v flow=%v", m.NormalMode, m.Flow)
	}
	s := NewShell(m)
	defer s.Dispose()
	if s.pending != nil {
		t.Fatal("ordinary entry scheduled a fake operation")
	}
}

func TestManagedCompatibilityRequiresRenewedCommunityConsent(t *testing.T) {
	s, wake := managedTestShell(state.InitialNormal())
	defer s.Dispose()
	s.ManagedStore = managedFakeStore{value: ManagedPreferences{GameRoot: `C:\owned-fixture`, RiskVersion: "loginhost-v0.9.4-community-risk-v1"}, save: make(chan ManagedPreferences, 4)}
	s.ManagedBackend = managedFakeBackend{locate: func(_ context.Context, path string) (string, error) { return path, nil }}
	s.syncManaged()
	managedUntil(t, s, wake, func() bool { return s.Model.Managed.Phase == state.ManagedReady })
	if s.Model.Managed.RiskAccepted {
		t.Fatal("compatibility build silently reused consent for the unmodified component")
	}
	s.Dispatch(state.Event{Kind: state.Primary})
	if s.Model.Overlay != state.LaunchNotice || s.Model.Managed.Phase != state.ManagedReady {
		t.Fatal("a new compatibility build bypassed the first-use notice")
	}
}
