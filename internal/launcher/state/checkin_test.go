// SPDX-License-Identifier: GPL-3.0-or-later

package state

import (
	"testing"
	"time"
)

// checkInAfternoon is a fixed local time used by the check-in tests.
func checkInAfternoon() time.Time {
	return time.Date(2026, 11, 17, 15, 30, 0, 0, time.Local)
}

func checkInDay(day int) time.Time {
	return time.Date(2026, 11, day, 15, 30, 0, 0, time.Local)
}

func linkedPayload(accountID string) CheckInPayload {
	return CheckInPayload{
		AccountID: accountID,
		RoleID:    "123",
		Linked:    true,
		Known:     [CheckInKinds]bool{true, true},
	}
}

// checkInLoaded creates a check-in page model whose account snapshot is p.
func checkInLoaded(t *testing.T, m Model, p CheckInPayload) Model {
	t.Helper()
	next, fx := Reduce(m, Event{Kind: CheckInStateRequested})
	if len(fx) != 1 || fx[0].Kind != TajiduoCheckInState {
		t.Fatalf("state request produced %v, want one TajiduoCheckInState effect", fx)
	}
	next, again := Reduce(next, Event{Kind: CheckInStateLoaded, Token: fx[0].Token, Check: p})
	if len(again) != 0 {
		t.Fatalf("state load produced effects %v, want none", again)
	}
	if !next.CheckIn.Loaded {
		t.Fatalf("check-in state not loaded: %+v", next.CheckIn)
	}
	return next
}

func checkInFixture(t *testing.T) Model {
	t.Helper()
	m := Initial()
	m.Page = CheckIn
	return checkInLoaded(t, m, linkedPayload("acc"))
}

// begin dispatches one manual write request and returns the effect it started.
func begin(t *testing.T, m Model, k CheckInKind) (Model, Effect, bool) {
	t.Helper()
	next, fx := Reduce(m, Event{Kind: CheckInBegin, Now: checkInAfternoon(), Check: CheckInPayload{Kind: k}})
	if len(fx) == 0 {
		return next, Effect{}, false
	}
	if len(fx) != 1 || fx[0].Kind != TajiduoCheckInSign || fx[0].CheckInKind != k {
		t.Fatalf("begin(%s) produced %v, want one sign effect for it", k, fx)
	}
	return next, fx[0], true
}

func signPayload(k CheckInKind, r CheckInResult, known, signed bool) CheckInPayload {
	p := CheckInPayload{Kind: k, At: [CheckInKinds]time.Time{}}
	p.Result[k] = r
	p.Known[k] = known
	p.Signed[k] = signed
	p.At[k] = checkInAfternoon()
	return p
}

func TestCheckInUnlinkedAccountOffersNoWrite(t *testing.T) {
	m := Initial()
	m.Page = CheckIn
	next, fx := Reduce(m, Event{Kind: CheckInBegin, Now: checkInAfternoon(), Check: CheckInPayload{Kind: CheckGame}})
	if len(fx) != 0 {
		t.Fatalf("unlinked account started %v, want no effect", fx)
	}
	if next.CheckIn.Linked {
		t.Fatal("unlinked account reported linked")
	}
}

func TestCheckInLoadsLinkedAccountState(t *testing.T) {
	m := checkInFixture(t)
	c := m.CheckIn
	if c.AccountID != "acc" || c.RoleID != "123" || !c.Linked || c.Loading {
		t.Fatalf("loaded state = %+v", c)
	}
	for k := 0; k < CheckInKinds; k++ {
		if c.Phase[k] != CheckInReady {
			t.Fatalf("%s phase = %v, want ready", CheckInKind(k), c.Phase[k])
		}
		if c.Signed[k] || c.Busy[k] || c.Result[k] != CheckResultNone {
			t.Fatalf("%s loaded dirty: %+v", CheckInKind(k), c)
		}
	}
}

