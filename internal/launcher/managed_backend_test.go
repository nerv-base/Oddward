// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/launcher/state"
	"github.com/nerv-base/Oddward/internal/process"
)

type managedFakeBackend struct {
	locate  func(context.Context, string) (string, error)
	prepare func(context.Context, string, func(ManagedTransfer)) (GamePlan, error)
}

func (b managedFakeBackend) Locate(c context.Context, p string) (string, error) {
	return b.locate(c, p)
}
func (b managedFakeBackend) Prepare(c context.Context, p string, progress func(ManagedTransfer)) (GamePlan, error) {
	return b.prepare(c, p, progress)
}

type managedFakePlan struct {
	starts  *atomic.Int32
	session GameSession
}

func (managedFakePlan) Summary() string { return "synthetic managed plan" }
func (p managedFakePlan) Start(context.Context) (GameSession, error) {
	p.starts.Add(1)
	return p.session, nil
}

// managedClosingPlan implements the optional Close of a plan that holds an OS
// resource, such as the native login installation lock.
type managedClosingPlan struct {
	managedFakePlan
	closes *atomic.Int32
}

func (p managedClosingPlan) Close() error {
	p.closes.Add(1)
	return nil
}

type fakeLogin struct{}

func (*fakeLogin) SendCode(context.Context, string) error                 { return nil }
func (*fakeLogin) LoginPhone(context.Context, string, string, bool) error { return nil }
func (*fakeLogin) LoginSaved(context.Context, string) error               { return nil }
func (*fakeLogin) Forget(context.Context, string) error                   { return nil }

type managedFakeSession struct {
	*fakeLogin
	result  chan error
	closed  chan struct{}
	notices chan GameNotice
}

func newManagedFakeSession() managedFakeSession {
	return managedFakeSession{fakeLogin: &fakeLogin{}, result: make(chan error, 1), closed: make(chan struct{}), notices: make(chan GameNotice, 4)}
}
func (s managedFakeSession) Notices() <-chan GameNotice { return s.notices }

