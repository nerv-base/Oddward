// SPDX-License-Identifier: GPL-3.0-or-later

package process

// Compound and concurrent regressions for the process observer. Every fixture in
// this file is deliberately owned and prefixed (conc*) so it cannot collide with
// fixtures from the other observer test files, and no test asserts a scheduler
// order: results are gated by channels/conditions, the native seam is a model of
// the OS contract (not evidence of real Windows behavior). Successful paths collect
// every result; watchdog failures need investigation rather than performance claims.

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// concIdentity is intentionally distinct from any other test's sample identity.
var concIdentity = Identity{PID: 4242, CreationTime: 133000000000000007}

// concBudget bounds test waits. A timeout is a failure to investigate: severe
// scheduling delays and native-operation bugs cannot be distinguished by it alone.
const concBudget = 5 * time.Second

// concFake models the private native seam. It records seam calls under a mutex,
// tracks how many calls are inside the seam right now, and captures what the
// native close observed, so serialization can be proven structurally instead of
// inferred from sleep durations.
type concFake struct {
	mu    sync.Mutex
	waits int
	infos int
	// closes counts native close calls; closeInFlight is the number of seam
	// calls that were running when close() was entered. Serialized closing must
	// always observe closeInFlight == 0.
	closes        int
	closeInFlight int
	waitsInFlight int
	closeErr      error
	waitErr       error
	infoErr       error
	status        uint32
	code          uint32
	end           uint64
	waitGate      chan struct{} // blocks inside wait() while open
	onWait        func(uint32)  // called from inside wait(), after entry is recorded
}

func (f *concFake) setExit(code uint32, end uint64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.code, f.end = waitSignaled, code, end
}

func (f *concFake) clearInfoError() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.infoErr = nil
}

func (f *concFake) counts() (waits, infos, closes, closeInFlight int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.waits, f.infos, f.closes, f.closeInFlight
}

func (f *concFake) wait(ms uint32) (uint32, error) {
	f.mu.Lock()
	f.waits++
	f.waitsInFlight++
	gate, hook, status, err := f.waitGate, f.onWait, f.status, f.waitErr
	f.mu.Unlock()
	if hook != nil {
		hook(ms)
	}
	if gate != nil {
		<-gate
	}
	f.mu.Lock()
	f.waitsInFlight--
	f.mu.Unlock()
	return status, err
}

func (f *concFake) exitInfo() (uint32, uint64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.infos++
	return f.code, f.end, f.infoErr
}

func (f *concFake) close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closes++
	f.closeInFlight = f.waitsInFlight
	return f.closeErr
}

// concAwait receives one value or fails the test at the watchdog deadline.
func concAwait[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(concBudget):
		t.Fatalf("%s did not settle within %v (deadlock or unbounded native wait)", what, concBudget)
	}
	var zero T
	return zero
}

// TestConcZeroValueHandleFailsClosed pins that a zero-value Handle owns no
// seam, fabricates nothing, and stays fail-closed under concurrent use.
func TestConcZeroValueHandleFailsClosed(t *testing.T) {
	var zero Handle
	h := &zero
	if got := h.Identity(); got != (Identity{}) {
		t.Fatalf("zero-value handle fabricated an identity: %+v", got)
	}
	if _, err := h.Poll(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("zero-value Poll must fail closed with ErrClosed, got %v", err)
	}
	if _, err := h.Wait(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("zero-value Wait must fail closed with ErrClosed, got %v", err)
	}
	first, second := h.Close(), h.Close()
	if (first == nil) != (second == nil) || (first != nil && first.Error() != second.Error()) {
		t.Fatalf("zero-value Close is not repeatable: %v then %v", first, second)
	}

	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := h.Poll(context.Background()); !errors.Is(err, ErrClosed) {
				t.Errorf("concurrent zero-value Poll: %v", err)
			}
			if _, err := h.Wait(context.Background()); !errors.Is(err, ErrClosed) {
				t.Errorf("concurrent zero-value Wait: %v", err)
			}
			_ = h.Close()
		}()
	}
	settled := make(chan struct{})
	go func() { wg.Wait(); close(settled) }()
	concAwait(t, settled, "concurrent zero-value operations")
}

