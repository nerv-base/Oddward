// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package gamedl

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"golang.org/x/sys/windows"
)

// Lease is a kernel-owned exclusive workRoot/lock. A crashed process releases
// the lock automatically; the file's existence never implies ownership.
type Lease struct {
	mu     sync.Mutex
	writer sync.Mutex
	root   *os.Root
	file   *os.File
	path   string
	users  int
}

func Acquire(workRoot string) (*Lease, error) {
	if err := safeRootPath(workRoot); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(workRoot)
	if err != nil {
		return nil, classify("open root", workRoot, err)
	}
	f, err := openSafe(root, "lock", os.O_CREATE|os.O_RDWR)
	if err != nil {
		root.Close()
		return nil, err
	}
	var overlap windows.Overlapped
	err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &overlap)
	if err != nil {
		f.Close()
		root.Close()
		if errors.Is(err, windows.ERROR_LOCK_VIOLATION) || errors.Is(err, windows.ERROR_SHARING_VIOLATION) {
			return nil, failure(ErrLocked, "acquire lock", "lock", err)
		}
		return nil, classify("acquire lock", "lock", err)
	}
	absolute, _ := filepath.Abs(workRoot)
	return &Lease{root: root, file: f, path: filepath.Clean(absolute)}, nil
}
func (l *Lease) borrow(workRoot string) (*os.Root, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	absolute, err := filepath.Abs(workRoot)
	if err != nil || l.file == nil || !strings.EqualFold(l.path, filepath.Clean(absolute)) {
		return nil, failure(ErrLocked, "borrow lock", "lock", err)
	}
	root, err := l.root.OpenRoot(".")
	if err != nil {
		return nil, err
	}
	l.users++
	return root, nil
}
func (l *Lease) release() { l.mu.Lock(); l.users--; l.mu.Unlock() }
func (l *Lease) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	if l.users != 0 {
		return failure(ErrLocked, "lock still in use", "lock", nil)
	}
	var overlap windows.Overlapped
	err := windows.UnlockFileEx(windows.Handle(l.file.Fd()), 0, 1, 0, &overlap)
	closeErr := l.file.Close()
	rootErr := l.root.Close()
	l.file = nil
	return errors.Join(err, closeErr, rootErr)
}