func TestCheckInManualWritesAreIndependent(t *testing.T) {
	m := checkInFixture(t)
	m, game, started := begin(t, m, CheckGame)
	if !started {
		t.Fatal("game write did not start")
	}
	if game.Token == 0 {
		t.Fatal("game effect has no token")
	}
	if !m.CheckIn.Busy[CheckGame] || m.CheckIn.Busy[CheckCommunity] {
		t.Fatalf("busy flags = %v, want only the game task", m.CheckIn.Busy)
	}
	if m.CheckIn.Phase[CheckGame] != CheckInSigning || m.CheckIn.Phase[CheckCommunity] != CheckInReady {
		t.Fatalf("phases = %v", m.CheckIn.Phase)
	}
	if got := m.CheckIn.AttemptDay[CheckGame]; got != CheckInDay(checkInAfternoon()) {
		t.Fatalf("attempt day = %q", got)
	}
	if _, _, again := begin(t, m, CheckGame); again {
		t.Fatal("a second game write started while the first was in flight")
	}
	next, fx := Reduce(m, Event{Kind: CheckInBegin, Now: checkInAfternoon(), Check: CheckInPayload{Kind: CheckCommunity}})
	if len(fx) != 1 || fx[0].CheckInKind != CheckCommunity {
		t.Fatalf("community write produced %v", fx)
	}
	if !next.CheckIn.Busy[CheckCommunity] {
		t.Fatal("community write not marked busy")
	}
}

func TestCheckInBeginNeedsCheckInPageAndLink(t *testing.T) {
	m := checkInFixture(t)
	m.Page = Home
	if _, _, started := begin(t, m, CheckGame); started {
		t.Fatal("write started from the home page")
	}
	relinked := checkInFixture(t)
	p := linkedPayload("acc")
	p.NeedsRelink = true
	relinked = checkInLoaded(t, relinked, p)
	if !relinked.CheckIn.NeedsRelink {
		t.Fatal("needs-relink flag was dropped")
	}
	if _, _, started := begin(t, relinked, CheckGame); started {
		t.Fatal("write started while the credential needs relinking")
	}
}

func TestCheckInUnknownResultBlocksAnotherWrite(t *testing.T) {
	m := checkInFixture(t)
	m, fx, _ := begin(t, m, CheckGame)
	m, _ = Reduce(m, Event{Kind: CheckInSigned, Token: fx.Token, Check: signPayload(CheckGame, CheckResultUnverified, true, false)})
	if m.CheckIn.Phase[CheckGame] != CheckInUnknown {
		t.Fatalf("phase after unverified = %v, want unknown", m.CheckIn.Phase[CheckGame])
	}
	if m.CheckIn.Signed[CheckGame] {
		t.Fatal("an unverified write marked the task signed")
	}
	if _, _, started := begin(t, m, CheckGame); started {
		t.Fatal("write started after an unverified result")
	}
}

func TestCheckInSignedResultsMapToPhases(t *testing.T) {
	cases := []struct {
		name   string
		result CheckInResult
		known  bool
		signed bool
		phase  CheckInPhase
		done   bool
	}{
		{"success", CheckResultSuccess, true, true, CheckInDone, true},
		{"already", CheckResultAlready, true, true, CheckInDone, true},
		{"unverified", CheckResultUnverified, true, false, CheckInUnknown, false},
		{"failed", CheckResultFailed, true, false, CheckInFailed, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := checkInFixture(t)
			m, fx, _ := begin(t, m, CheckGame)
			m, _ = Reduce(m, Event{Kind: CheckInSigned, Token: fx.Token, Check: signPayload(CheckGame, tc.result, tc.known, tc.signed)})
			c := m.CheckIn
			if c.Phase[CheckGame] != tc.phase {
				t.Fatalf("phase = %v, want %v", c.Phase[CheckGame], tc.phase)
			}
			if c.Busy[CheckGame] {
				t.Fatal("lookup still busy after its answer")
			}
			if c.Signed[CheckGame] != tc.done {
				t.Fatalf("signed = %v, want %v", c.Signed[CheckGame], tc.done)
			}
			if c.Result[CheckGame] != tc.result {
				t.Fatalf("result = %v, want %v", c.Result[CheckGame], tc.result)
			}
			if c.At[CheckGame].IsZero() {
				t.Fatal("answer time was not recorded")
			}
		})
	}
}

