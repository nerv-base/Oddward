// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package overlaywin

import (
	"errors"
	"fmt"
	"strconv"

	"golang.org/x/sys/windows"
)

// Identity is the PID + creation-time pair that process identity is based on.
// The creation time is the Windows FILETIME of the process, formatted as a
// canonical decimal string on the login UI wire protocol.
type Identity struct {
	PID          uint32
	CreationTime uint64
}

// OpenIdentity opens pid with query and synchronize rights only and returns
// its verified identity. The handle is owned by the caller.
func OpenIdentity(pid uint32) (windows.Handle, Identity, error) {
	if pid == 0 {
		return 0, Identity{}, errors.New("overlay: process id is zero")
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return 0, Identity{}, fmt.Errorf("overlay: open process %d: %w", pid, err)
	}
	id, err := IdentityOf(h)
	if err != nil {
		_ = windows.CloseHandle(h)
		return 0, Identity{}, err
	}
	id.PID = pid
	return h, id, nil
}

// OpenVerified opens id.PID with query and synchronize rights and checks that
// its kernel creation time matches id.CreationTime exactly. The returned handle
// pins the process identity against PID reuse until closed.
func OpenVerified(id Identity) (windows.Handle, error) {
	if id.PID == 0 || id.CreationTime == 0 {
		return 0, errors.New("overlay: incomplete process identity")
	}
	h, got, err := OpenIdentity(id.PID)
	if err != nil {
		return 0, err
	}
	if got.CreationTime != id.CreationTime {
		_ = windows.CloseHandle(h)
		return 0, fmt.Errorf("overlay: process %d identity mismatch", id.PID)
	}
	return h, nil
}

// CloseIdentity releases a handle returned by OpenIdentity or OpenVerified.
func CloseIdentity(h windows.Handle) error {
	if h == 0 {
		return nil
	}
	return windows.CloseHandle(h)
}

// ProcessAlive checks the retained process handle without blocking. Using
// WaitForSingleObject avoids GetExitCodeProcess's STILL_ACTIVE (259) ambiguity.
func ProcessAlive(h windows.Handle) (bool, error) {
	if h == 0 {
		return false, errors.New("overlay: process handle is closed")
	}
	state, err := windows.WaitForSingleObject(h, 0)
	if err != nil {
		return false, fmt.Errorf("overlay: wait process: %w", err)
	}
	switch state {
	case uint32(windows.WAIT_TIMEOUT):
		return true, nil
	case uint32(windows.WAIT_OBJECT_0):
		return false, nil
	default:
		return false, fmt.Errorf("overlay: unexpected wait state %d", state)
	}
}

// IdentityOf reads the creation time of an already opened process handle.
func IdentityOf(h windows.Handle) (Identity, error) {
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(h, &creation, &exit, &kernel, &user); err != nil {
		return Identity{}, fmt.Errorf("overlay: process times: %w", err)
	}
	return Identity{CreationTime: uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime)}, nil
}

// ParseCreationTime parses the canonical decimal creation time used by the
// login UI protocol.
func ParseCreationTime(s string) (uint64, error) {
	v, err := strconv.ParseUint(s, 10, 64)
	if err != nil || v == 0 || strconv.FormatUint(v, 10) != s {
		return 0, fmt.Errorf("overlay: invalid creation time")
	}
	return v, nil
}

// Verify checks that pid still has the expected creation time.
func (id Identity) Verify() error {
	h, err := OpenVerified(id)
	if err != nil {
		return err
	}
	_ = CloseIdentity(h)
	return nil
}

// CurrentImagePath returns the running process's actual Win32 image path from
// the kernel, avoiding stale paths if the executable was renamed on disk.
func CurrentImagePath() (string, error) {
	buf := make([]uint16, 32768)
	size := uint32(len(buf))
	if err := windows.QueryFullProcessImageName(windows.CurrentProcess(), 0, &buf[0], &size); err != nil {
		return "", fmt.Errorf("overlay: current image path: %w", err)
	}
	return windows.UTF16ToString(buf[:size]), nil
}
