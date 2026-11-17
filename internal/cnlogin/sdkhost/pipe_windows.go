// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package sdkhost

import (
	"errors"
	"fmt"
	"os"

	"github.com/nerv-base/Oddward/internal/cnlogin/cnproto"
	"golang.org/x/sys/windows"
)

// errServeStopped reports that Close began while a pipe operation was in
// flight. It is control flow for serve, never a protocol failure.
var errServeStopped = errors.New("sdkhost: serve stopped")

// serve accepts game connections until Close. Every blocking wait includes the
// host stop event, so Close can cancel a pending overlapped operation and join
// this goroutine before any handle is released.
func (h *Host) serve() {
	defer h.workers.Done()
	select {
	case <-h.done:
		return
	case <-h.bound:
	}
	ioEvent, err := windows.CreateEvent(nil, 0, 0, nil)
	if err != nil {
		h.fail(fmt.Errorf("sdkhost: create pipe event: %w", err))
		return
	}
	defer windows.CloseHandle(ioEvent)
	buf := make([]byte, cnproto.MaxFrameSize)
	defer clear(buf)
	for !h.stopping() {
		ov := new(windows.Overlapped)
		ov.HEvent = ioEvent
		err := windows.ConnectNamedPipe(h.pipe, ov)
		if errors.Is(err, windows.ERROR_IO_PENDING) {
			completed, _, waitErr := h.waitPipe(ioEvent, ov)
			if !completed {
				return
			}
			err = waitErr
		}
		switch {
		case err == nil, errors.Is(err, windows.ERROR_PIPE_CONNECTED):
			// A client is connected.
		case errors.Is(err, windows.ERROR_NO_DATA):
			// The previous client closed between instances; accept again.
			continue
		default:
			h.fail(fmt.Errorf("sdkhost: accept pipe client: %w", err))
			return
		}
		// Only the bound game process may talk to this host. A queued foreign
		// connection is dropped without reading a single frame from it.
		var pid uint32
		if err := windows.GetNamedPipeClientProcessId(h.pipe, &pid); err != nil || pid != h.gamePID {
			fmt.Fprintf(os.Stderr, "sdkhost debug: reject client pid=%d game=%d err=%v\n", pid, h.gamePID, err)
			windows.DisconnectNamedPipe(h.pipe)
			continue
		}
		fmt.Fprintf(os.Stderr, "sdkhost debug: accept client pid=%d\n", pid)
		h.serveClient(ioEvent, buf)
		if h.stopping() {
			return
		}
		windows.DisconnectNamedPipe(h.pipe)
	}
}

func (h *Host) stopping() bool {
	select {
	case <-h.done:
		return true
	default:
		return false
	}
}

// waitPipe waits for an overlapped operation to complete or Close to begin.
// When Close wins it cancels the operation and waits for the kernel to release
// the OVERLAPPED before the caller returns and its storage may be reused.
func (h *Host) waitPipe(ioEvent windows.Handle, ov *windows.Overlapped) (bool, uint32, error) {
	event, err := windows.WaitForMultipleObjects([]windows.Handle{ioEvent, h.stopEvent}, false, windows.INFINITE)
	if err != nil {
		return false, 0, fmt.Errorf("sdkhost: wait for pipe: %w", err)
	}
	switch event {
	case windows.WAIT_OBJECT_0:
		var n uint32
		err := windows.GetOverlappedResult(h.pipe, ov, &n, false)
		return true, n, err
	case windows.WAIT_OBJECT_0 + 1:
		_ = windows.CancelIoEx(h.pipe, ov)
		var n uint32
		_ = windows.GetOverlappedResult(h.pipe, ov, &n, true)
		return false, 0, nil
	default:
		return false, 0, errors.New("sdkhost: pipe wait returned an unexpected state")
	}
}

