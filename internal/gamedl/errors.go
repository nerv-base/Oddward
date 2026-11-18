// SPDX-License-Identifier: GPL-3.0-or-later
package gamedl

import (
	"context"
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

type ErrorKind int

const (
	ErrNetwork ErrorKind = iota
	ErrHTTP
	ErrRangeMismatch
	ErrChecksum
	ErrDiskFull
	ErrPermission
	ErrPathUnsafe
	ErrTargetExists
	ErrStaleTask
	ErrLocked
	ErrCancelled
	ErrRecoveryRequired
	ErrPatchRejected
	ErrPatchSourceMismatch
	ErrPatchBlobMissing
	ErrRebuildFailed
	ErrRebuildVerifyFailed
	ErrPatchUnsupported
	ErrRangeRejected
)

type TaskError struct {
	Kind           ErrorKind
	Path, Host, Op string
	HTTPStatus     int
	Err            error
}

func (e *TaskError) Error() string {
	return fmt.Sprintf("game download %s: kind=%d path=%q host=%q status=%d", e.Op, e.Kind, e.Path, e.Host, e.HTTPStatus)
}
func (e *TaskError) Unwrap() error { return e.Err }
func failure(kind ErrorKind, op, path string, err error) error {
	return &TaskError{Kind: kind, Op: op, Path: path, Err: err}
}
func classify(op, path string, err error) error {
	if err == nil {
		return nil
	}
	var te *TaskError
	if errors.As(err, &te) {
		return err
	}
	kind := ErrNetwork
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		kind = ErrCancelled
	case errors.Is(err, windows.ERROR_DISK_FULL), errors.Is(err, windows.ERROR_HANDLE_DISK_FULL):
		kind = ErrDiskFull
	case errors.Is(err, os.ErrPermission):
		kind = ErrPermission
	}
	return failure(kind, op, path, err)
}
