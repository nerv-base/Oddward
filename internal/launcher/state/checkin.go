// SPDX-License-Identifier: GPL-3.0-or-later

package state

import "time"

// CheckInKind identifies one of the two independent daily tasks. They never
// share an outcome: a game success is not a community success.
type CheckInKind uint8

const (
	CheckGame CheckInKind = iota
	CheckCommunity
	// CheckInKinds is the fixed number of tasks; arrays in the model use it.
	CheckInKinds = 2
)

func (k CheckInKind) String() string {
	if k == CheckCommunity {
		return "community"
	}
	return "game"
}

// Valid reports whether k addresses a real task.
func (k CheckInKind) Valid() bool { return k < CheckInKinds }

// CheckInPhase is the per-task phase shown on the check-in page.
type CheckInPhase uint8

const (
	// CheckInUnlinked means the saved account is not linked to Tajiduo.
	CheckInUnlinked CheckInPhase = iota
	// CheckInReady means the server state was read and today is still open.
	CheckInReady
	// CheckInSigning means a write is in flight; the result is unknown.
	CheckInSigning
	// CheckInDone means today's task is complete (fresh or already signed).
	CheckInDone
	// CheckInFailed means the write definitely did not happen.
	CheckInFailed
	// CheckInUnknown means a write may have happened. It is never retried
	// automatically; the server is asked first.
	CheckInUnknown
)

// CheckInResult is what the launcher recorded for one task on one day.
type CheckInResult uint8

const (
	CheckResultNone CheckInResult = iota
	CheckResultSuccess
	CheckResultAlready
	CheckResultUnverified
	CheckResultFailed
)

// CheckInFailure is a coarse reason the page can explain without upstream text.
type CheckInFailure uint8

const (
	CheckInFailureNone CheckInFailure = iota
	CheckInFailureOffline
	CheckInFailureAuth
	CheckInFailureChallenge
	CheckInFailureProtocol
	CheckInFailureMismatch
	CheckInFailureNoRole
	CheckInFailureStorage
	CheckInFailureBusy
)

// CheckInStatusPhase is the game status panel's own loading state.
type CheckInStatusPhase uint8

const (
	CheckStatusIdle CheckInStatusPhase = iota
	CheckStatusLoading
	CheckStatusReady
	CheckStatusFailed
)

// CheckInArea is one exploration area the role-home endpoint reports.
type CheckInArea struct {
	ID, Name        string
	Progress, Total int
}

// CheckInCharacter is the summary the role-home endpoint reports for one
// owned character. The raw quality and element codes stay as the server sent
// them; the page translates them for display.
type CheckInCharacter struct {
	ID, Name, ElementType, Quality string
	Level, MixLevel, Likeability   int
	Awaken                         int
}

// CheckInCollection is one ownership summary (real estate or vehicles).
type CheckInCollection struct {
	Own, Total int
	ShowName   string
}

// CheckInDetails is the optional game-data payload. It lives behind a pointer
// so Model stays comparable, and it is replaced whole on every read: the page
// never sees a half-updated list.
type CheckInDetails struct {
	Areas      []CheckInArea
	Characters []CheckInCharacter
	RealEstate CheckInCollection
	Vehicle    CheckInCollection
}

// CheckInRole is the role summary the status panel shows. Values come from
// the server; a zero cap means the server did not report that figure.
type CheckInRole struct {
	RoleID, Name, Server          string
	Level, WorldLevel             int
	Stamina, StaminaMax           int
	CityStamina, CityStaminaMax   int
	DayValue, WeekRemain          int
	LoginDays, CharCount          int
	Achievement, AchievementTotal int
	FetchedAt                     time.Time
	// Details carries the richer role-home payload used by the game-data page.
	Details *CheckInDetails
}