func TestCheckInStaleSignAnswerIsDropped(t *testing.T) {
	m := checkInFixture(t)
	m, fx, _ := begin(t, m, CheckGame)
	stale := fx.Token + 1
	m, _ = Reduce(m, Event{Kind: CheckInSigned, Token: stale, Check: signPayload(CheckGame, CheckResultSuccess, true, true)})
	if m.CheckIn.Phase[CheckGame] != CheckInSigning || !m.CheckIn.Busy[CheckGame] {
		t.Fatalf("stale answer applied: %+v", m.CheckIn)
	}
}

func TestCheckInAutoTickSignsOncePerDay(t *testing.T) {
	p := linkedPayload("acc")
	p.Auto = [CheckInKinds]bool{true, true}
	m := Initial()
	m.Page = CheckIn
	m = checkInLoaded(t, m, p)

	early := time.Date(2026, 11, 17, 0, 2, 0, 0, time.Local)
	if next, fx := Reduce(m, Event{Kind: CheckInAutoTick, Now: early}); len(fx) != 0 {
		t.Fatalf("tick at 00:02 started %v, want none", fx)
	} else if next.CheckIn.Busy[CheckGame] {
		t.Fatal("tick outside the window marked a task busy")
	}

	var fx []Effect
	m, fx = Reduce(m, Event{Kind: CheckInAutoTick, Now: checkInAfternoon()})
	if len(fx) != CheckInKinds {
		t.Fatalf("first tick started %d writes, want %d", len(fx), CheckInKinds)
	}
	for _, e := range fx {
		if e.Kind != TajiduoCheckInSign {
			t.Fatalf("tick effect = %v", e)
		}
	}
	// Both writes fail; the same day must not try again.
	for _, e := range fx {
		m, _ = Reduce(m, Event{Kind: CheckInSigned, Token: e.Token, Check: signPayload(e.CheckInKind, CheckResultFailed, true, false)})
	}
	if next, again := Reduce(m, Event{Kind: CheckInAutoTick, Now: checkInAfternoon().Add(time.Hour)}); len(again) != 0 {
		t.Fatalf("same-day tick retried %v", again)
	} else if next.CheckIn.Busy[CheckGame] {
		t.Fatal("retry marked the game task busy")
	}

	next, fx := Reduce(m, Event{Kind: CheckInAutoTick, Now: checkInDay(18)})
	if len(fx) != CheckInKinds {
		t.Fatalf("next-day tick started %d writes, want %d", len(fx), CheckInKinds)
	}
	if got := next.CheckIn.AttemptDay[CheckGame]; got != "2026-11-18" {
		t.Fatalf("attempt day = %q, want the new day", got)
	}
}

func TestCheckInAutoTickSkipsCompleteAndUnverified(t *testing.T) {
	p := linkedPayload("acc")
	p.Auto = [CheckInKinds]bool{true, true}
	p.Signed = [CheckInKinds]bool{true, false}
	p.Result = [CheckInKinds]CheckInResult{CheckResultSuccess, CheckResultUnverified}
	m := Initial()
	m.Page = CheckIn
	m = checkInLoaded(t, m, p)
	if _, fx := Reduce(m, Event{Kind: CheckInAutoTick, Now: checkInAfternoon()}); len(fx) != 0 {
		t.Fatalf("tick started %v, want none", fx)
	}
}