func (s managedFakeSession) Wait(ctx context.Context) error {
	select {
	case err := <-s.result:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (s managedFakeSession) Close() error { close(s.closed); return nil }

type managedFakeStore struct {
	value ManagedPreferences
	save  chan ManagedPreferences
}

func (s managedFakeStore) Load(context.Context) (ManagedPreferences, error) { return s.value, nil }
func (s managedFakeStore) Save(c context.Context, v ManagedPreferences) error {
	select {
	case s.save <- v:
		return nil
	case <-c.Done():
		return c.Err()
	}
}

func managedUntil(t *testing.T, s *Shell, wake <-chan struct{}, done func() bool) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	for !done() {
		select {
		case <-wake:
			s.syncManaged()
		case <-deadline.C:
			t.Fatalf("background result never woke ordinary UI: %+v", s.Model.Managed)
		}
	}
}
func managedTestShell(m state.Model) (*Shell, chan struct{}) {
	s := NewShell(m)
	w := make(chan struct{}, 32)
	s.RequestFrame = func() {
		select {
		case w <- struct{}{}:
		default:
		}
	}
	return s, w
}

func TestManagedWorkerRestoresAndRevalidatesSavedLocation(t *testing.T) {
	s, w := managedTestShell(state.InitialNormal())
	defer s.Dispose()
	s.ManagedStore = managedFakeStore{value: ManagedPreferences{GameRoot: `C:\saved`, RiskVersion: state.ManagedConsentVersion}, save: make(chan ManagedPreferences, 4)}
	s.ManagedBackend = managedFakeBackend{locate: func(ctx context.Context, p string) (string, error) {
		if p != `C:\saved` {
			t.Errorf("restore path=%q", p)
		}
		return `C:\canonical`, nil
	}}
	s.syncManaged()
	managedUntil(t, s, w, func() bool { return s.Model.Managed.Phase == state.ManagedReady })
	if s.Model.Managed.Root != `C:\canonical` || s.Model.Managed.RiskAccepted {
		t.Fatalf("a different inspected root must be adopted without old consent: %+v", s.Model.Managed)
	}
}

func TestManagedWorkerPrimaryPreparesThenCallsRealStartSeam(t *testing.T) {
	var starts atomic.Int32
	session := newManagedFakeSession()
	m := normalFixture(state.ManagedReady)
	m.Managed.RiskAccepted = true
	s, w := managedTestShell(m)
	defer s.Dispose()
	s.ManagedBackend = managedFakeBackend{prepare: func(ctx context.Context, root string, progress func(ManagedTransfer)) (GamePlan, error) {
		if root != m.Managed.Root {
			t.Errorf("wrong root %q", root)
		}
		progress(ManagedTransfer{Stage: "preparing", Downloaded: 50, Total: 100})
		return managedFakePlan{starts: &starts, session: session}, nil
	}}
	s.Dispatch(state.Event{Kind: state.Primary})
	managedUntil(t, s, w, func() bool { return s.Model.Managed.Phase == state.ManagedRunning })
	if starts.Load() != 1 || s.Model.Flow == state.Running {
		t.Fatalf("host Start calls=%v gameFlow=%v", starts.Load(), s.Model.Flow)
	}
	for i := 0; i < 20; i++ {
		s.Dispatch(state.Event{Kind: state.Primary})
	}
	if starts.Load() != 1 {
		t.Fatal("repeated primary created another host")
	}
	session.result <- nil
	managedUntil(t, s, w, func() bool { return s.Model.Managed.Phase == state.ManagedExited })
}

func TestManagedWorkerObservationErrorsKeepStartLock(t *testing.T) {
	for _, err := range []error{process.ErrClosed, errors.New("synthetic observation unavailable")} {
		var starts atomic.Int32
		session := newManagedFakeSession()
		m := normalFixture(state.ManagedReady)
		m.Managed.RiskAccepted = true
		s, w := managedTestShell(m)
		s.ManagedBackend = managedFakeBackend{prepare: func(context.Context, string, func(ManagedTransfer)) (GamePlan, error) {
			return managedFakePlan{starts: &starts, session: session}, nil
		}}
		s.Dispatch(state.Event{Kind: state.Primary})
		managedUntil(t, s, w, func() bool { return s.Model.Managed.Phase == state.ManagedRunning })
		session.result <- err
		managedUntil(t, s, w, func() bool { return s.Model.Managed.Phase == state.ManagedUnobserved })
		s.Dispatch(state.Event{Kind: state.Primary})
		if starts.Load() != 1 {
			t.Fatal("unknown observation unlocked retry")
		}
		s.Dispose()
	}
}

func TestManagedWorkerConfirmedExitCodeIsNotLoginSuccess(t *testing.T) {
	var starts atomic.Int32
	session := newManagedFakeSession()
	m := normalFixture(state.ManagedReady)
	m.Managed.RiskAccepted = true
	s, w := managedTestShell(m)
	defer s.Dispose()
	s.ManagedBackend = managedFakeBackend{prepare: func(context.Context, string, func(ManagedTransfer)) (GamePlan, error) {
		return managedFakePlan{starts: &starts, session: session}, nil
	}}
	s.Dispatch(state.Event{Kind: state.Primary})
	managedUntil(t, s, w, func() bool { return s.Model.Managed.Phase == state.ManagedRunning })
	session.result <- &GameExitError{Code: 17}
	managedUntil(t, s, w, func() bool { return s.Model.Managed.Phase == state.ManagedFailed })
	if s.Model.Flow == state.Running || s.Model.Managed.Error == "" {
		t.Fatal("confirmed error exit claimed successful game")
	}
}

func TestManagedWorkerCancelCannotLaunchLatePreparedPlan(t *testing.T) {
	release := make(chan struct{})
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	var starts atomic.Int32
	var closes atomic.Int32
	m := normalFixture(state.ManagedReady)
	m.Managed.RiskAccepted = true
	s, w := managedTestShell(m)
	defer s.Dispose()
	s.ManagedBackend = managedFakeBackend{prepare: func(ctx context.Context, _ string, _ func(ManagedTransfer)) (GamePlan, error) {
		close(entered)
		<-ctx.Done()
		close(cancelled)
		<-release
		return managedClosingPlan{managedFakePlan{starts: &starts}, &closes}, nil
	}}
	s.Dispatch(state.Event{Kind: state.Primary})
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("component preparation never began")
	}
	s.Dispatch(state.Event{Kind: state.ManagedCancel})
	select {
	case <-cancelled:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel never reached component manager")
	}
	close(release)
	s.syncManaged()
	select {
	case <-w:
		s.syncManaged()
	default:
	}
	deadline := time.Now().Add(2 * time.Second)
	for closes.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if starts.Load() != 0 || s.Model.Managed.Phase != state.ManagedReady {
		t.Fatal("late cancelled preparation started host")
	}
	if closes.Load() != 1 {
		t.Fatalf("late cancelled preparation leaked its plan: closes=%d", closes.Load())
	}
}

