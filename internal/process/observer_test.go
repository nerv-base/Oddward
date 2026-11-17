// SPDX-License-Identifier: GPL-3.0-or-later

package process

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type fakeHandle struct {
	status                     uint32
	waitErr, infoErr, closeErr error
	code                       uint32
	end                        uint64
	waits, infos, closes       int
	onWait                     func(uint32)
}

func (f *fakeHandle) wait(ms uint32) (uint32, error) {
	f.waits++
	if f.onWait != nil {
		f.onWait(ms)
	}
	return f.status, f.waitErr
}
func (f *fakeHandle) exitInfo() (uint32, uint64, error) { f.infos++; return f.code, f.end, f.infoErr }
func (f *fakeHandle) close() error                      { f.closes++; return f.closeErr }

var sampleIdentity = Identity{PID: 123, CreationTime: 134123456789012345}

func TestIdentityJSONKeepsExactCreationTime(t *testing.T) {
	b, err := json.Marshal(sampleIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"creation_time":"134123456789012345"`) {
		t.Fatalf("unsafe timestamp JSON: %s", b)
	}
	var got Identity
	if err = json.Unmarshal(b, &got); err != nil || got != sampleIdentity {
		t.Fatalf("round trip: %+v %v", got, err)
	}
}
func TestPollDistinguishesLiveFromConfirmedExit(t *testing.T) {
	f := &fakeHandle{status: waitTimeout, code: 259, end: 134123456789099999}
	h := newHandle(sampleIdentity, f)
	got, err := h.Poll(context.Background())
	if err != nil || got.Identity != sampleIdentity || got.Exited || f.infos != 0 {
		t.Fatalf("live: %+v %v infoCalls=%d", got, err, f.infos)
	}
	f.status = waitSignaled
	got, err = h.Poll(context.Background())
	if err != nil || !got.Exited || got.ExitCode != 259 || got.ExitTime != f.end {
		t.Fatalf("exit: %+v %v", got, err)
	}
	if h.Identity() != sampleIdentity {
		t.Fatal("identity changed")
	}
}
func TestObservationFailuresNeverBecomeExit(t *testing.T) {
	failure := errors.New("native failure")
	for _, tc := range []struct {
		name  string
		f     fakeHandle
		cause error
	}{
		{"wait", fakeHandle{waitErr: failure}, failure},
		{"exit-info", fakeHandle{status: waitSignaled, infoErr: failure}, failure},
		{"unexpected-status", fakeHandle{status: 128}, nil},
		{"zero-exit-time", fakeHandle{status: waitSignaled}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHandle(sampleIdentity, &tc.f)
			got, err := h.Poll(context.Background())
			if err == nil || got.Exited {
				t.Fatalf("fabricated success: %+v %v", got, err)
			}
			if tc.cause != nil && !errors.Is(err, tc.cause) {
				t.Fatalf("lost cause: %v", err)
			}
		})
	}
}
func TestCancellationDoesNotCloseOrTerminateTarget(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &fakeHandle{status: waitTimeout}
	f.onWait = func(ms uint32) {
		if ms == 0 || ms > 100 {
			t.Errorf("unbounded wait %d", ms)
		}
		cancel()
	}
	h := newHandle(sampleIdentity, f)
	got, err := h.Wait(ctx)
	if !errors.Is(err, context.Canceled) || got.Exited || f.closes != 0 || f.waits != 1 {
		t.Fatalf("cancel: %+v %v %+v", got, err, f)
	}
	f.onWait = nil
	if _, err = h.Poll(context.Background()); err != nil {
		t.Fatal(err)
	}
}
func TestPreCanceledWaitDoesNotQuery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	f := &fakeHandle{status: waitSignaled, end: 9}
	h := newHandle(sampleIdentity, f)
	for _, fn := range []func(context.Context) (Observation, error){h.Poll, h.Wait} {
		if _, err := fn(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("got %v", err)
		}
	}
	if f.waits != 0 {
		t.Fatal("pre-canceled observation queried OS")
	}
}
func TestExitWinsCancellationDuringNativeWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f := &fakeHandle{status: waitSignaled, end: 9, code: 17, onWait: func(uint32) { cancel() }}
	got, err := newHandle(sampleIdentity, f).Wait(ctx)
	if err != nil || !got.Exited || got.ExitCode != 17 {
		t.Fatalf("confirmed exit lost: %+v %v", got, err)
	}
}
func TestCloseIsIdempotentAndNeverReusesHandle(t *testing.T) {
	failure := errors.New("close failure")
	f := &fakeHandle{closeErr: failure}
	h := newHandle(sampleIdentity, f)
	for range 2 {
		if err := h.Close(); !errors.Is(err, failure) {
			t.Fatalf("lost close error %v", err)
		}
	}
	if f.closes != 1 {
		t.Fatalf("closes=%d", f.closes)
	}
	if _, err := h.Poll(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("query after close %v", err)
	}
	if _, err := h.Wait(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("wait after close %v", err)
	}
	if f.waits != 0 {
		t.Fatal("used closed handle")
	}
}
func TestCloseWaitsForInFlightNativeCall(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var active atomic.Bool
	f := &fakeHandle{status: waitTimeout, onWait: func(uint32) { active.Store(true); close(entered); <-release; active.Store(false) }}
	h := newHandle(sampleIdentity, f)
	polled := make(chan error, 1)
	go func() { _, err := h.Poll(context.Background()); polled <- err }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("query never entered")
	}
	closed := make(chan error, 1)
	go func() { closed <- h.Close() }()
	select {
	case <-closed:
		t.Fatal("closed a pending native call")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-polled; err != nil {
		t.Fatal(err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if active.Load() || f.closes != 1 {
		t.Fatal("bad close order")
	}
}
