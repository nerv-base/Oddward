// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package broker

import (
	"runtime"
	"testing"
	"unsafe"

	"github.com/nerv-base/Oddward/internal/process"
	"golang.org/x/sys/windows"
)

func TestNativeOverlayProcessChecksIdentityAndLifetime(t *testing.T) {
	var created, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(windows.CurrentProcess(), &created, &exit, &kernel, &user); err != nil {
		t.Fatal(err)
	}
	id := process.Identity{PID: windows.GetCurrentProcessId(), CreationTime: uint64(created.HighDateTime)<<32 | uint64(created.LowDateTime)}
	sys := nativeOverlaySystem{}
	p, err := sys.open(id)
	if err != nil {
		t.Fatal("cannot verify owned test process")
	}
	defer p.close()
	if p.identity != id || p.parentPID == 0 || p.sid == "" || !p.alive() {
		t.Fatal("native process metadata incomplete")
	}
	id.CreationTime++
	if invalid, err := sys.open(id); err == nil {
		invalid.close()
		t.Fatal("creation mismatch accepted")
	}
}

// These two test-owned STATIC windows are never shown, activated or enumerated.
// This verifies native owner write/readback only, not cross-integrity elevation
// or gameplay Z-order (which require a separate authorized integration probe).
func TestNativeOverlayHiddenWindowsOwnerReadback(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	create := overlayUser32.NewProc("CreateWindowExW")
	destroy := overlayUser32.NewProc("DestroyWindow")
	class := windows.StringToUTF16Ptr("STATIC")
	newWindow := func() uintptr {
		w, _, err := create.Call(0, uintptr(unsafe.Pointer(class)), 0, 0x80000000, 0, 0, 1, 1, 0, 0, 0, 0)
		if w == 0 {
			t.Fatalf("create hidden fixture window: %v", err)
		}
		return w
	}
	fixture := newWindow()
	defer destroy.Call(fixture)
	ui := newWindow()
	defer destroy.Call(ui)
	sys := nativeOverlaySystem{}
	before, err := sys.window(ui)
	if err != nil || !before.topLevel || before.visible || before.topmost || before.owner != 0 {
		t.Fatal("invalid hidden window fixture")
	}
	if err := sys.setOwner(ui, fixture); err != nil {
		t.Fatal("native own-window owner assignment failed")
	}
	actual, err := sys.window(ui)
	if err != nil || actual.owner != fixture || actual.visible || actual.topmost || actual.pid != windows.GetCurrentProcessId() {
		t.Fatal("native owner readback mismatch")
	}
	owner, err := sys.window(fixture)
	if err != nil || owner.owner != 0 || owner.visible {
		t.Fatal("owner fixture unexpectedly changed")
	}
}