// CheckInState is everything the check-in page and the auto scheduler need.
// It contains no I/O: the shell performs every effect.
type CheckInState struct {
	AccountID string
	RoleID    string
	Linked    bool
	// NeedsRelink means the stored credential no longer works and the user
	// must link again; signing must not be attempted.
	NeedsRelink bool
	// Loading is the account-level state read; LoadFailure explains a failure.
	Loading     bool
	Loaded      bool
	LoadFailure CheckInFailure
	// LoadToken fences account-level reads.
	LoadToken uint64
	// Day is the local calendar day the per-day fields describe. A tick on a
	// later day rolls them over, so yesterday's success never blocks today.
	Day string

	Auto [CheckInKinds]bool
	// AutoSaved mirrors the persisted switches; Auto is the requested value.
	AutoSaved [CheckInKinds]bool
	// AutoFailure explains a switch that could not be saved.
	AutoFailure [CheckInKinds]CheckInFailure
	// AutoToken fences one switch write.
	AutoToken [CheckInKinds]uint64
	Known     [CheckInKinds]bool
	// Signed is the server's answer that today is already complete.
	Signed [CheckInKinds]bool
	// Busy is true while a write for that task is in flight.
	Busy   [CheckInKinds]bool
	Phase  [CheckInKinds]CheckInPhase
	Result [CheckInKinds]CheckInResult
	Detail [CheckInKinds]string
	// Failure explains the last failed attempt of that task.
	Failure [CheckInKinds]CheckInFailure
	At      [CheckInKinds]time.Time
	// AttemptDay is the local day of the last attempt started in this process.
	// It stops an automatic task from retrying after a failure or an unknown
	// result on the same day.
	AttemptDay [CheckInKinds]string
	// Confirm arms a manual retry of a task whose last write left no answer.
	// Without it the write is refused, so an unknown result is never repeated
	// by a stray click.
	Confirm [CheckInKinds]bool
	// Token fences one task's write result.
	Token [CheckInKinds]uint64

	StatusPhase   CheckInStatusPhase
	StatusFailure CheckInFailure
	Role          CheckInRole
	// StatusToken fences the status read.
	StatusToken uint64
}

// CheckInPayload carries the facts a shell response reports. It is a plain
// value so Model stays comparable.
type CheckInPayload struct {
	AccountID, RoleID   string
	Linked, NeedsRelink bool
	Auto, Known, Signed [CheckInKinds]bool
	Result              [CheckInKinds]CheckInResult
	Detail              [CheckInKinds]string
	Failure             [CheckInKinds]CheckInFailure
	At                  [CheckInKinds]time.Time
	// Day is the local day a snapshot describes.
	Day string
	// Kind addresses the task of a sign event.
	Kind CheckInKind
	// Enabled is the requested automatic switch value, or the refresh request
	// flag of a state load.
	Enabled bool
	// Fail is the single failure of an operation that has no per-task row,
	// such as a status read or an automatic-switch write.
	Fail CheckInFailure
	// Role is the status panel payload.
	Role CheckInRole
}

// CheckInAutoWindow reports whether the local clock has left the first minutes
// after midnight, when the server may still be closing the previous day. The
// server's own "today" answer stays authoritative; this is only a gate.
func CheckInAutoWindow(now time.Time) bool {
	local := now.In(time.Local)
	return local.Hour() != 0 || local.Minute() >= 5
}

// CheckInDay is the local calendar day the ledger uses.
func CheckInDay(now time.Time) string {
	return now.In(time.Local).Format("2006-01-02")
}

// Reset clears everything that belongs to one account. Account switches call
// it so a stale result can never describe the new account.
func (s CheckInState) Reset(accountID string) CheckInState {
	var next CheckInState
	next.AccountID = accountID
	next.Loading = true
	for k := 0; k < CheckInKinds; k++ {
		next.Phase[k] = CheckInUnlinked
		next.Token[k] = s.Token[k] + 1
	}
	next.LoadToken = s.LoadToken + 1
	next.StatusToken = s.StatusToken + 1
	next.StatusPhase = CheckStatusIdle
	return next
}