func TestCheckInAutoToggleCommitsAndReverts(t *testing.T) {
	m := checkInFixture(t)
	m, fx := Reduce(m, Event{Kind: CheckInAutoToggle, Check: CheckInPayload{Kind: CheckGame, Enabled: true}})
	if len(fx) != 1 || fx[0].Kind != TajiduoCheckInAuto || !fx[0].Enabled || fx[0].Token == 0 {
		t.Fatalf("toggle effect = %v", fx)
	}
	if !m.CheckIn.Auto[CheckGame] {
		t.Fatal("switch did not turn on optimistically")
	}
	m, _ = Reduce(m, Event{Kind: CheckInAutoSaved, Token: fx[0].Token, Check: CheckInPayload{Kind: CheckGame, Enabled: true}})
	if !m.CheckIn.AutoSaved[CheckGame] {
		t.Fatal("saved switch was not committed")
	}

	off, fx := Reduce(m, Event{Kind: CheckInAutoToggle, Check: CheckInPayload{Kind: CheckGame}})
	if len(fx) != 1 || off.CheckIn.Auto[CheckGame] {
		t.Fatalf("second toggle = %v, state %+v", fx, off.CheckIn)
	}
	// A failed write reports the value still on disk, so the switch goes back.
	reverted, _ := Reduce(off, Event{Kind: CheckInAutoSaved, Token: fx[0].Token, Check: CheckInPayload{Kind: CheckGame, Enabled: true, Fail: CheckInFailureStorage}})
	if !reverted.CheckIn.Auto[CheckGame] {
		t.Fatal("failed switch write did not restore the saved value")
	}
	if reverted.CheckIn.AutoFailure[CheckGame] != CheckInFailureStorage {
		t.Fatalf("switch failure = %v", reverted.CheckIn.AutoFailure[CheckGame])
	}

	if stale, _ := Reduce(reverted, Event{Kind: CheckInAutoSaved, Token: fx[0].Token + 9, Check: CheckInPayload{Kind: CheckGame}}); stale.CheckIn.AutoFailure[CheckGame] != CheckInFailureStorage {
		t.Fatal("stale switch answer changed the state")
	}
}

func TestCheckInAccountSwitchClearsOldResults(t *testing.T) {
	m := checkInFixture(t)
	m, fx, _ := begin(t, m, CheckGame)
	m, _ = Reduce(m, Event{Kind: CheckInSigned, Token: fx.Token, Check: signPayload(CheckGame, CheckResultSuccess, true, true)})
	if !m.CheckIn.Signed[CheckGame] {
		t.Fatal("game task was not signed before the switch")
	}
	p := linkedPayload("other")
	p.Known = [CheckInKinds]bool{true, true}
	m = checkInLoaded(t, m, p)
	c := m.CheckIn
	if c.AccountID != "other" {
		t.Fatalf("account = %q", c.AccountID)
	}
	if c.Signed[CheckGame] || c.Result[CheckGame] != CheckResultNone || c.Phase[CheckGame] != CheckInReady {
		t.Fatalf("old account result survived: %+v", c)
	}
	// A late answer for the previous account is fenced off.
	late, _ := Reduce(m, Event{Kind: CheckInSigned, Token: fx.Token, Check: signPayload(CheckGame, CheckResultFailed, true, false)})
	if late.CheckIn.Phase[CheckGame] != CheckInReady {
		t.Fatalf("late answer for the old account applied: %+v", late.CheckIn)
	}
}

func TestCheckInUnlinkClearsTasksAndAuto(t *testing.T) {
	p := linkedPayload("acc")
	p.Auto = [CheckInKinds]bool{true, true}
	m := Initial()
	m.Page = CheckIn
	m = checkInLoaded(t, m, p)
	m, fx, _ := begin(t, m, CheckGame)

	unlinked := CheckInPayload{AccountID: "acc", Known: [CheckInKinds]bool{true, true}}
	m = checkInLoaded(t, m, unlinked)
	c := m.CheckIn
	if c.Linked {
		t.Fatal("unlinked snapshot kept the account linked")
	}
	for k := 0; k < CheckInKinds; k++ {
		if c.Phase[k] != CheckInUnlinked {
			t.Fatalf("%s phase = %v, want unlinked", CheckInKind(k), c.Phase[k])
		}
		if c.Auto[k] || c.AutoSaved[k] {
			t.Fatalf("%s auto switch survived unlink", CheckInKind(k))
		}
	}
	if c.Busy[CheckGame] {
		t.Fatal("in-flight write survived unlink")
	}
	late, _ := Reduce(m, Event{Kind: CheckInSigned, Token: fx.Token, Check: signPayload(CheckGame, CheckResultSuccess, true, true)})
	if late.CheckIn.Phase[CheckGame] != CheckInUnlinked || late.CheckIn.Signed[CheckGame] {
		t.Fatalf("late answer survived unlink: %+v", late.CheckIn)
	}
}

