//go:build windows

// SPDX-License-Identifier: GPL-3.0-or-later

package process

import (
	"context"
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

const observeRights = windows.PROCESS_QUERY_LIMITED_INFORMATION | windows.SYNCHRONIZE

type windowsHandle struct{ handle windows.Handle }

func (h windowsHandle) wait(ms uint32) (uint32, error) {
	return windows.WaitForSingleObject(h.handle, ms)
}
func (h windowsHandle) close() error { return windows.CloseHandle(h.handle) }
func (h windowsHandle) exitInfo() (uint32, uint64, error) {
	var creation, end, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h.handle, &creation, &end, &kernel, &user); err != nil {
		return 0, 0, err
	}
	var code uint32
	if err := windows.GetExitCodeProcess(h.handle, &code); err != nil {
		return 0, 0, err
	}
	return code, filetime(end), nil
}
func filetime(t windows.Filetime) uint64 { return uint64(t.HighDateTime)<<32 | uint64(t.LowDateTime) }

// Capture duplicates a directly-created process's borrowed Go handle while
// WithHandle guarantees its identity. Call before Go Wait/Release. The duplicate
// is non-inheritable and narrowed to query-limited + synchronize rights; it owns
// an independent lifetime, without acquiring target terminate/VM/debug rights.
func Capture(ctx context.Context, p *os.Process) (*Handle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if p == nil {
		return nil, ErrInvalidIdentity
	}
	var duplicate windows.Handle
	var duplicateErr error
	borrowedErr := p.WithHandle(func(raw uintptr) {
		duplicateErr = windows.DuplicateHandle(windows.CurrentProcess(), windows.Handle(raw), windows.CurrentProcess(), &duplicate, observeRights, false, 0)
	})
	if borrowedErr != nil {
		return nil, fmt.Errorf("borrow created process handle: %w", borrowedErr)
	}
	if duplicateErr != nil {
		return nil, fmt.Errorf("retain created process handle: %w", duplicateErr)
	}
	return adopt(ctx, duplicate, nil)
}

// Attach reopens an explicitly supplied PID and rejects a different creation
// time. It does not establish intended game identity by itself. Access failure
// is unknown/unavailable, not proven exit; there is no scan or elevated retry.
func Attach(ctx context.Context, expected Identity) (*Handle, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if expected.PID == 0 || expected.CreationTime == 0 {
		return nil, ErrInvalidIdentity
	}
	raw, err := windows.OpenProcess(observeRights, false, expected.PID)
	if err != nil {
		return nil, fmt.Errorf("open observed process: %w", err)
	}
	return adopt(ctx, raw, &expected)
}

// adopt takes ownership even on failure. Every identity query uses this one
// retained object, not a fresh PID lookup. No liveness is inferred from creation.
func adopt(ctx context.Context, raw windows.Handle, expected *Identity) (result *Handle, err error) {
	defer func() {
		if result == nil {
			err = errors.Join(err, windows.CloseHandle(raw))
		}
	}()
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	pid, err := windows.GetProcessId(raw)
	if err != nil {
		return nil, fmt.Errorf("read observed process PID: %w", err)
	}
	var creation, end, kernel, user windows.Filetime
	if err = windows.GetProcessTimes(raw, &creation, &end, &kernel, &user); err != nil {
		return nil, fmt.Errorf("read process creation time: %w", err)
	}
	actual := Identity{PID: pid, CreationTime: filetime(creation)}
	if actual.PID == 0 || actual.CreationTime == 0 {
		return nil, ErrInvalidIdentity
	}
	if expected != nil && actual != *expected {
		return nil, ErrIdentityMismatch
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	return newHandle(actual, windowsHandle{raw}), nil
}
