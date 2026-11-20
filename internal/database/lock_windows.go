// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/windows"
)

// lockRetry is how often an attempt is repeated while another process holds the
// lock. The wait itself is bounded by the caller's context.
const lockRetry = 50 * time.Millisecond

// Lock is an exclusive advisory lock on a file, held across processes. SQLite's
// own lock only covers the duration of a statement, which is too short to keep
// two launchers from migrating the same data; this lock covers the whole upgrade.
// The kernel releases it when the process dies, so a crash cannot leave a stale
// lock behind.
type Lock struct {
	file *os.File
	path string
}

// AcquireLock takes the exclusive lock on path, waiting until ctx is done.
func AcquireLock(ctx context.Context, path string) (*Lock, error) {
	file, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o600)
	if err != nil {
		return nil, err
	}
	handle := windows.Handle(file.Fd())
	for {
		overlapped := new(windows.Overlapped)
		err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, overlapped)
		if err == nil {
			return &Lock{file: file, path: path}, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) && !errors.Is(err, windows.ERROR_IO_PENDING) {
			file.Close()
			return nil, &os.PathError{Op: "lock", Path: path, Err: err}
		}
		if cerr := ctx.Err(); cerr != nil {
			file.Close()
			return nil, fmt.Errorf("%w: %s: %v", ErrBusy, path, cerr)
		}
		time.Sleep(lockRetry)
	}
}

// Path reports the file the lock is held on.
func (l *Lock) Path() string { return l.path }

// Release drops the lock. It is safe to call twice.
func (l *Lock) Release() error {
	if l == nil || l.file == nil {
		return nil
	}
	handle := windows.Handle(l.file.Fd())
	overlapped := new(windows.Overlapped)
	unlockErr := windows.UnlockFileEx(handle, 0, 1, 0, overlapped)
	closeErr := l.file.Close()
	l.file = nil
	return errors.Join(unlockErr, closeErr)
}