func TestCheckInStatusPanelIsFenced(t *testing.T) {
	m := checkInFixture(t)
	m, fx := Reduce(m, Event{Kind: CheckInStatusRequested})
	if len(fx) != 1 || fx[0].Kind != TajiduoCheckInStatus || fx[0].Token == 0 {
		t.Fatalf("status request effect = %v", fx)
	}
	if m.CheckIn.StatusPhase != CheckStatusLoading {
		t.Fatalf("status phase = %v", m.CheckIn.StatusPhase)
	}
	role := CheckInRole{Name: "漂泊者", Stamina: 240, StaminaMax: 240, FetchedAt: checkInAfternoon()}
	m, _ = Reduce(m, Event{Kind: CheckInStatusLoaded, Token: fx[0].Token, Check: CheckInPayload{Role: role}})
	if m.CheckIn.StatusPhase != CheckStatusReady || m.CheckIn.Role.Name != "漂泊者" {
		t.Fatalf("status load = %+v", m.CheckIn)
	}
	stale, _ := Reduce(m, Event{Kind: CheckInStatusLoaded, Token: fx[0].Token + 7, Check: CheckInPayload{Role: CheckInRole{Name: "别的角色"}}})
	if stale.CheckIn.Role.Name != "漂泊者" {
		t.Fatal("stale status answer replaced the panel")
	}
	next, fx := Reduce(m, Event{Kind: CheckInStatusRequested})
	next, _ = Reduce(next, Event{Kind: CheckInStatusFailed, Token: fx[0].Token, Check: CheckInPayload{Fail: CheckInFailureOffline}})
	if next.CheckIn.StatusPhase != CheckStatusFailed || next.CheckIn.StatusFailure != CheckInFailureOffline {
		t.Fatalf("status failure = %+v", next.CheckIn)
	}
	// A failed refresh keeps the last numbers on screen; only the phase and
	// reason change, so the panel can still show what was last fetched.
	if next.CheckIn.Role.Name != "漂泊者" || next.CheckIn.Role.Stamina != 240 {
		t.Fatalf("failed status read dropped the last snapshot: %+v", next.CheckIn.Role)
	}
	home := m
	home.Page = Home
	if _, fx := Reduce(home, Event{Kind: CheckInStatusRequested}); len(fx) != 1 || fx[0].Kind != TajiduoCheckInStatus {
		t.Fatalf("status read must start on the home page: %v", fx)
	}
	away := m
	away.Page = WishAnalysis
	if _, fx := Reduce(away, Event{Kind: CheckInStatusRequested}); len(fx) != 0 {
		t.Fatalf("status read started off the data pages: %v", fx)
	}
}

func TestCheckInPartialSuccessKeepsBothAnswers(t *testing.T) {
	m := checkInFixture(t)
	m, game, _ := begin(t, m, CheckGame)
	m, _ = Reduce(m, Event{Kind: CheckInSigned, Token: game.Token, Check: signPayload(CheckGame, CheckResultSuccess, true, true)})
	m, community, _ := begin(t, m, CheckCommunity)
	m, _ = Reduce(m, Event{Kind: CheckInSigned, Token: community.Token, Check: signPayload(CheckCommunity, CheckResultFailed, true, false)})
	c := m.CheckIn
	if c.Phase[CheckGame] != CheckInDone || !c.Signed[CheckGame] {
		t.Fatalf("game answer = %+v", c)
	}
	if c.Phase[CheckCommunity] != CheckInFailed || c.Signed[CheckCommunity] {
		t.Fatalf("community answer = %+v", c)
	}
	if c.Failure[CheckCommunity] != CheckInFailureNone {
		t.Fatalf("failure reason = %v", c.Failure[CheckCommunity])
	}
}

