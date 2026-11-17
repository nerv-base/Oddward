// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package loginui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/process"
)

func startACKHelper(t *testing.T, kinds string, bind func(context.Context, process.Identity, uintptr, uintptr) error) (*Session, context.Context, string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	trace := filepath.Join(t.TempDir(), "received-frames.jsonl")
	t.Setenv("ODDWARD_SYNTHETIC_LOGINUI", "1")
	t.Setenv("ODDWARD_SYNTHETIC_WINDOW_BIND", kinds)
	t.Setenv("ODDWARD_SYNTHETIC_FRAME_TRACE", trace)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	t.Cleanup(cancel)
	s, err := Start(ctx, Config{Executable: exe, BindWindow: bind, EmbedWindow: bind}, Model{Screen: "notice"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, ctx, trace
}

func receiveAction(t *testing.T, s *Session, ctx context.Context, kind string) {
	t.Helper()
	select {
	case event, ok := <-s.Events():
		if !ok || event.Kind != kind || event.RequestID != "" {
			t.Fatalf("wanted business event %s; got %+v, open=%v, helper error=%v", kind, event, ok, s.Err())
		}
	case <-ctx.Done():
		t.Fatalf("timed out awaiting %s", kind)
	}
}

func receivedFrames(t *testing.T, path string) []envelope {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var frames []envelope
	for {
		var frame envelope
		err := decoder.Decode(&frame)
		if errors.Is(err, io.EOF) {
			return frames
		}
		if err != nil || validateEnvelope(frame) != nil {
			t.Fatalf("helper received a partial or invalid frame: %v", err)
		}
		frames = append(frames, frame)
	}
}

func waitHelperClosed(t *testing.T, s *Session, ctx context.Context) {
	t.Helper()
	select {
	case <-s.Done():
	case <-ctx.Done():
		t.Fatal("helper did not close")
	}
}

func TestWindowBoundWaitsForCallbackAndKeepsSessionAlive(t *testing.T) {
	for _, kind := range []string{"bind_overlay", "embed_overlay"} {
		t.Run(kind, func(t *testing.T) {
			t.Setenv("ODDWARD_SYNTHETIC_BIND_GATED", "1")
			called, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			s, ctx, trace := startACKHelper(t, kind, func(ctx context.Context, _ process.Identity, own, game uintptr) error {
				if own != 23 || game != 42 {
					return errors.New("wrong synthetic handles")
				}
				close(called)
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
			receiveAction(t, s, ctx, "ready")
			select {
			case <-called:
			case <-ctx.Done():
				t.Fatal("callback was not entered")
			}
			if err := s.Update(ctx, Model{Screen: "phone", Status: "binding-pending"}); err != nil {
				t.Fatal(err)
			}
			select {
			case action := <-s.Events():
				t.Fatalf("business event escaped while binding pending: %+v", action)
			default:
			}
			// This marker enters the same input stream before the gate opens.
			// The child rejects any ACK seen before it, detecting premature ACKs
			// even though Go's read loop is still inside the blocked callback.
			if err := s.Update(ctx, Model{Screen: "phone", Status: "binding-released"}); err != nil {
				t.Fatal(err)
			}
			once.Do(func() { close(release) })
			receiveAction(t, s, ctx, "send_sms")
			if err := s.Update(ctx, Model{Screen: "notice", Status: "after-callback-context-cancel"}); err != nil {
				t.Fatalf("callback context cancellation stopped helper: %v", err)
			}
			receiveAction(t, s, ctx, "acknowledge")
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			released, acknowledgements := false, 0
			for _, frame := range receivedFrames(t, trace) {
				if frame.Model != nil && frame.Model.Status == "binding-released" {
					released = true
				}
				if frame.Type == "window_bound" {
					want := Event{Kind: kind, Window: "23", GameWindow: "42", RequestID: "1"}
					if !released || frame.Model != nil || frame.Event == nil || *frame.Event != want {
						t.Fatalf("early or uncorrelated completion: %+v", frame)
					}
					acknowledgements++
				}
			}
			if acknowledgements != 1 {
				t.Fatalf("completion frames=%d, want 1", acknowledgements)
			}
		})
	}
}

func TestFailedWindowCallbackDoesNotAcknowledge(t *testing.T) {
	for _, kind := range []string{"bind_overlay", "embed_overlay"} {
		t.Run(kind, func(t *testing.T) {
			release := make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(release) })
			s, ctx, trace := startACKHelper(t, kind, func(ctx context.Context, _ process.Identity, _, _ uintptr) error {
				select {
				case <-release:
					return errors.New("synthetic secret-bearing callback error")
				case <-ctx.Done():
					return ctx.Err()
				}
			})
			once.Do(func() { close(release) })
			waitHelperClosed(t, s, ctx)
			if s.Err() != ErrHelper {
				t.Fatalf("callback failure not sanitized: %v", s.Err())
			}
			for _, frame := range receivedFrames(t, trace) {
				if frame.Type == "window_bound" {
					t.Fatal("failed callback emitted completion")
				}
			}
			for event := range s.Events() {
				if event.Kind != "ready" {
					t.Fatalf("internal or late action escaped rejected callback: %+v", event)
				}
			}
		})
	}
}

func TestWindowRequestsRejectReplaysAndReverseCompletion(t *testing.T) {
	for _, tc := range []struct {
		name, ids string
		reverse   bool
		calls     int32
	}{
		{name: "duplicate across kinds", ids: "4,4", calls: 1},
		{name: "decreasing across kinds", ids: "4,3", calls: 1},
		{name: "reverse completion", ids: "1", reverse: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("ODDWARD_SYNTHETIC_BIND_IDS", tc.ids)
			if tc.reverse {
				t.Setenv("ODDWARD_SYNTHETIC_REVERSE_ACK", "1")
			}
			var calls atomic.Int32
			s, ctx, trace := startACKHelper(t, "bind_overlay,embed_overlay", func(context.Context, process.Identity, uintptr, uintptr) error {
				calls.Add(1)
				return nil
			})
			waitHelperClosed(t, s, ctx)
			if s.Err() != ErrHelper || calls.Load() != tc.calls {
				t.Fatalf("invalid completion/request: error=%v callbacks=%d, want ErrHelper/%d", s.Err(), calls.Load(), tc.calls)
			}
			var acks int32
			for _, frame := range receivedFrames(t, trace) {
				if frame.Type == "window_bound" {
					acks++
				}
			}
			if acks != tc.calls {
				t.Fatalf("invalid request acknowledged: acks=%d accepted=%d", acks, tc.calls)
			}
			for event := range s.Events() {
				if event.Kind != "ready" {
					t.Fatalf("late/replayed frame became business event: %+v", event)
				}
			}
		})
	}
}

func TestWindowRequestsPermitIncreasingIDsWithGaps(t *testing.T) {
	t.Setenv("ODDWARD_SYNTHETIC_BIND_IDS", "1,7,18446744073709551615")
	var calls atomic.Int32
	s, ctx, trace := startACKHelper(t, "bind_overlay,embed_overlay", func(context.Context, process.Identity, uintptr, uintptr) error {
		calls.Add(1)
		return nil
	})
	receiveAction(t, s, ctx, "ready")
	receiveAction(t, s, ctx, "acknowledge")
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, frame := range receivedFrames(t, trace) {
		if frame.Type == "window_bound" {
			ids = append(ids, frame.Event.RequestID)
		}
	}
	if calls.Load() != 3 || fmt.Sprint(ids) != "[1 7 18446744073709551615]" {
		t.Fatalf("increasing IDs changed: callbacks=%d IDs=%v", calls.Load(), ids)
	}
}

func TestWindowCompletionSerializesWithRefreshAndClose(t *testing.T) {
	for _, concurrentClose := range []bool{false, true} {
		t.Run(fmt.Sprintf("close=%t", concurrentClose), func(t *testing.T) {
			called, start := make(chan struct{}), make(chan struct{})
			var once sync.Once
			defer once.Do(func() { close(start) })
			s, ctx, trace := startACKHelper(t, "embed_overlay", func(ctx context.Context, _ process.Identity, _, _ uintptr) error {
				close(called)
				select {
				case <-start:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			})
			receiveAction(t, s, ctx, "ready")
			select {
			case <-called:
			case <-ctx.Done():
				t.Fatal("callback not entered")
			}
			var workers sync.WaitGroup
			errs := make(chan error, 17)
			for i := range 16 {
				workers.Go(func() {
					<-start
					errs <- s.Update(ctx, Model{Screen: "phone", Status: fmt.Sprintf("refresh-%d", i)})
				})
			}
			if concurrentClose {
				workers.Go(func() { <-start; errs <- s.Close() })
			}
			once.Do(func() { close(start) })
			workers.Wait()
			close(errs)
			for err := range errs {
				if err != nil && !errors.Is(err, ErrClosed) {
					t.Fatalf("concurrent refresh/close returned %v", err)
				}
			}
			if !concurrentClose {
				select {
				case event := <-s.Events():
					// The ACK may precede the first concurrent phone snapshot.
					if (event.Kind != "send_sms" && event.Kind != "acknowledge") || event.RequestID != "" {
						t.Fatalf("internal completion escaped as an action: %+v", event)
					}
				case <-ctx.Done():
					t.Fatal("helper stopped responding during refresh")
				}
			}
			if err := s.Close(); err != nil {
				t.Fatal(err)
			}
			frames := receivedFrames(t, trace)
			acks := 0
			for _, frame := range frames {
				if frame.Type == "window_bound" {
					acks++
				}
			}
			if acks > 1 || (!concurrentClose && acks != 1) {
				t.Fatalf("completion frames=%d concurrentClose=%t", acks, concurrentClose)
			}
		})
	}
}