// TestConcCancelAndCloseDuringInFlightWaitKeepOrder checks the compound case the
// sequential tests cannot: one close racing a canceling wait. The pending native
// step must be allowed to return, cancellation must then decide the outcome, and
// close must neither preempt the native call nor poison it with ErrClosed.
func TestConcCancelAndCloseDuringInFlightWaitKeepOrder(t *testing.T) {
	gate := make(chan struct{})
	release := sync.OnceFunc(func() { close(gate) })
	t.Cleanup(release)
	entered := make(chan struct{})
	var enteredOnce sync.Once
	f := &concFake{status: waitTimeout, waitGate: gate}
	f.onWait = func(ms uint32) {
		if ms == 0 || ms > 100 {
			t.Errorf("Wait used an unbounded native step of %dms", ms)
		}
		enteredOnce.Do(func() { close(entered) })
	}
	h := newHandle(concIdentity, f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type result struct {
		obs Observation
		err error
	}
	waited := make(chan result, 1)
	go func() {
		obs, err := h.Wait(ctx)
		waited <- result{obs, err}
	}()
	concAwait(t, entered, "native wait entry")

	closed := make(chan error, 1)
	go func() { closed <- h.Close() }()
	// Early detection only; the structural proof of serialization is the
	// closeInFlight count asserted below.
	select {
	case err := <-closed:
		t.Fatalf("Close returned while a native wait was in flight: %v", err)
	case <-time.After(20 * time.Millisecond):
	}

	// Cancellation is fully observed before the pending native step returns.
	cancel()
	release()

	got := concAwait(t, waited, "canceled wait")
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("cancellation during a native wait was not reported after the step: %+v %v", got.obs, got.err)
	}
	if got.obs.Exited {
		t.Fatalf("cancellation fabricated a confirmed exit: %+v", got.obs)
	}
	if err := concAwait(t, closed, "close waiting for the in-flight native call"); err != nil {
		t.Fatalf("close: %v", err)
	}

	waits, infos, closes, closeInFlight := f.counts()
	if waits != 1 {
		t.Errorf("canceled wait made %d native wait calls, want exactly the one in flight", waits)
	}
	if infos != 0 {
		t.Errorf("canceled wait queried exit info %d times without a confirmed exit", infos)
	}
	if closes != 1 || closeInFlight != 0 {
		t.Fatalf("native close ran %d times and observed %d in-flight native calls, want 1 and 0", closes, closeInFlight)
	}
	if _, err := h.Poll(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Poll after close: %v", err)
	}
	if _, err := h.Wait(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Wait after close: %v", err)
	}
}

// TestConcConcurrentObservationsFabricateNothingAcrossClose runs many observers
// against one closing handle. Each observer must either report the live state or
// ErrClosed; none may invent an exit, and closure must happen exactly once.
func TestConcConcurrentObservationsFabricateNothingAcrossClose(t *testing.T) {
	const observers = 8
	f := &concFake{status: waitTimeout}
	f.onWait = func(ms uint32) {
		if ms != 0 {
			t.Errorf("Poll used a %dms native wait, want a zero-time observation", ms)
		}
	}
	h := newHandle(concIdentity, f)

	type result struct {
		exited bool
		err    error
	}
	results := make(chan result, observers)
	start := make(chan struct{})
	for range observers {
		go func() {
			<-start
			obs, err := h.Poll(context.Background())
			results <- result{obs.Exited, err}
		}()
	}
	closed := make(chan error, 1)
	go func() {
		<-start
		closed <- h.Close()
	}()
	close(start)

	for range observers {
		got := concAwait(t, results, "concurrent poll")
		if got.exited {
			t.Fatalf("live handle reported a fabricated exit under concurrency")
		}
		if got.err != nil && !errors.Is(got.err, ErrClosed) {
			t.Fatalf("unexpected concurrent observation error: %v", got.err)
		}
	}
	if err := concAwait(t, closed, "concurrent close"); err != nil {
		t.Fatalf("close: %v", err)
	}
	_, infos, closes, closeInFlight := f.counts()
	if infos != 0 {
		t.Errorf("live observations queried exit info %d times", infos)
	}
	if closes != 1 || closeInFlight != 0 {
		t.Fatalf("native close ran %d times and observed %d in-flight native calls, want 1 and 0", closes, closeInFlight)
	}
	if _, err := h.Wait(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Wait after close: %v", err)
	}
}