func TestCheckInFailedReadKeepsLastSnapshot(t *testing.T) {
	m := checkInFixture(t)
	requested, fx := Reduce(m, Event{Kind: CheckInStateRequested, Check: CheckInPayload{Enabled: true}})
	if len(fx) != 1 {
		t.Fatalf("refresh produced %v", fx)
	}
	failed, _ := Reduce(requested, Event{Kind: CheckInStateFailed, Token: fx[0].Token, Check: CheckInPayload{Fail: CheckInFailureOffline}})
	c := failed.CheckIn
	if c.Loading || c.LoadFailure != CheckInFailureOffline {
		t.Fatalf("failed read = %+v", c)
	}
	if c.AccountID != "acc" || !c.Linked || c.Phase[CheckGame] != CheckInReady {
		t.Fatalf("failed read dropped the last snapshot: %+v", c)
	}
	stale, _ := Reduce(requested, Event{Kind: CheckInStateFailed, Token: fx[0].Token + 5, Check: CheckInPayload{Fail: CheckInFailureAuth}})
	if stale.CheckIn.LoadFailure == CheckInFailureAuth {
		t.Fatal("stale read failure applied")
	}
}

func TestCheckInAutoWindowBounds(t *testing.T) {
	cases := []struct {
		at   time.Time
		want bool
	}{
		{time.Date(2026, 11, 17, 0, 0, 0, 0, time.Local), false},
		{time.Date(2026, 11, 17, 0, 4, 59, 0, time.Local), false},
		{time.Date(2026, 11, 17, 0, 5, 0, 0, time.Local), true},
		{time.Date(2026, 11, 17, 12, 0, 0, 0, time.Local), true},
		{time.Date(2026, 11, 17, 23, 59, 0, 0, time.Local), true},
	}
	for _, tc := range cases {
		if got := CheckInAutoWindow(tc.at); got != tc.want {
			t.Fatalf("CheckInAutoWindow(%s) = %v, want %v", tc.at, got, tc.want)
		}
	}
	if got := CheckInDay(checkInAfternoon()); got != "2026-11-17" {
		t.Fatalf("CheckInDay = %q", got)
	}
}

func TestCheckInRoleChangeClearsOldRoleState(t *testing.T) {
	m := checkInFixture(t)
	m, requested := Reduce(m, Event{Kind: CheckInStatusRequested})
	if len(requested) != 1 {
		t.Fatalf("status request produced %v", requested)
	}
	role := CheckInRole{Name: "旧角色", RoleID: "123", Level: 61, Stamina: 187, StaminaMax: 240}
	m, _ = Reduce(m, Event{Kind: CheckInStatusLoaded, Token: requested[0].Token, Check: CheckInPayload{Role: role}})
	if m.CheckIn.StatusPhase != CheckStatusReady {
		t.Fatalf("status phase = %v", m.CheckIn.StatusPhase)
	}
	m, effect, ok := begin(t, m, CheckGame)
	if !ok {
		t.Fatal("the first role could not start its game write")
	}
	m, _ = Reduce(m, Event{Kind: CheckInSigned, Token: effect.Token, Check: signPayload(CheckGame, CheckResultSuccess, true, true)})

	// The same account rebinds to another role.
	p := linkedPayload("acc")
	p.RoleID = "456"
	next := checkInLoaded(t, m, p)
	c := next.CheckIn
	if c.Role.Name != "" || c.Role.Level != 0 || c.StatusPhase != CheckStatusIdle {
		t.Fatalf("a role change must drop the previous role snapshot: %+v", c.Role)
	}
	if c.Signed[CheckGame] || c.Phase[CheckGame] == CheckInDone {
		t.Fatalf("a role change must not keep the previous role's game result: %+v", c)
	}
	if c.Phase[CheckGame] != CheckInReady {
		t.Fatalf("the new role starts unsigned, phase = %v", c.Phase[CheckGame])
	}
}

