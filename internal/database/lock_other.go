// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !windows

package database

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

const lockRetry = 50 * time.Millisecond

// Lock is an exclusive advisory lock on a file, held across processes. See the
// Windows implementation for the reasoning; this fallback keeps the package
// testable off Windows.
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
	for {
		err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return &Lock{file: file, path: path}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
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
	unlockErr := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	return errors.Join(unlockErr, closeErr)
}