// TestConcCancellationBetweenBoundedStepsLeavesHandleUsable covers progress
// across several bounded steps: cancellation is honored at the next step
// boundary, it is not sticky, and a later real exit is still observable.
func TestConcCancellationBetweenBoundedStepsLeavesHandleUsable(t *testing.T) {
	const endTime = 134123456789099999
	var steps atomic.Int32
	var cancelNow func()
	f := &concFake{status: waitTimeout}
	f.onWait = func(ms uint32) {
		if ms == 0 || ms > 100 {
			t.Errorf("Wait used an unbounded native step of %dms", ms)
		}
		if steps.Add(1) == 2 {
			// Cancellation arrives while the second bounded step is in flight.
			cancelNow()
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancelNow = cancel
	defer cancel()
	h := newHandle(concIdentity, f)

	obs, err := h.Wait(ctx)
	if !errors.Is(err, context.Canceled) || obs.Exited {
		t.Fatalf("cancellation during a bounded step: %+v %v", obs, err)
	}
	if got := steps.Load(); got != 2 {
		t.Errorf("cancellation was honored after %d bounded steps, want 2 (one in flight plus the boundary check)", got)
	}
	if _, infos, closes, _ := f.counts(); infos != 0 || closes != 0 {
		t.Fatalf("cancellation queried exit info %d times and closed %d times", infos, closes)
	}

	f.setExit(17, endTime)
	obs, err = h.Wait(context.Background())
	if err != nil || !obs.Exited || obs.ExitCode != 17 || obs.ExitTime != endTime {
		t.Fatalf("confirmed exit lost after cancellation: %+v %v", obs, err)
	}
	if obs.Identity != concIdentity {
		t.Fatalf("identity drifted across observations: %+v", obs.Identity)
	}
	if err := h.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if _, _, closes, _ := f.counts(); closes != 1 {
		t.Errorf("native close ran %d times, want 1", closes)
	}
}

// TestConcConfirmedExitBeatsConcurrentCancellationBarrier makes the race explicit
// across goroutines: cancellation is completed on another goroutine before the
// pending native wait reports exit, so the confirmed exit must win.
func TestConcConfirmedExitBeatsConcurrentCancellationBarrier(t *testing.T) {
	const endTime = 134123456789012345
	proceed := make(chan struct{})
	release := sync.OnceFunc(func() { close(proceed) })
	t.Cleanup(release)
	entered := make(chan struct{})
	var enteredOnce sync.Once
	f := &concFake{status: waitSignaled, code: 17, end: endTime, waitGate: proceed}
	f.onWait = func(uint32) { enteredOnce.Do(func() { close(entered) }) }
	h := newHandle(concIdentity, f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type result struct {
		obs Observation
		err error
	}
	done := make(chan result, 1)
	go func() {
		obs, err := h.Wait(ctx)
		done <- result{obs, err}
	}()
	concAwait(t, entered, "native wait entry")

	cancel()
	release() // the native wait now reports a signaled exit

	got := concAwait(t, done, "exit beating simultaneous cancellation")
	if got.err != nil || !got.obs.Exited || got.obs.ExitCode != 17 || got.obs.ExitTime != endTime {
		t.Fatalf("confirmed exit was discarded for a cancellation: %+v %v", got.obs, got.err)
	}
	waits, infos, closes, _ := f.counts()
	if waits != 1 || infos != 1 {
		t.Errorf("observed exit used %d waits and %d exit-info queries, want 1 and 1", waits, infos)
	}
	if closes != 0 {
		t.Fatalf("an observation closed the handle %d times", closes)
	}
	again, err := h.Poll(context.Background())
	if err != nil || again != got.obs {
		t.Fatalf("confirmed exit was not observable again: %+v %v", again, err)
	}
}

// TestConcSignalExitInfoFailureIsNotMaskedByCancellation keeps a genuine
// observation failure visible when cancellation is also pending: the wait
// reported exit, so the failed exit query is the result - not a cancellation
// error and never a fabricated exit.
func TestConcSignalExitInfoFailureIsNotMaskedByCancellation(t *testing.T) {
	failure := errors.New("native exit info failure")
	proceed := make(chan struct{})
	release := sync.OnceFunc(func() { close(proceed) })
	t.Cleanup(release)
	entered := make(chan struct{})
	var enteredOnce sync.Once
	f := &concFake{status: waitSignaled, infoErr: failure, waitGate: proceed}
	f.onWait = func(uint32) { enteredOnce.Do(func() { close(entered) }) }
	h := newHandle(concIdentity, f)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type result struct {
		obs Observation
		err error
	}
	done := make(chan result, 1)
	go func() {
		obs, err := h.Wait(ctx)
		done <- result{obs, err}
	}()
	concAwait(t, entered, "native wait entry")

	cancel()
	release()

	got := concAwait(t, done, "wait reporting a failed exit query")
	if got.obs.Exited {
		t.Fatalf("failed exit query became a confirmed exit: %+v", got.obs)
	}
	if got.err == nil || !errors.Is(got.err, failure) {
		t.Fatalf("observation failure was masked by cancellation: %v", got.err)
	}

	// An observation failure is not a poisoned handle: the seam recovers, the
	// exit is then confirmed normally.
	f.clearInfoError()
	f.setExit(259, 134123456789012345)
	after, err := h.Wait(context.Background())
	if err != nil || !after.Exited || after.ExitCode != 259 {
		t.Fatalf("handle unusable after an observation failure: %+v %v", after, err)
	}
}

// TestConcConcurrentCloseIsSingleAndSticky proves closure happens exactly once
// and every concurrent caller receives the same sticky close error.
func TestConcConcurrentCloseIsSingleAndSticky(t *testing.T) {
	failure := errors.New("native close failure")
	const closers = 6
	f := &concFake{closeErr: failure}
	h := newHandle(concIdentity, f)

	results := make(chan error, closers)
	start := make(chan struct{})
	for range closers {
		go func() {
			<-start
			results <- h.Close()
		}()
	}
	close(start)

	for range closers {
		if err := concAwait(t, results, "concurrent close"); !errors.Is(err, failure) {
			t.Fatalf("concurrent close lost the sticky close error: %v", err)
		}
	}
	if _, _, closes, _ := f.counts(); closes != 1 {
		t.Fatalf("native close ran %d times under concurrency, want 1", closes)
	}
	if _, err := h.Poll(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Poll after close: %v", err)
	}
	if err := h.Close(); !errors.Is(err, failure) {
		t.Fatalf("close error is not sticky for later callers: %v", err)
	}
}