func TestCheckInUnknownNeedsConfirmationToResubmit(t *testing.T) {
	m := checkInFixture(t)
	m, effect, ok := begin(t, m, CheckGame)
	if !ok {
		t.Fatal("the first write was refused")
	}
	m, _ = Reduce(m, Event{Kind: CheckInSigned, Token: effect.Token, Check: signPayload(CheckGame, CheckResultUnverified, true, false)})
	if m.CheckIn.Phase[CheckGame] != CheckInUnknown {
		t.Fatalf("phase = %v, want unknown", m.CheckIn.Phase[CheckGame])
	}
	// Without a confirmation the same button must not send a second request.
	if _, _, ok := begin(t, m, CheckGame); ok {
		t.Fatal("an unconfirmed retry was sent after a lost answer")
	}
	m, _ = Reduce(m, Event{Kind: CheckInConfirm, Check: CheckInPayload{Kind: CheckGame, Enabled: true}})
	if !m.CheckIn.Confirm[CheckGame] {
		t.Fatal("confirmation was not armed")
	}
	next, retry, ok := begin(t, m, CheckGame)
	if !ok || retry.Kind != TajiduoCheckInSign || !retry.Confirmed {
		t.Fatalf("a confirmed retry must be sent and marked, got %+v", retry)
	}
	if next.CheckIn.Confirm[CheckGame] {
		t.Fatal("one confirmation must arm exactly one retry")
	}
	// An automatic attempt never claims the confirmation.
	auto, effects := Reduce(m, Event{Kind: CheckInAutoTick, Now: checkInAfternoon()})
	for _, f := range effects {
		if f.Confirmed {
			t.Fatal("an automatic attempt must never claim the confirmation")
		}
	}
	if len(effects) != 0 {
		t.Fatalf("a task whose result is already recorded must not be repeated: %v", effects)
	}
	if !auto.CheckIn.Confirm[CheckGame] {
		t.Fatal("the confirmation must still be waiting for the user")
	}
}

func TestCheckInLaunchIsTriggeredByStartingTheGame(t *testing.T) {
	m := InitialNormal()
	m.Managed.Phase, m.Managed.Root, m.Managed.RiskAccepted = ManagedReady, `D:\SyntheticGame`, true
	next, fx := Reduce(m, Event{Kind: Primary})
	if len(fx) != 2 || fx[0].Kind != PrepareManagedLogin || fx[1].Kind != TajiduoCheckInLaunch {
		t.Fatalf("starting the game must ask for one check-in attempt, got %v", fx)
	}
	if next.Managed.Phase != ManagedPreparing || next.Page != Home {
		t.Fatalf("the launch flow must keep its own transition: %+v", next.Managed.Phase)
	}
	// Opening the page or letting time pass is not a trigger.
	page := m
	page.Page = CheckIn
	if _, fx := Reduce(page, Event{Kind: CheckInAutoTick, Now: checkInAfternoon()}); len(fx) != 0 {
		t.Fatalf("a page visit must not sign anything: %v", fx)
	}
	// A blocked launch does not reach the trigger.
	blocked := m
	blocked.Managed.Phase = ManagedRunning
	if _, fx := Reduce(blocked, Event{Kind: Primary}); len(fx) != 0 {
		t.Fatalf("a blocked launch must not fire the trigger: %v", fx)
	}
}

func TestCheckInStatusFailureKeepsTheLastSnapshot(t *testing.T) {
	m := checkInFixture(t)
	m, first := Reduce(m, Event{Kind: CheckInStatusRequested})
	if len(first) != 1 {
		t.Fatalf("status request produced %v", first)
	}
	role := CheckInRole{Name: "合成测试角色", Stamina: 187, StaminaMax: 240}
	m, _ = Reduce(m, Event{Kind: CheckInStatusLoaded, Token: first[0].Token, Check: CheckInPayload{Role: role}})
	m, second := Reduce(m, Event{Kind: CheckInStatusRequested})
	if len(second) != 1 {
		t.Fatalf("second status request produced %v", second)
	}
	next, _ := Reduce(m, Event{Kind: CheckInStatusFailed, Token: second[0].Token, Check: CheckInPayload{Fail: CheckInFailureOffline}})
	c := next.CheckIn
	if c.StatusPhase != CheckStatusFailed || c.StatusFailure != CheckInFailureOffline {
		t.Fatalf("a failed read must be reported: %+v", c)
	}
	if c.Role.Name != "合成测试角色" || c.Role.Stamina != 187 || c.Role.StaminaMax != 240 {
		t.Fatalf("a failed read must keep the last snapshot: %+v", c.Role)
	}
}
