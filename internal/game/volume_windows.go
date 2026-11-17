// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package game

import (
	"fmt"
	"syscall"
	"unsafe"
)

// getDriveTypeW is resolved lazily through the standard library so no new dependency is
// added. A missing export is reported as an error instead of panicking inside the call.
var getDriveTypeW = syscall.NewLazyDLL("kernel32.dll").NewProc("GetDriveTypeW")

// checkLocalVolume asks Windows which kind of volume holds p and accepts only the kinds
// this milestone supports. Inspect calls it before any path resolution, so a mapped
// network drive is rejected from its drive type without contacting the remote server.
// GetDriveTypeW is not documented to set a last error on every failure, so an unusable
// result (DRIVE_UNKNOWN/DRIVE_NO_ROOT_DIR) is treated as a rejection rather than an error.
func checkLocalVolume(p string) error {
	root, err := volumeRoot(p)
	if err != nil {
		return err
	}
	if err := getDriveTypeW.Find(); err != nil {
		return fmt.Errorf("GetDriveTypeW unavailable: %w", err)
	}
	ptr, err := syscall.UTF16PtrFromString(root)
	if err != nil {
		return fmt.Errorf("volume root %q: %w", root, err)
	}
	kind, _, _ := getDriveTypeW.Call(uintptr(unsafe.Pointer(ptr)))
	if err := classifyDriveType(uint32(kind)); err != nil {
		return fmt.Errorf("volume %s: %w", root, err)
	}
	return nil
}
