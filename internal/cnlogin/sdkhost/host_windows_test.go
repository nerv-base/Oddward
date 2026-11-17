// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package sdkhost

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"testing"
	"time"
	"unsafe"

	"github.com/nerv-base/Oddward/internal/cnlogin/cnproto"
	"golang.org/x/sys/windows"
)

func testOptions(t *testing.T) Options {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return Options{Profile: cnproto.KnownProfiles()[0], HostExecutable: exe}
}

func testHost(t *testing.T) *Host {
	t.Helper()
	id := t.Name() + "." + time.Now().Format("150405.000000000")
	h, err := newHost(context.Background(), testOptions(t), `Local\Oddward.SdkHost.Test.Ready.`+id, `Local\Oddward.SdkHost.Test.Instance.`+id)
	if err != nil {
		t.Fatalf("create SDK host: %v", err)
	}
	t.Cleanup(func() {
		if err := h.Close(); err != nil {
			t.Error(err)
		}
	})
	return h
}

func TestNewCreatesExactPlatformObjects(t *testing.T) {
	h := testHost(t)
	if h.PipeName() == "" {
		t.Fatal("pipe name missing")
	}
	name, _ := windows.UTF16PtrFromString(h.env.MapName)
	// Open the named section independently, as an SDK client would.
	handle, err := openMapping(name)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	view, err := windows.MapViewOfFile(handle, windows.FILE_MAP_READ, 0, 0, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.UnmapViewOfFile(view)
	data := unsafe.Slice((*byte)(unsafe.Add(nil, view)), 1<<20)
	for off, want := range map[int]uint32{0: 1, 8: 1289, 0x716: 1} {
		if got := binary.LittleEndian.Uint32(data[off:]); got != want {
			t.Errorf("mapping[%#x]=%d want %d", off, got, want)
		}
	}
	if binary.LittleEndian.Uint16(data[12:]) != uint16(os.Getpid()) {
		t.Error("mapping host PID")
	}
	if readWide(data[14:14+520]) != h.contextName || readWide(data[0x316:0x716]) != "1289" {
		t.Error("mapping wide names")
	}
	if string(data[0x922:0x922+32]) != h.contextName || data[0x922+32] != 0 {
		t.Error("mapping context ASCII")
	}
	for _, check := range []struct {
		handle windows.Handle
		want   uint32
	}{{h.ready, windows.WAIT_OBJECT_0}, {h.contextEvent, uint32(windows.WAIT_TIMEOUT)}} {
		got, err := windows.WaitForSingleObject(check.handle, 0)
		if err != nil || got != check.want {
			t.Errorf("event state %d %v", got, err)
		}
	}
	if pid := windowPID(h.hwnd); pid != uint32(os.Getpid()) {
		t.Errorf("window owner %d", pid)
	}
	parent, _, _ := getParent.Call(h.hwnd)
	if parent != 0 {
		t.Error("host window is not top-level")
	}
	visible, _, _ := isWindowVisible.Call(h.hwnd)
	if visible != 0 {
		t.Error("host window visible")
	}
	var mode, instances uint32
	if err := windows.GetNamedPipeInfo(h.pipe, &mode, nil, nil, &instances); err != nil {
		t.Fatal(err)
	}
	if mode&windows.PIPE_TYPE_MESSAGE == 0 || instances != 1 {
		t.Errorf("pipe mode=%x instances=%d", mode, instances)
	}
}

func TestCloseCancelsUnboundHostAndJoinsWindow(t *testing.T) {
	h := testHost(t)
	hwnd := h.hwnd
	done := make(chan error, 1)
	go func() { done <- h.Close() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close deadlocked before BindGame")
	}
	if windowPID(hwnd) != 0 {
		t.Fatal("window survived Close")
	}
	if _, ok := <-h.Events(); ok {
		t.Fatal("events still open")
	}
	if err := h.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestInitializationContextDoesNotOwnSuccessfulHost(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	id := time.Now().Format("150405.000000000")
	h, err := newHost(ctx, testOptions(t), `Local\Oddward.SdkHost.Test.Ready.`+id, `Local\Oddward.SdkHost.Test.Instance.`+id)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	cancel()
	if windowPID(h.hwnd) != uint32(os.Getpid()) {
		t.Fatal("cancellation destroyed active dependency")
	}
	created, err := processCreation(uint32(os.Getpid()))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.BindGame(uint32(os.Getpid()), created); err != nil {
		t.Fatal(err)
	}
}

func TestNewHonorsAlreadyCancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	h, err := New(ctx, testOptions(t))
	if h != nil {
		defer h.Close()
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("New error %v", err)
	}
}

func TestNotificationsCoalesceUntilCompleted(t *testing.T) {
	h := testHost(t)
	for i := 0; i < 4; i++ {
		postMessage.Call(h.hwnd, uintptr(h.env.NotifyMessage), 1, 0)
	}
	select {
	case e := <-h.Events():
		if e.Kind != EventLoginRequested {
			t.Fatalf("event %v", e.Kind)
		}
	case <-time.After(time.Second):
		t.Fatal("missing login event")
	}
	// A synchronous WM_NULL ensures the window has processed every posted duplicate.
	var result uintptr
	sendMessageTimeout.Call(h.hwnd, 0, 0, 0, 0x23, 5000, uintptr(unsafe.Pointer(&result)))
	select {
	case e := <-h.Events():
		t.Fatalf("duplicate event %v", e.Kind)
	default:
	}
	h.CompleteLoginRequest()
	postMessage.Call(h.hwnd, uintptr(h.env.NotifyMessage), 14, 0)
	postMessage.Call(h.hwnd, uintptr(h.env.NotifyMessage), 4, 0)
	select {
	case e := <-h.Events():
		if e.Kind != EventLogout {
			t.Fatalf("event %v", e.Kind)
		}
	case <-time.After(time.Second):
		t.Fatal("missing logout event")
	}
	postMessage.Call(h.hwnd, uintptr(h.env.NotifyMessage), 1, 0)
	select {
	case e := <-h.Events():
		if e.Kind != EventLoginRequested {
			t.Fatalf("event %v", e.Kind)
		}
	case <-time.After(time.Second):
		t.Fatal("missing subsequent login event")
	}
}
