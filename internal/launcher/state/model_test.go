// SPDX-License-Identifier: GPL-3.0-or-later

package state

import (
	"math"
	"testing"
)

func TestPrimaryTransitions(t *testing.T) {
	for _, tc := range []struct {
		from, to Flow
		enabled  bool
	}{
		{Checking, Checking, false}, {NotInstalled, Downloading, true}, {Ready, Launching, true},
		{UpdateAvailable, Downloading, true}, {Downloading, Paused, true}, {Paused, Downloading, true},
		{Verifying, Verifying, false}, {Launching, Launching, false}, {Running, Running, false}, {Failed, Checking, true},
	} {
		t.Run(Present(settled(tc.from)).Primary, func(t *testing.T) {
			m := settled(tc.from)
			n, fx := Reduce(m, Event{Kind: Primary})
			if n.Flow != tc.to {
				t.Errorf("flow %d primary: got %d want %d", tc.from, n.Flow, tc.to)
			}
			p := Present(m)
			if p.Primary == "" || p.Status == "" || p.Enabled != tc.enabled {
				t.Errorf("incomplete projection: %+v", p)
			}
			if tc.from != tc.to && len(fx) == 0 {
				t.Error("transition must declare an effect")
			}
		})
	}
}
func TestCompleteDemoChain(t *testing.T) {
	m := settled(NotInstalled)
	m = send(m, Primary)
	m, _ = Reduce(m, Event{Kind: Progressed, Token: m.Token, Progress: .42})
	if m.Progress != .42 {
		t.Fatalf("progress=%v", m.Progress)
	}
	token := m.Token
	m = send(m, Primary)
	if m.Flow != Paused || m.Token == token {
		t.Fatal("pause must fence old work")
	}
	m = send(m, Primary)
	m, _ = Reduce(m, Event{Kind: Completed, Token: m.Token})
	if m.Flow != Verifying {
		t.Fatalf("download completion=%v", m.Flow)
	}
	m, _ = Reduce(m, Event{Kind: Completed, Token: m.Token})
	if m.Flow != Ready {
		t.Fatalf("verify completion=%v", m.Flow)
	}
	m = send(m, Primary)
	m, _ = Reduce(m, Event{Kind: Completed, Token: m.Token})
	if m.Flow != Running {
		t.Fatalf("launch completion=%v", m.Flow)
	}
}
func TestStaleEventsCannotResurrectOperation(t *testing.T) {
	m := send(settled(UpdateAvailable), Primary)
	old := m.Token
	m = send(m, Primary)
	before := m
	for _, k := range []EventKind{Completed, Progressed, OperationFailed} {
		n, fx := Reduce(m, Event{Kind: k, Token: old, Progress: 1, Message: "late"})
		if n != before || len(fx) != 0 {
			t.Fatalf("stale %v changed paused model", k)
		}
	}
	m, _ = Reduce(m, Event{Kind: OpenOverlay, Overlay: Scenarios})
	m, _ = Reduce(m, Event{Kind: SelectScenario, Flow: Ready})
	n, _ := Reduce(m, Event{Kind: Completed, Token: old})
	if n != m {
		t.Fatal("scenario reset admitted stale event")
	}
}
func TestProgressFiniteMonotonicAndBounded(t *testing.T) {
	m := settled(Downloading)
	for _, v := range []float64{.5, .2, math.NaN(), math.Inf(1), -1} {
		m, _ = Reduce(m, Event{Kind: Progressed, Token: m.Token, Progress: v})
		if m.Progress != .5 {
			t.Fatalf("input %v yielded %v", v, m.Progress)
		}
	}
	m, _ = Reduce(m, Event{Kind: Progressed, Token: m.Token, Progress: 2})
	if m.Progress != 1 {
		t.Fatal("progress must clamp")
	}
}
func TestOverlayGuardsAndQuitConfirmation(t *testing.T) {
	m := settled(Ready)
	m, _ = Reduce(m, Event{Kind: OpenOverlay, Overlay: Scenarios})
	if m.Overlay != Scenarios {
		t.Fatal("settings not opened")
	}
	if send(m, Primary) != m {
		t.Fatal("primary leaked through modal")
	}
	m = send(m, Dismiss)
	if m.Overlay != NoOverlay {
		t.Fatal("Escape did not dismiss")
	}
	m = settled(Downloading)
	m.Progress = .31
	old := m.Token
	m = send(m, RequestQuit)
	if m.Overlay != ConfirmQuit || m.Closed {
		t.Fatal("active operation must confirm")
	}
	m = send(m, Dismiss)
	if m.Progress != .31 || m.Token != old {
		t.Fatal("cancel quit mutated operation")
	}
	if send(m, ConfirmExit).Closed {
		t.Fatal("unguarded confirmation closed window")
	}
	m = send(m, RequestQuit)
	n, fx := Reduce(m, Event{Kind: ConfirmExit})
	if !n.Closed || n.Token == old || len(fx) == 0 {
		t.Fatal("confirmed exit did not cancel and close")
	}
	if send(n, Primary) != n {
		t.Fatal("closed model accepted input")
	}
	if !send(settled(Ready), RequestQuit).Closed {
		t.Fatal("idle close should be immediate")
	}
}
func TestMediaIsIndependentAndSettingsAreGuarded(t *testing.T) {
	m := settled(Downloading)
	m, _ = Reduce(m, Event{Kind: MediaReady})
	if m.Media != MediaPlaying {
		t.Fatal("media never became ready")
	}
	m = send(m, ToggleMotion)
	if m.Flow != Downloading || m.AnimateBackground() || m.Media != MediaPaused {
		t.Fatal("motion toggle crossed regions")
	}
	m = send(m, ToggleMotion)
	m, _ = Reduce(m, Event{Kind: Navigate, Page: LauncherSettings})
	m = send(m, ToggleReducedMotion)
	if m.AnimateBackground() {
		t.Fatal("reduced motion should pause")
	}
	m, _ = Reduce(m, Event{Kind: MediaFailed})
	if m.Media != MediaUnavailable || m.Flow != Downloading {
		t.Fatal("media failure broke flow")
	}
}
func TestMediaPausedCanResumeFromUserAndSystemPreferences(t *testing.T) {
	m := send(settled(Ready), MediaReady)
	m = send(m, ToggleMotion)
	if m.Media != MediaPaused || m.AnimateBackground() {
		t.Fatal("disabling motion must pause local media")
	}
	m = send(m, ToggleMotion)
	if m.Media != MediaPlaying || !m.AnimateBackground() {
		t.Fatal("paused enum must not prevent restoring enabled motion")
	}
	m, _ = Reduce(m, Event{Kind: SystemMotionChanged, Enabled: true})
	if m.Media != MediaPaused || m.AnimateBackground() {
		t.Fatal("system reduced motion must pause media")
	}
	m, _ = Reduce(m, Event{Kind: SystemMotionChanged, Enabled: false})
	if m.Media != MediaPlaying || !m.AnimateBackground() {
		t.Fatal("clearing system preference must resume user-enabled motion")
	}
	m, _ = Reduce(m, Event{Kind: Navigate, Page: LauncherSettings})
	m = send(m, ToggleReducedMotion)
	m = send(m, ToggleReducedMotion)
	if m.Media != MediaPlaying || !m.AnimateBackground() {
		t.Fatal("clearing local reduced motion must resume playback")
	}
}

func TestScenarioSelectionRequiresScenarioPanel(t *testing.T) {
	m := settled(Ready)
	n, _ := Reduce(m, Event{Kind: SelectScenario, Flow: Failed})
	if n != m {
		t.Fatal("scenario injection without panel")
	}
	m, _ = Reduce(m, Event{Kind: OpenOverlay, Overlay: Scenarios})
	n, _ = Reduce(m, Event{Kind: SelectScenario, Flow: Failed})
	if n.Flow != Failed || n.Error == "" || n.Overlay != NoOverlay || n.Token == m.Token {
		t.Fatal("scenario should reset complete operation")
	}
}
func TestOnlyActiveOperationsAcceptResults(t *testing.T) {
	for _, f := range []Flow{Ready, NotInstalled, UpdateAvailable, Paused, Running, Failed} {
		m := settled(f)
		for _, kind := range []EventKind{Completed, Progressed, OperationFailed} {
			n, _ := Reduce(m, Event{Kind: kind, Token: m.Token, Progress: .8, Message: "bad"})
			if n != m {
				t.Fatalf("idle flow %v accepted %v", f, kind)
			}
		}
	}
}
