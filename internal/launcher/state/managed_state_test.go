// SPDX-License-Identifier: GPL-3.0-or-later

package state

import "testing"

func hasManagedEffect(fx []Effect, kind EffectKind) bool {
	for _, f := range fx {
		if f.Kind == kind {
			return true
		}
	}
	return false
}

func TestNormalPrimaryRoutesToLocateRatherThanFakeLaunch(t *testing.T) {
	m := normalFixture(ManagedUnlocated)
	m.Managed.Root = ""
	if p := Present(m); p.Primary != "定位游戏" || !p.Enabled {
		t.Fatalf("unlocated action = %+v", p)
	}
	next, fx := Reduce(m, Event{Kind: Primary})
	if next.Overlay != LocateGame || next.Flow != m.Flow || hasManagedEffect(fx, Simulate) {
		t.Fatalf("missing game still fake-launches: %+v fx=%+v", next, fx)
	}
}

func TestNormalFirstLaunchRequiresRiskReview(t *testing.T) {
	m := normalFixture(ManagedReady)
	next, fx := Reduce(m, Event{Kind: Primary})
	if next.Overlay != LaunchNotice || len(fx) != 0 {
		t.Fatalf("first click must review managed login risk, got overlay=%v fx=%+v", next.Overlay, fx)
	}
	next, fx = Reduce(next, Event{Kind: ManagedAcceptLaunch})
	if !next.Managed.RiskAccepted || next.Managed.Phase != ManagedPreparing || next.Overlay != NoOverlay || !hasManagedEffect(fx, PrepareManagedLogin) || hasManagedEffect(fx, Simulate) {
		t.Fatalf("accepted launch did not prepare internal capability: %+v %+v", next.Managed, fx)
	}
}

func TestNormalPreparedComponentActuallyRequestsHostStart(t *testing.T) {
	m := normalFixture(ManagedPreparing)
	m.Managed.RiskAccepted = true
	next, fx := Reduce(m, Event{Kind: ManagedPrepared, Token: 8})
	if next.Managed.Phase != ManagedStarting || !hasManagedEffect(fx, LaunchManagedHost) {
		t.Fatalf("prepared component not connected to real Start: %+v %+v", next.Managed, fx)
	}
	for _, event := range []Event{{Kind: ManagedHostStarted, Token: next.Managed.Request}, {Kind: ManagedHostExited, Token: next.Managed.Request}} {
		next, _ = Reduce(next, event)
		if next.Flow == Running {
			t.Fatal("host observation manufactured game Running")
		}
	}
}

func TestNormalUnknownHostKeepsInstallationLocked(t *testing.T) {
	for _, phase := range []ManagedPhase{ManagedStarting, ManagedRunning} {
		m := normalFixture(phase)
		next, _ := Reduce(m, Event{Kind: ManagedHostUnobserved, Token: 8, Message: "observation failed"})
		if next.Managed.Phase != ManagedUnobserved {
			t.Fatalf("phase %v lost unknown-host lock: %+v", phase, next.Managed)
		}
		for _, e := range []Event{{Kind: Primary}, {Kind: ManagedBrowse}, {Kind: ManagedSelectPath, Path: `C:\other`}, {Kind: ManagedAcceptLaunch}} {
			got, fx := Reduce(next, e)
			if got.Managed != next.Managed || len(fx) > 0 {
				t.Fatalf("unknown host unlocked by %v: %+v %+v", e.Kind, got.Managed, fx)
			}
		}
	}
}

func TestNormalRepeatedClicksAndStaleResultsNeverDuplicateStart(t *testing.T) {
	for _, phase := range []ManagedPhase{ManagedPreparing, ManagedStarting, ManagedRunning} {
		m := normalFixture(phase)
		m.Managed.RiskAccepted = true
		for i := 0; i < 20; i++ {
			next, fx := Reduce(m, Event{Kind: Primary})
			if next != m || len(fx) != 0 {
				t.Fatalf("phase %v repeated primary changed state/effects", phase)
			}
		}
		next, fx := Reduce(m, Event{Kind: ManagedPrepared, Token: 7})
		if next != m || len(fx) != 0 {
			t.Fatal("stale preparation affected current state")
		}
	}
}

func TestNormalPathSelectionRequiresSuccessfulInspection(t *testing.T) {
	m := normalFixture(ManagedUnlocated)
	m.Managed.Root = ""
	m.Overlay = LocateGame
	next, fx := Reduce(m, Event{Kind: ManagedSelectPath, Path: `C:\candidate`})
	if next.Managed.Phase != ManagedLocating || next.Managed.Root != "" || !hasManagedEffect(fx, InspectManagedDirectory) {
		t.Fatalf("selection not inspected: %+v %+v", next.Managed, fx)
	}
	next, fx = Reduce(next, Event{Kind: ManagedLocated, Token: next.Managed.Request, Path: `C:\canonical`})
	if next.Managed.Root != `C:\canonical` || next.Managed.Phase != ManagedReady || !hasManagedEffect(fx, SaveManagedSettings) || next.Overlay != NoOverlay {
		t.Fatalf("inspection not adopted/saved: %+v %+v", next, fx)
	}
}

func TestNormalCancelInvalidatesPreparation(t *testing.T) {
	m := normalFixture(ManagedPreparing)
	next, fx := Reduce(m, Event{Kind: ManagedCancel})
	if next.Managed.Request <= m.Managed.Request || next.Managed.Phase != ManagedReady || !hasManagedEffect(fx, CancelManagedWork) {
		t.Fatalf("cancel did not invalidate preparation: %+v %+v", next.Managed, fx)
	}
	got, fx := Reduce(next, Event{Kind: ManagedPrepared, Token: m.Managed.Request})
	if got != next || len(fx) != 0 {
		t.Fatal("cancelled preparation launched later")
	}
}

