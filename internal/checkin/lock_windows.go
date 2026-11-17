// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package checkin

import (
	"errors"
	"os"

	"golang.org/x/sys/windows"
)

// lockFile takes the ledger lock for the lifetime of the returned file. The
// handle is opened without sharing, so a second Oddward process gets ErrBusy
// instead of writing over the first one's ledger. The kernel drops the lock
// when the process dies, so a crash never leaves a stale lock behind.
func lockFile(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_ALWAYS, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		if errors.Is(err, windows.ERROR_SHARING_VIOLATION) || errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return nil, ErrBusy
		}
		return nil, &os.PathError{Op: "lock", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

// unlock closes the handle, which releases the mandatory lock.
func unlock(f *os.File) { f.Close() }