func TestManagedWorkerPickerCancelKeepsPreviouslyLocatedRoot(t *testing.T) {
	m := normalFixture(state.ManagedReady)
	m.Overlay = state.LocateGame
	s, w := managedTestShell(m)
	defer s.Dispose()
	s.PickGameDirectory = func(context.Context) (string, error) { return "", nil }
	s.Dispatch(state.Event{Kind: state.ManagedBrowse})
	managedUntil(t, s, w, func() bool { return s.Model.Managed.Phase == state.ManagedReady })
	if s.Model.Managed.Root != m.Managed.Root {
		t.Fatal("cancelled picker discarded saved game")
	}
}

func TestManagedWorkerPrepareDeadlineReachesUI(t *testing.T) {
	for _, latePlan := range []bool{false, true} {
		name := "deadline-error"
		if latePlan {
			name = "late-success-after-deadline"
		}
		t.Run(name, func(t *testing.T) {
			var starts atomic.Int32
			var closes atomic.Int32
			expired := make(chan error, 1)
			m := normalFixture(state.ManagedPreparing)
			m.Managed.RiskAccepted = true
			s, wake := managedTestShell(m)
			defer s.Dispose()
			s.ManagedBackend = managedFakeBackend{prepare: func(ctx context.Context, _ string, _ func(ManagedTransfer)) (GamePlan, error) {
				<-ctx.Done()
				expired <- ctx.Err()
				if latePlan {
					return managedClosingPlan{managedFakePlan{starts: &starts}, &closes}, nil
				}
				return nil, ctx.Err()
			}}
			s.beginManagedPrepareWithin(m.Managed.Request, 20*time.Millisecond)
			select {
			case err := <-expired:
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatalf("fixture did not return a real worker deadline: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("short preparation deadline did not reach the backend")
			}
			managedUntil(t, s, wake, func() bool { return s.Model.Managed.Phase == state.ManagedFailed })
			if !strings.Contains(s.Model.Managed.Error, context.DeadlineExceeded.Error()) || starts.Load() != 0 || s.managedPlan != nil {
				t.Fatalf("deadline was hidden or started a late plan: managed=%+v starts=%v cachedPlan=%v", s.Model.Managed, starts.Load(), s.managedPlan != nil)
			}
			if latePlan && closes.Load() != 1 {
				t.Fatalf("late plan after deadline leaked: closes=%d", closes.Load())
			}
		})
	}
}

func TestManagedWorkerRestoreAndOverrideBindConsentToRoot(t *testing.T) {
	for _, tt := range []struct {
		name, saved, override, inspected string
		wantConsent                      bool
	}{
		{"saved-root", `C:\saved`, "", `C:\saved`, true},
		{"saved-root-normalized", `C:\saved`, "", `c:\SAVED\.`, true},
		{"same-root-override", `C:\saved`, `c:\SAVED\.`, `C:\saved`, true},
		{"new-root-override", `C:\saved`, `C:\different`, `C:\different`, false},
		{"new-inspected-root", `C:\saved`, "", `C:\different`, false},
		{"consent-without-root", "", `C:\different`, `C:\different`, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			s, wake := managedTestShell(state.InitialNormal())
			defer s.Dispose()
			s.InitialManagedRoot = tt.override
			s.ManagedStore = managedFakeStore{value: ManagedPreferences{GameRoot: tt.saved, RiskVersion: state.ManagedConsentVersion}, save: make(chan ManagedPreferences, 4)}
			s.ManagedBackend = managedFakeBackend{locate: func(_ context.Context, selected string) (string, error) {
				want := tt.saved
				if tt.override != "" {
					want = tt.override
				}
				if selected != want {
					t.Errorf("inspection bypassed selected/saved path: got=%q want=%q", selected, want)
				}
				return tt.inspected, nil
			}}
			s.syncManaged()
			managedUntil(t, s, wake, func() bool { return s.Model.Managed.Phase == state.ManagedReady })
			if s.Model.Managed.Root != tt.inspected || s.Model.Managed.RiskAccepted != tt.wantConsent {
				t.Fatalf("saved consent leaked across root identity: managed=%+v wantConsent=%v", s.Model.Managed, tt.wantConsent)
			}
		})
	}
}

