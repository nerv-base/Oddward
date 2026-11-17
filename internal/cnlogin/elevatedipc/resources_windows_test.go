// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package elevatedipc

import (
	"context"
	"io"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestRepeatedDuplexClosesOwnedHandles(t *testing.T) {
	getHandleCount := windows.NewLazySystemDLL("kernel32.dll").NewProc("GetProcessHandleCount")
	count := func() uint32 {
		var n uint32
		ok, _, _ := getHandleCount.Call(uintptr(windows.CurrentProcess()), uintptr(unsafe.Pointer(&n)))
		if ok == 0 {
			t.Fatal("cannot count process handles")
		}
		return n
	}
	// Warm up runtime wait infrastructure before measuring our handle ownership.
	cycle := func() {
		local, err := identify(windows.CurrentProcess())
		if err != nil {
			t.Fatal(err)
		}
		name := pipePrefix + "11111111111111111111111111111111"
		server, err := listen(name, local.sid)
		if err != nil {
			t.Fatal(err)
		}
		defer server.Close()
		client, err := dial(context.Background(), name)
		if err != nil {
			t.Fatal(err)
		}
		defer client.Close()
		if err := server.connect(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { _, err := client.Write([]byte("test")); done <- err }()
		var message [4]byte
		if _, err := io.ReadFull(server, message[:]); err != nil {
			t.Fatal(err)
		}
		if string(message[:]) != "test" {
			t.Fatal("stream corrupted")
		}
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	cycle()
	// First GC starts runtime worker threads (and their Windows handles).
	// Warm those before the baseline rather than attributing them to pipes.
	runtime.GC()
	before := count()
	for range 32 {
		cycle()
	}
	after := count()
	// Runtime can allocate a small number of thread/wait handles. A leaked pipe
	// or event per cycle would exceed this bound decisively.
	if after > before+8 {
		t.Fatalf("handles grew across 32 connections: %d -> %d", before, after)
	}
}