// rollDay clears the per-day answers when the local day moved past the one the
// state describes. In-flight writes are fenced: their answer belongs to the
// previous day, where the ledger already holds it.
func (s CheckInState) rollDay(day string) CheckInState {
	for k := 0; k < CheckInKinds; k++ {
		s.Token[k]++
		s.Busy[k] = false
		s.Result[k] = CheckResultNone
		s.Detail[k] = ""
		s.Failure[k] = CheckInFailureNone
		s.At[k] = time.Time{}
		s.Signed[k] = false
		s.AttemptDay[k] = ""
		if s.Linked && !s.NeedsRelink {
			s.Phase[k] = CheckInReady
		} else {
			s.Phase[k] = CheckInUnlinked
		}
	}
	s.LoadToken++
	s.Day = day
	return s
}

// startedReport marks one task as in flight and returns the effect that runs
// the write. The caller has already checked the preconditions.
func startedReport(s CheckInState, k CheckInKind, now time.Time) (CheckInState, Effect) {
	s.Token[k]++
	s.Busy[k] = true
	s.Phase[k] = CheckInSigning
	s.Result[k] = CheckResultNone
	s.Detail[k] = ""
	s.Failure[k] = CheckInFailureNone
	s.AttemptDay[k] = CheckInDay(now)
	// One confirmation arms one retry; a second click needs a second one.
	s.Confirm[k] = false
	return s, Effect{Kind: TajiduoCheckInSign, CheckInKind: k, Token: s.Token[k]}
}

