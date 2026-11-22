//go:build windows

package overlaywin

import (
	"errors"
	"fmt"
	"strconv"

	"golang.org/x/sys/windows"
)

// Identity is the PID + creation-time pair that process identity is based on.
// The creation time is the Windows FILETIME of the process, as a decimal
// string in the same form the login protocol uses.
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
		windows.CloseHandle(h)
		return 0, Identity{}, err
	}
	id.PID = pid
	return h, id, nil
}

// CloseIdentity releases a handle returned by OpenIdentity.
func CloseIdentity(h windows.Handle) error {
	return windows.CloseHandle(h)
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
	if err != nil || v == 0 {
		return 0, fmt.Errorf("overlay: invalid creation time %q", s)
	}
	return v, nil
}

// Verify checks that pid still has the expected creation time.
func (id Identity) Verify() error {
	if id.PID == 0 || id.CreationTime == 0 {
		return errors.New("overlay: incomplete process identity")
	}
	h, _, err := OpenIdentity(id.PID)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	got, err := IdentityOf(h)
	if err != nil {
		return err
	}
	if got.CreationTime != id.CreationTime {
		return fmt.Errorf("overlay: process %d identity changed", id.PID)
	}
	return nil
}
