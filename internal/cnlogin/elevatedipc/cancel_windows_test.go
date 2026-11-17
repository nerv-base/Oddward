// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package elevatedipc

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestCancelDuringLaunchClosesListenerBeforeLateChild(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered := make(chan string, 1)
	release := make(chan struct{})
	launchReturned := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		c, err := start(ctx, func(exe string, args []string) (windows.Handle, error) {
			entered <- args[1]
			<-release // Simulate an uncancellable UAC prompt.
			h, err := childLaunch(t, "reject")(exe, args)
			close(launchReturned)
			return h, err
		}, false)
		if c != nil {
			c.Close()
		}
		result <- err
	}()
	name := <-entered
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("pending launch: %v", err)
		}
	case <-time.After(time.Second):
		close(release)
		<-launchReturned
		t.Fatal("Start waited for the UAC result")
	}
	// The listener must already be gone before the delayed launch is allowed.
	c, err := dial(context.Background(), name)
	if c != nil {
		c.Close()
	}
	close(release)
	<-launchReturned
	if err == nil {
		t.Fatal("canceled listener remained open")
	}
}

func TestCancelPendingServerConnect(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		c, err := start(ctx, func(string, []string) (windows.Handle, error) {
			var h windows.Handle
			err := windows.DuplicateHandle(windows.CurrentProcess(), windows.CurrentProcess(), windows.CurrentProcess(), &h, 0, false, windows.DUPLICATE_SAME_ACCESS)
			close(entered)
			return h, err
		}, false)
		if c != nil {
			c.Close()
		}
		result <- err
	}()
	<-entered
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("pending connect: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ConnectNamedPipe did not cancel")
	}
}

func TestCloseUnblocksPendingReadAndWrite(t *testing.T) {
	for _, write := range []bool{false, true} {
		name := "read"
		if write {
			name = "write"
		}
		t.Run(name, func(t *testing.T) {
			server, args, _ := testListener(t)
			client, err := dial(context.Background(), args[0])
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			if err := server.connect(); err != nil {
				t.Fatal(err)
			}
			result := make(chan error, 1)
			go func() {
				var err error
				if write {
					_, err = server.Write(make([]byte, 1<<20))
				} else {
					_, err = server.Read(make([]byte, 1))
				}
				result <- err
			}()
			// A real pipe with no reader/writer keeps the operation pending.
			select {
			case err := <-result:
				t.Fatalf("operation did not block: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			server.Close()
			select {
			case err := <-result:
				if !errors.Is(err, io.ErrClosedPipe) {
					t.Fatalf("Close: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("Close did not unblock I/O")
			}
			server.Close()
			if _, err := server.Read([]byte{0}); !errors.Is(err, io.ErrClosedPipe) {
				t.Fatalf("read after close: %v", err)
			}
		})
	}
}

func TestBusyPipeDialHonorsContext(t *testing.T) {
	server, args, _ := testListener(t)
	first, err := dial(context.Background(), args[0])
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if err := server.connect(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	second, err := dial(ctx, args[0])
	if second != nil {
		second.Close()
		t.Fatal("second client admitted")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("busy dial: %v", err)
	}
}