func TestNormalModalAndWrongPageCannotStart(t *testing.T) {
	for _, overlay := range []Overlay{Settings, NewsDetails, LocateGame, About} {
		m := normalFixture(ManagedReady)
		m.Managed.RiskAccepted = true
		m.Overlay = overlay
		next, fx := Reduce(m, Event{Kind: Primary})
		if next != m || len(fx) > 0 {
			t.Fatalf("modal %v allowed primary", overlay)
		}
	}
	m := normalFixture(ManagedReady)
	m.Page = WishAnalysis
	m.Managed.RiskAccepted = true
	if next, fx := Reduce(m, Event{Kind: Primary}); next != m || len(fx) > 0 {
		t.Fatal("off-home primary started host")
	}
}

func TestDemoPreviewIsExplicitAndCannotStartManagedHost(t *testing.T) {
	m := normalFixture(ManagedReady)
	m.Overlay = Scenarios
	next, fx := Reduce(m, Event{Kind: SelectScenario, Flow: Ready})
	if next.NormalMode || hasManagedEffect(fx, LaunchManagedHost) || hasManagedEffect(fx, PrepareManagedLogin) {
		t.Fatal("state preview did not explicitly switch to demo")
	}
	next, fx = Reduce(next, Event{Kind: ManagedResume})
	if !next.NormalMode || hasManagedEffect(fx, LaunchManagedHost) {
		t.Fatal("return to normal mode automatically started host")
	}
}

func TestNormalPreparingProgressCannotInventBytes(t *testing.T) {
	m := normalFixture(ManagedPreparing)
	next, _ := Reduce(m, Event{Kind: ManagedProgress, Token: 8, Downloaded: 42, Total: 100, Message: "准备本地登录窗口"})
	if next.Managed.Downloaded != 42 || next.Managed.Total != 100 || next.Progress != .42 {
		t.Fatalf("progress not mapped from actual transfer: %+v", next.Managed)
	}
	next2, _ := Reduce(next, Event{Kind: ManagedProgress, Token: 7, Downloaded: 99, Total: 100})
	if next2 != next {
		t.Fatal("stale progress accepted")
	}
}

func TestNormalChangedRootNeedsFreshLaunchConsent(t *testing.T) {
	for _, tt := range []struct {
		name, selected string
		wantConsent    bool
	}{
		{"same-root", `C:\fixture\nte`, true},
		{"case-and-clean-alias", `c:\FIXTURE\nte\.`, true},
		{"different-root", `C:\fixture\other`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := normalFixture(ManagedReady)
			m.Managed.RiskAccepted = true
			m.Overlay = LocateGame
			m, _ = Reduce(m, Event{Kind: ManagedSelectPath, Path: tt.selected})
			m, fx := Reduce(m, Event{Kind: ManagedLocated, Token: m.Managed.Request, Path: tt.selected})
			if m.Managed.RiskAccepted != tt.wantConsent || m.Managed.Root != tt.selected || !hasManagedEffect(fx, SaveManagedSettings) {
				t.Fatalf("adopted root did not bind consent: %+v effects=%+v", m.Managed, fx)
			}
			if !tt.wantConsent {
				next, fx := Reduce(m, Event{Kind: Primary})
				if next.Overlay != LaunchNotice || len(fx) != 0 {
					t.Fatal("new root bypassed the required fresh risk review")
				}
			}
		})
	}
}

func TestNormalRestoredConsentRequiresMatchingInspectedRoot(t *testing.T) {
	for _, tt := range []struct {
		name, inspected string
		accepted, want  bool
	}{
		{"same-saved-root", `C:\saved`, true, true},
		{"normalized-saved-root", `c:\SAVED\.`, true, true},
		{"different-inspected-root", `C:\different`, true, false},
		{"no-version-consent", `C:\saved`, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			m := InitialNormal()
			m, _ = Reduce(m, Event{Kind: ManagedLoad})
			m, _ = Reduce(m, Event{Kind: ManagedLoaded, Token: m.Managed.Request, Path: `C:\saved`, Enabled: tt.accepted})
			if m.Managed.Root != "" || m.Managed.Phase != ManagedLocating {
				t.Fatal("saved directory must not be adopted before inspection")
			}
			m, _ = Reduce(m, Event{Kind: ManagedLocated, Token: m.Managed.Request, Path: tt.inspected})
			if m.Managed.RiskAccepted != tt.want {
				t.Fatalf("restored consent mismatched inspected root: %+v want=%v", m.Managed, tt.want)
			}
		})
	}
	m := InitialNormal()
	m, _ = Reduce(m, Event{Kind: ManagedLoad})
	m, _ = Reduce(m, Event{Kind: ManagedLoaded, Token: m.Managed.Request, Enabled: true})
	if m.Managed.RiskAccepted {
		t.Fatal("a saved consent without a saved root cannot authorize a later selection")
	}
}

func TestNormalCancelledRequestRejectsLatePrepareFailure(t *testing.T) {
	m := normalFixture(ManagedPreparing)
	before := m.Managed.Request
	m, _ = Reduce(m, Event{Kind: ManagedCancel})
	got, fx := Reduce(m, Event{Kind: ManagedPrepareFailed, Token: before, Message: "late worker deadline"})
	if got != m || len(fx) != 0 {
		t.Fatal("cancelled worker failure changed the current request")
	}
}
