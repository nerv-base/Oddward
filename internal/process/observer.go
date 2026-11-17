// SPDX-License-Identifier: GPL-3.0-or-later

// Package process observes explicitly supplied Windows process identities. It
// never discovers, launches, terminates, injects into or elevates for a target.
package process

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

var ErrClosed = errors.New("process observer is closed")
var ErrIdentityMismatch = errors.New("process creation identity does not match")
var ErrInvalidIdentity = errors.New("invalid process identity")

const (
	waitSignaled uint32 = 0
	waitTimeout  uint32 = 258
	waitSliceMS  uint32 = 100
)

// Identity is a practical PID-reuse guard, not an authentication token or a
// documented globally unique identity across boots. CreationTime is raw FILETIME.
type Identity struct {
	PID          uint32 `json:"pid"`
	CreationTime uint64 `json:"creation_time,string"`
}

// Observation is meaningful only when its accompanying error is nil. ExitCode
// and ExitTime are valid only after Exited. ExitTime is raw FILETIME, not elapsed
// playtime; failure/cancellation never manufactures a successful exit.
type Observation struct {
	Identity Identity
	Exited   bool
	ExitCode uint32
	ExitTime uint64
}

type nativeHandle interface {
	wait(uint32) (uint32, error)
	exitInfo() (uint32, uint64, error)
	close() error
}

// Handle retains one process object with query/wait rights. Operations serialize
// native handle use against Close. Use observation/wait/close on background
// workers, never a UI thread: cancellation/Close latency is nominally one 100ms
// wait slice plus scheduling/native-call latency, not a real-time guarantee.
// The zero value is closed. Do not copy a Handle after use.
type Handle struct {
	identity Identity
	mu       sync.Mutex
	native   nativeHandle
	closed   bool
	closeErr error
}

func newHandle(id Identity, native nativeHandle) *Handle {
	return &Handle{identity: id, native: native}
}

func (h *Handle) Identity() Identity { return h.identity }

// Poll confirms liveness or exit using a zero-time native wait on the retained
// process object. It does not reopen by PID or imply gameplay/login status.
func (h *Handle) Poll(ctx context.Context) (Observation, error) { return h.observe(ctx, 0) }

// Wait stops observing on cancellation without closing or terminating the target.
// A context canceled before an observation wins without OS access. If a native
// wait confirms exit concurrently with cancellation, that confirmed exit wins.
func (h *Handle) Wait(ctx context.Context) (Observation, error) {
	for {
		observation, err := h.observe(ctx, waitSliceMS)
		if err != nil || observation.Exited {
			return observation, err
		}
	}
}

func (h *Handle) observe(ctx context.Context, milliseconds uint32) (Observation, error) {
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Observation{}, err
	}
	if h.closed || h.native == nil {
		return Observation{}, ErrClosed
	}
	status, err := h.native.wait(milliseconds)
	if err != nil {
		return Observation{}, fmt.Errorf("wait for process: %w", err)
	}
	switch status {
	case waitTimeout:
		if err := ctx.Err(); err != nil {
			return Observation{}, err
		}
		return Observation{Identity: h.identity}, nil
	case waitSignaled:
		code, end, err := h.native.exitInfo()
		if err != nil {
			return Observation{}, fmt.Errorf("read process exit: %w", err)
		}
		if end == 0 {
			return Observation{}, errors.New("signaled process has no exit time")
		}
		return Observation{Identity: h.identity, Exited: true, ExitCode: code, ExitTime: end}, nil
	default:
		return Observation{}, fmt.Errorf("unexpected process wait status: %#x", status)
	}
}

// Close waits for an in-flight native operation before closing exactly once.
// A close failure is retained, not retried against a potentially recycled handle.
// Closing never terminates the observed process.
func (h *Handle) Close() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.closed {
		h.closed = true
		if h.native != nil {
			h.closeErr = h.native.close()
		}
	}
	return h.closeErr
}
