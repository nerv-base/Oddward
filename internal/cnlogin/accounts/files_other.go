// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !windows

package accounts

import "os"

// privateMkdir creates path with owner-only permissions.
func privateMkdir(path string) error { return os.Mkdir(path, 0o700) }

// protectDirectory restricts an existing directory to its owner.
func protectDirectory(path string) error { return os.Chmod(path, 0o700) }

// openRegular opens an existing regular file for reading. Anything that is not
// a plain regular file is reported as errNotRegular so callers count it as
// corrupt instead of following it.
func openRegular(path string) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	if !info.Mode().IsRegular() {
		f.Close()
		return nil, errNotRegular
	}
	return f, nil
}

// createPrivate creates a new owner-only file.
func createPrivate(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
}

// removeRegular deletes a regular, non-reparse file. expected, when not nil,
// must describe the same file.
func removeRegular(path string, expected os.FileInfo) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || isReparse(info) || (expected != nil && !os.SameFile(expected, info)) {
		return ErrUnsafePath
	}
	return os.Remove(path)
}
