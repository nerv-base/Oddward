// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !windows

package checkin

import (
	"errors"
	"os"
	"time"
)

// staleLock is how long a lock file may sit before it is assumed abandoned.
// Only non-Windows builds use this path; they exist so the package builds and
// tests outside the product target.
const staleLock = 2 * time.Minute

// lockFile creates the lock file exclusively. A lock older than staleLock is
// treated as abandoned and replaced once.
func lockFile(path string) (*os.File, error) {
	for attempt := 0; attempt < 2; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		info, statErr := os.Stat(path)
		if statErr != nil || time.Since(info.ModTime()) < staleLock {
			return nil, ErrBusy
		}
		os.Remove(path)
	}
	return nil, ErrBusy
}

// unlock removes the portable lock file.
func unlock(f *os.File) {
	name := f.Name()
	f.Close()
	os.Remove(name)
}