func TestManagedWorkerOpensTheLoginWindowWhenTheGameAsks(t *testing.T) {
	var starts atomic.Int32
	session := newManagedFakeSession()
	m := normalFixture(state.ManagedReady)
	m.Managed.RiskAccepted = true
	s, w := managedTestShell(m)
	defer s.Dispose()
	var shown []bool
	s.ShowLogin = func(open bool) { shown = append(shown, open) }
	s.ManagedBackend = managedFakeBackend{prepare: func(context.Context, string, func(ManagedTransfer)) (GamePlan, error) {
		return managedFakePlan{starts: &starts, session: session}, nil
	}}
	s.Dispatch(state.Event{Kind: state.Primary})
	managedUntil(t, s, w, func() bool { return s.Model.Managed.Phase == state.ManagedRunning })
	if s.LoginBackend != nil || state.LoginOpen(s.Model.Login.Phase) {
		t.Fatal("login offered before the game asked for it")
	}
	session.notices <- GameNotice{LoginRequested: true, Logins: []state.SavedLogin{{ID: "a1", Label: "主号 · 138****0000"}}}
	managedUntil(t, s, w, func() bool { return state.LoginOpen(s.Model.Login.Phase) })
	if s.LoginBackend == nil || s.Model.Login.Count != 1 || len(shown) != 1 || !shown[0] {
		t.Fatalf("login request did not reach the window: login=%+v backend=%v shown=%v", s.Model.Login, s.LoginBackend != nil, shown)
	}
	session.result <- nil
	managedUntil(t, s, w, func() bool { return s.Model.Managed.Phase == state.ManagedExited })
	if s.LoginBackend != nil || s.Model.Login.Phase != state.LoginIdle || shown[len(shown)-1] {
		t.Fatalf("game exit left login reachable: login=%+v backend=%v shown=%v", s.Model.Login, s.LoginBackend != nil, shown)
	}
	select {
	case <-session.closed:
	default:
		t.Fatal("session observer not released after exit")
	}
}

func TestManagedWorkerUnobservedStartKeepsTheLock(t *testing.T) {
	m := normalFixture(state.ManagedReady)
	m.Managed.RiskAccepted = true
	s, w := managedTestShell(m)
	defer s.Dispose()
	s.ManagedBackend = managedFakeBackend{prepare: func(context.Context, string, func(ManagedTransfer)) (GamePlan, error) {
		return unobservedPlan{}, nil
	}}
	s.Dispatch(state.Event{Kind: state.Primary})
	managedUntil(t, s, w, func() bool { return s.Model.Managed.Phase == state.ManagedUnobserved })
}

type unobservedPlan struct{}

func (unobservedPlan) Summary() string { return "synthetic unobserved plan" }
func (unobservedPlan) Start(context.Context) (GameSession, error) {
	return nil, fmt.Errorf("wait for creation: %w", ErrGameUnobserved)
}

func TestCancelManagedWorkClosesThePreparedPlan(t *testing.T) {
	var closes atomic.Int32
	s := &Shell{}
	s.managedPlan = managedClosingPlan{managedFakePlan{starts: new(atomic.Int32)}, &closes}
	s.managedPlanRoot = `C:\Game`
	s.cancelManagedWork()
	if closes.Load() != 1 {
		t.Fatalf("closes=%d want 1", closes.Load())
	}
	if s.managedPlan != nil || s.managedPlanRoot != "" {
		t.Fatal("cancelled plan must be released")
	}
}