// serveClient reads framed messages from one verified connection. A protocol
// error is reported as EventFailure and drops the connection; the host and its
// window survive so the game's dependencies remain available.
func (h *Host) serveClient(ioEvent windows.Handle, buf []byte) {
	defer clear(buf)
	for !h.stopping() {
		var n uint32
		ov := new(windows.Overlapped)
		ov.HEvent = ioEvent
		err := windows.ReadFile(h.pipe, buf, &n, ov)
		if errors.Is(err, windows.ERROR_IO_PENDING) {
			completed, read, waitErr := h.waitPipe(ioEvent, ov)
			if !completed {
				return
			}
			n, err = read, waitErr
		}
		switch {
		case err == nil:
			// A complete message is in buf[:n].
		case errors.Is(err, windows.ERROR_MORE_DATA):
			h.fail(errors.New("sdkhost: SDK frame size bound"))
			return
		case errors.Is(err, windows.ERROR_BROKEN_PIPE),
			errors.Is(err, windows.ERROR_PIPE_NOT_CONNECTED),
			errors.Is(err, windows.ERROR_NO_DATA),
			errors.Is(err, windows.ERROR_OPERATION_ABORTED):
			return
		default:
			h.fail(fmt.Errorf("sdkhost: read pipe: %w", err))
			return
		}
		if n == 0 {
			// A zero-length message is the client's orderly end of connection.
			return
		}
		if err := h.dispatchFrame(ioEvent, buf[:n]); err != nil {
			if !errors.Is(err, errServeStopped) {
				h.fail(err)
			}
			return
		}
		clear(buf)
	}
}

func (h *Host) writeReply(ioEvent windows.Handle, packet []byte) error {
	var n uint32
	ov := new(windows.Overlapped)
	ov.HEvent = ioEvent
	err := windows.WriteFile(h.pipe, packet, &n, ov)
	if errors.Is(err, windows.ERROR_IO_PENDING) {
		completed, written, waitErr := h.waitPipe(ioEvent, ov)
		if !completed {
			return errServeStopped
		}
		n, err = written, waitErr
	}
	if err != nil {
		return fmt.Errorf("sdkhost: write pipe: %w", err)
	}
	if int(n) != len(packet) {
		return fmt.Errorf("sdkhost: short pipe write: %d of %d bytes", n, len(packet))
	}
	return nil
}

// dispatchFrame validates one message and hands accepted events to the event
// pump. Known operations are answered in order; valid but unsupported ops are
// ignored exactly as the platform does.
func (h *Host) dispatchFrame(ioEvent windows.Handle, frame []byte) error {
	header, err := cnproto.ParseHeader(frame)
	if err != nil {
		return fmt.Errorf("sdkhost: invalid SDK frame: %w", err)
	}
	fmt.Fprintf(os.Stderr, "sdkhost debug: frame op=%d len=%d\n", header.Op, len(frame))
	switch header.Op {
	case cnproto.OpEnvInit:
		init, err := cnproto.ParseEnvInit(frame, h.gamePID)
		if err != nil {
			return fmt.Errorf("sdkhost: invalid environment request: %w", err)
		}
		// ArcGameAppAbbr echoes the abbreviation from the game's request
		// (CN expects "1289"), not a host-side constant.
		h.env.Abbreviation = init.Abbreviation
		reply, err := cnproto.BuildEnvReply(h.env)
		if err != nil {
			return fmt.Errorf("sdkhost: build environment reply: %w", err)
		}
		defer clear(reply)
		return h.writeReply(ioEvent, reply)
	case cnproto.OpRegister:
		h.mu.Lock()
		registered := h.callback != 0
		h.mu.Unlock()
		if registered {
			return nil
		}
		registration, err := cnproto.ParseRegistration(frame, h.gamePID, h.opts.Profile)
		if err != nil {
			return fmt.Errorf("sdkhost: invalid registration: %w", err)
		}
		h.mu.Lock()
		h.callback = uintptr(registration.CallbackWindow)
		h.mu.Unlock()
		h.emit(Event{Kind: EventRegistration, Registration: registration})
		return nil
	case cnproto.OpQuery:
		h.mu.Lock()
		registered := h.callback != 0
		h.mu.Unlock()
		if !registered {
			return nil
		}
		query, err := cnproto.ParseQuery(frame, h.gamePID)
		if err != nil {
			return fmt.Errorf("sdkhost: invalid query: %w", err)
		}
		h.emit(Event{Kind: EventQuery, Query: query})
		return nil
	default:
		return nil
	}
}