// ReduceCheckIn owns the check-in page and the auto scheduler. It performs no
// I/O: every network or file action becomes an Effect for the shell.
func ReduceCheckIn(m Model, e Event) (Model, []Effect, bool) {
	if m.Closed {
		return m, nil, false
	}
	c := &m.CheckIn
	switch e.Kind {
	case CheckInStateRequested:
		// Account reads are allowed from any page: the scheduler needs them
		// even while the user is looking at the home page.
		c.LoadToken++
		c.Loading = true
		m.CheckIn = *c
		return m, []Effect{{Kind: TajiduoCheckInState, Token: c.LoadToken, Enabled: e.Check.Enabled}}, true
	case CheckInStateLoaded:
		if e.Token == 0 || e.Token != c.LoadToken {
			// A snapshot superseded by a newer read is dropped whole.
			return m, nil, true
		}
		return loadCheckIn(m, e), nil, true
	case CheckInStateFailed:
		if e.Token == 0 || e.Token != c.LoadToken {
			return m, nil, true
		}
		// The last usable snapshot stays on screen; only the reason changes.
		c.Loading = false
		c.LoadFailure = e.Check.Fail
		return m, nil, true
	case CheckInBegin, CheckInAutoTick:
		manual := e.Kind == CheckInBegin
		if manual {
			if m.Page != CheckIn || m.Overlay != NoOverlay || !e.Check.Kind.Valid() {
				return m, nil, true
			}
			k := e.Check.Kind
			if !c.Linked || c.NeedsRelink || c.Busy[k] {
				return m, nil, true
			}
			if c.Phase[k] == CheckInUnknown && !c.Confirm[k] {
				// The last write may already have reached the server. A click
				// alone does not repeat it; the page asks for confirmation
				// first, and that request arms this flag.
				return m, nil, true
			}
			confirmed := c.Phase[k] == CheckInUnknown
			next, fx := startedReport(*c, k, e.Now)
			fx.Confirmed = confirmed
			m.CheckIn = next
			return m, []Effect{fx}, true
		}
		// The tick never wakes an unlinked task.
		if !c.Linked {
			return m, nil, true
		}
		if today := CheckInDay(e.Now); c.Day == "" {
			// The first tick of a session adopts the day it sees.
			c.Day = today
		} else if c.Day != today {
			// A new local day makes yesterday's answers stale, even while the
			// launcher stayed open across midnight.
			*c = c.rollDay(today)
		}
		if !CheckInAutoWindow(e.Now) {
			return m, nil, true
		}
		today := CheckInDay(e.Now)
		var effects []Effect
		for k := 0; k < CheckInKinds; k++ {
			if !c.Auto[k] || !c.Known[k] || !canSign(*c, CheckInKind(k)) {
				continue
			}
			if c.Signed[k] || c.AttemptDay[k] == today {
				continue
			}
			// An automatic attempt is only ever the day's first one for this
			// task: a recorded answer — even a failed or unverified one — is
			// never retried on its own.
			if c.Result[k] != CheckResultNone || c.Phase[k] == CheckInUnknown {
				continue
			}
			next, fx := startedReport(*c, CheckInKind(k), e.Now)
			m.CheckIn = next
			effects = append(effects, fx)
		}
		if len(effects) == 0 {
			return m, nil, true
		}
		return m, effects, true
	case CheckInConfirm:
		k := e.Check.Kind
		if m.Page != CheckIn || m.Overlay != NoOverlay || !k.Valid() || !c.Linked {
			return m, nil, true
		}
		c.Confirm[k] = e.Check.Enabled
		return m, nil, true
	case CheckInSigned:
		if !e.Check.Kind.Valid() || e.Token == 0 {
			return m, nil, true
		}
		k := e.Check.Kind
		if e.Token != c.Token[k] {
			// A stale answer for a superseded attempt is dropped.
			return m, nil, true
		}
		c.Busy[k] = false
		c.Result[k] = e.Check.Result[k]
		c.Detail[k] = e.Check.Detail[k]
		c.Failure[k] = e.Check.Failure[k]
		c.At[k] = e.Check.At[k]
		if e.Check.Known[k] {
			c.Known[k] = true
			c.Signed[k] = e.Check.Signed[k]
		}
		switch c.Result[k] {
		case CheckResultSuccess, CheckResultAlready:
			c.Phase[k] = CheckInDone
			c.Signed[k] = true
		case CheckResultUnverified:
			c.Phase[k] = CheckInUnknown
			c.Signed[k] = false
		default:
			c.Phase[k] = CheckInFailed
		}
		return m, nil, true
	case CheckInAutoToggle:
		k := e.Check.Kind
		if !k.Valid() || !c.Linked || !c.Loaded {
			return m, nil, true
		}
		if c.Auto[k] == e.Check.Enabled {
			return m, nil, true
		}
		c.AutoToken[k]++
		c.Auto[k] = e.Check.Enabled
		c.AutoFailure[k] = CheckInFailureNone
		return m, []Effect{{Kind: TajiduoCheckInAuto, CheckInKind: k, Enabled: e.Check.Enabled, Token: c.AutoToken[k]}}, true
	case CheckInAutoSaved:
		k := e.Check.Kind
		if !k.Valid() || e.Token == 0 {
			return m, nil, true
		}
		if e.Check.Fail != CheckInFailureNone {
			// The write never reached the ledger: the switch goes back to what
			// is on disk. A newer request wins; its own answer settles it.
			c.AutoSaved[k] = e.Check.Enabled
			if e.Token == c.AutoToken[k] {
				c.Auto[k] = e.Check.Enabled
				c.AutoFailure[k] = e.Check.Fail
			}
			return m, nil, true
		}
		// Record the value that actually reached the ledger. If the user has
		// already asked for another one, a later write settles the difference;
		// a stale answer must not clear the newer request's error either.
		c.AutoSaved[k] = e.Check.Enabled
		if e.Token == c.AutoToken[k] {
			c.AutoFailure[k] = CheckInFailureNone
		}
		return m, nil, true
	case CheckInStatusRequested:
		if (m.Page != CheckIn && m.Page != GameData && m.Page != Home) || m.Overlay != NoOverlay || !c.Linked {
			return m, nil, true
		}
		c.StatusToken++
		c.StatusPhase = CheckStatusLoading
		c.StatusFailure = CheckInFailureNone
		return m, []Effect{{Kind: TajiduoCheckInStatus, Token: c.StatusToken}}, true
	case CheckInStatusLoaded:
		if e.Token == 0 || e.Token != c.StatusToken {
			return m, nil, true
		}
		c.StatusPhase = CheckStatusReady
		c.StatusFailure = CheckInFailureNone
		c.Role = e.Check.Role
		return m, nil, true
	case CheckInStatusFailed:
		if e.Token == 0 || e.Token != c.StatusToken {
			return m, nil, true
		}
		// The last numbers stay on screen with their own timestamp; only the
		// fact that this refresh failed is new.
		c.StatusPhase = CheckStatusFailed
		c.StatusFailure = e.Check.Fail
		return m, nil, true
	}
	return m, nil, false
}

