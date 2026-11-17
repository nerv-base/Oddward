//go:build windows

// SPDX-License-Identifier: GPL-3.0-or-later
package launch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestFindGameProcessRetainsVerifiedHandle(t *testing.T) {
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	release := filepath.Join(dir, "release")
	game, err := startProcess(context.Background(), LaunchPlan{
		Image: exe, CommandLine: RequiredArguments, Directory: dir,
		Environment: append(os.Environ(), "ODDWARD_LAUNCH_HELPER=1", "SYNTHETIC_REPORT="+filepath.Join(dir, "report.json"), "SYNTHETIC_RELEASE="+release),
	}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.WriteFile(release, nil, 0600)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := game.Wait(ctx); err != nil {
			t.Errorf("reap synthetic successor: %v", err)
		}
		if err := game.Close(); err != nil {
			t.Errorf("close synthetic successor: %v", err)
		}
	})

	getCount := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessHandleCount")
	count := func() uint32 {
		var n uint32
		ok, _, err := getCount.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&n)))
		if ok == 0 {
			t.Fatal(err)
		}
		return n
	}
	before := count()
	handle, identity, found, err := findGameProcess(uint32(os.Getpid()), 0, exe)
	if err != nil || !found {
		t.Fatalf("find synthetic successor: found=%v err=%v", found, err)
	}
	defer func() {
		if handle != 0 {
			_ = windows.CloseHandle(handle)
		}
	}()
	if after := count(); after <= before {
		t.Fatalf("finder did not retain the verified process handle: before=%d after=%d", before, after)
	}
	if identity != game.Identity() {
		t.Fatalf("finder identity %+v differs from created successor %+v", identity, game.Identity())
	}
	pid, err := windows.GetProcessId(handle)
	if err != nil || pid != identity.PID {
		t.Fatalf("retained handle pid=%d err=%v, want %d", pid, err, identity.PID)
	}
	assertCreation := func() {
		t.Helper()
		var creation, exit, kernel, user windows.Filetime
		if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
			t.Fatal(err)
		}
		if created := uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime); created != identity.CreationTime {
			t.Fatalf("retained handle creation=%d, want %d", created, identity.CreationTime)
		}
	}
	assertCreation()
	adopted := &Game{handle: handle, identity: identity, image: exe}
	if err := adopted.Close(); !errors.Is(err, ErrGameRunning) {
		t.Fatalf("retained successor must remain open while running: %v", err)
	}
	if err := os.WriteFile(release, nil, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if code, err := adopted.Wait(ctx); err != nil || code != 23 {
		t.Fatalf("wait using retained handle: exit=%d err=%v", code, err)
	}
	assertCreation() // The owned process object remains queryable after exit.
	if err := adopted.Close(); err != nil {
		t.Fatal(err)
	}
	closedHandle := handle
	handle = 0
	if _, err := windows.WaitForSingleObject(closedHandle, 0); !errors.Is(err, windows.ERROR_INVALID_HANDLE) {
		t.Fatalf("Close did not release the retained handle: %v", err)
	}
}
