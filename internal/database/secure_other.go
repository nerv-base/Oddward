// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !windows

package database

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// checkDataPath validates a non-Windows path. The product targets Windows; this
// fallback keeps the package testable elsewhere with the same contract.
func checkDataPath(path string) (string, error) {
	if path == "" {
		return "", fmt.Errorf("%w: empty path", ErrUnsafePath)
	}
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return "", fmt.Errorf("%w: %s is not absolute", ErrUnsafePath, path)
	}
	for current := clean; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				return "", err
			}
		} else if info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("%w: %s is a symlink", ErrUnsafePath, current)
		}
		if parent := filepath.Dir(current); parent == current {
			break
		}
	}
	if info, err := os.Lstat(clean); err == nil && !info.Mode().IsRegular() {
		return "", fmt.Errorf("%w: %s is not a regular file", ErrUnsafePath, clean)
	}
	return clean, nil
}

func ensureDataDir(dir string, writable bool) error {
	if dir == "" || dir == "." {
		return fmt.Errorf("%w: empty data directory", ErrUnsafePath)
	}
	if _, err := os.Stat(dir); err != nil {
		if !writable {
			return err
		}
		return os.MkdirAll(dir, 0o700)
	}
	return nil
}

func (db *DB) protectFiles() error { return nil }

// protectFile is a no-op outside Windows: the private DACL re-applied to the
// database and mirror files is a Windows security feature.
func protectFile(path string) error { return nil }