// loadCheckIn applies one account snapshot. A snapshot for another account
// replaces the state instead of merging into it.
func loadCheckIn(m Model, e Event) Model {
	c := &m.CheckIn
	if c.AccountID != "" && e.Check.AccountID != c.AccountID {
		*c = c.Reset(e.Check.AccountID)
	}
	if c.AccountID == "" {
		c.AccountID = e.Check.AccountID
	}
	// A snapshot for another calendar day replaces yesterday's answers; the
	// ledger for the new day is what the page must show.
	if e.Check.Day != "" && c.Day != "" && c.Day != e.Check.Day {
		*c = c.rollDay(e.Check.Day)
	}
	// A link that now points at another role invalidates everything the
	// previous role's answers described, including the cached summary. The
	// snapshot that follows is read from the new role's ledger.
	if c.RoleID != "" && e.Check.RoleID != c.RoleID {
		for k := 0; k < CheckInKinds; k++ {
			c.Token[k]++
			c.Busy[k] = false
			c.AttemptDay[k] = ""
			c.Confirm[k] = false
		}
		c.StatusToken++
		c.StatusPhase = CheckStatusIdle
		c.StatusFailure = CheckInFailureNone
		c.Role = CheckInRole{}
	}
	c.Loading = false
	c.Loaded = true
	c.LoadFailure = e.Check.Fail
	c.Linked = e.Check.Linked
	c.NeedsRelink = e.Check.NeedsRelink
	c.RoleID = e.Check.RoleID
	if e.Check.Day != "" {
		c.Day = e.Check.Day
	}
	// A switch the user just flipped is not overwritten by a snapshot that
	// raced with its write; the switch's own answer settles it.
	for k := 0; k < CheckInKinds; k++ {
		pending := c.Auto[k] != c.AutoSaved[k]
		c.AutoSaved[k] = e.Check.Auto[k]
		if !pending {
			c.Auto[k] = e.Check.Auto[k]
		}
		c.AutoFailure[k] = CheckInFailureNone
	}
	c.Known = e.Check.Known
	c.Signed = e.Check.Signed
	for k := 0; k < CheckInKinds; k++ {
		if c.Busy[k] {
			// A write is still in flight; its own answer wins.
			continue
		}
		c.Result[k] = e.Check.Result[k]
		c.Detail[k] = e.Check.Detail[k]
		c.Failure[k] = e.Check.Failure[k]
		c.At[k] = e.Check.At[k]
		switch {
		case !c.Linked:
			c.Phase[k] = CheckInUnlinked
		case c.Result[k] == CheckResultUnverified:
			c.Phase[k] = CheckInUnknown
		case c.Signed[k] || c.Result[k] == CheckResultSuccess || c.Result[k] == CheckResultAlready:
			c.Phase[k] = CheckInDone
		case c.Result[k] == CheckResultFailed:
			c.Phase[k] = CheckInFailed
		default:
			c.Phase[k] = CheckInReady
		}
	}
	if !c.Linked {
		c.Auto = [CheckInKinds]bool{}
		c.AutoSaved = [CheckInKinds]bool{}
		c.AutoFailure = [CheckInKinds]CheckInFailure{}
		// A token bump drops a sign answer that can no longer apply, and the
		// record of the old link is not shown against the unlinked account.
		for k := 0; k < CheckInKinds; k++ {
			c.Busy[k] = false
			c.Token[k]++
			c.Phase[k] = CheckInUnlinked
			c.Result[k] = CheckResultNone
			c.Detail[k] = ""
			c.Failure[k] = CheckInFailureNone
			c.Signed[k] = false
			c.Confirm[k] = false
		}
		c.StatusPhase = CheckStatusIdle
		c.Role = CheckInRole{}
	}
	return m
}

// canSign reports whether a write for k may start now.
func canSign(c CheckInState, k CheckInKind) bool {
	return c.Linked && !c.NeedsRelink && !c.Busy[k] && c.Phase[k] != CheckInUnknown
}
