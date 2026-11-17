// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package elevatedipc

import (
	"errors"
	"io"
	"runtime"
	"sync"

	"golang.org/x/sys/windows"
)

// conn owns only its pipe and events. Each outstanding OVERLAPPED is drained
// before Close releases handles or lets its buffer become unreachable.
type conn struct {
	h                           windows.Handle
	stop, readEvent, writeEvent windows.Handle
	mu                          sync.Mutex
	closed                      bool
	ops                         sync.WaitGroup
	readMu, writeMu             sync.Mutex
	closeOnce                   sync.Once
}

func newConn(h windows.Handle) (*conn, error) {
	c := &conn{h: h}
	for _, dst := range []*windows.Handle{&c.stop, &c.readEvent, &c.writeEvent} {
		event, err := windows.CreateEvent(nil, 1, 0, nil)
		if err != nil {
			for _, owned := range []windows.Handle{c.stop, c.readEvent, c.writeEvent, c.h} {
				if owned != 0 {
					windows.CloseHandle(owned)
				}
			}
			return nil, errTransport
		}
		*dst = event
	}
	return c, nil
}

func (c *conn) begin() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return io.ErrClosedPipe
	}
	c.ops.Add(1)
	return nil
}

func (c *conn) Close() error {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		windows.SetEvent(c.stop)
		c.mu.Unlock()
		c.ops.Wait()
		for _, h := range []windows.Handle{c.h, c.stop, c.readEvent, c.writeEvent} {
			windows.CloseHandle(h)
		}
	})
	return nil
}

func (c *conn) run(event windows.Handle, operation func(*windows.Overlapped, *uint32) error) (uint32, error) {
	if err := c.begin(); err != nil {
		return 0, err
	}
	defer c.ops.Done()
	if err := windows.ResetEvent(event); err != nil {
		return 0, errTransport
	}
	ov := &windows.Overlapped{HEvent: event}
	var n uint32
	err := operation(ov, &n)
	if errors.Is(err, windows.ERROR_IO_PENDING) {
		status, waitErr := windows.WaitForMultipleObjects([]windows.Handle{c.stop, event}, false, windows.INFINITE)
		if waitErr != nil || status != windows.WAIT_OBJECT_0+1 {
			windows.CancelIoEx(c.h, ov)
			windows.GetOverlappedResult(c.h, ov, &n, true)
			if waitErr != nil {
				return 0, errTransport
			}
			return 0, io.ErrClosedPipe
		}
		err = windows.GetOverlappedResult(c.h, ov, &n, false)
	}
	return n, err
}

func (c *conn) Read(p []byte) (int, error) {
	c.readMu.Lock()
	defer c.readMu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	n, err := c.run(c.readEvent, func(ov *windows.Overlapped, n *uint32) error { return windows.ReadFile(c.h, p, n, ov) })
	runtime.KeepAlive(p) // The kernel may retain p after ReadFile returns IO_PENDING.
	if errors.Is(err, windows.ERROR_BROKEN_PIPE) || errors.Is(err, windows.ERROR_PIPE_NOT_CONNECTED) || errors.Is(err, windows.ERROR_NO_DATA) {
		return int(n), io.EOF
	}
	if err != nil {
		if errors.Is(err, io.ErrClosedPipe) {
			return int(n), err
		}
		return int(n), errTransport
	}
	if n == 0 {
		return 0, io.EOF
	}
	return int(n), nil
}

func (c *conn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if len(p) == 0 {
		return 0, nil
	}
	n, err := c.run(c.writeEvent, func(ov *windows.Overlapped, n *uint32) error { return windows.WriteFile(c.h, p, n, ov) })
	runtime.KeepAlive(p)
	if err != nil {
		if errors.Is(err, io.ErrClosedPipe) {
			return int(n), err
		}
		return int(n), errTransport
	}
	if int(n) != len(p) {
		return int(n), io.ErrShortWrite
	}
	return int(n), nil
}

func (c *conn) connect() error {
	_, err := c.run(c.readEvent, func(ov *windows.Overlapped, _ *uint32) error { return windows.ConnectNamedPipe(c.h, ov) })
	if err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		return errTransport
	}
	return nil
}

func (c *conn) peerPID(server bool) (uint32, error) {
	if err := c.begin(); err != nil {
		return 0, err
	}
	defer c.ops.Done()
	var pid uint32
	var err error
	if server {
		err = windows.GetNamedPipeServerProcessId(c.h, &pid)
	} else {
		err = windows.GetNamedPipeClientProcessId(c.h, &pid)
	}
	if err != nil || pid == 0 {
		return 0, errIdentity
	}
	return pid, nil
}
