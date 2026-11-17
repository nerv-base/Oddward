// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

// Package elevatedipc authenticates the private byte stream between the UI and
// the elevated SDK host. Application framing belongs to the caller.
package elevatedipc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Mode is the same-executable internal entry point, before the UI is initialized.
const Mode = "--cnlogin-host"

const pipePrefix = `\\.\pipe\Oddward.CNLogin.`

var (
	errTransport = errors.New("elevatedipc: transport failed")
	errIdentity  = errors.New("elevatedipc: peer identity rejected")
	errArguments = errors.New("elevatedipc: invalid host arguments")
	errElevation = errors.New("elevatedipc: elevation required")
	errLaunch    = errors.New("elevatedipc: elevated host launch failed")
)

// Start creates a private server and invokes this executable with runas and
// Mode, pipe name, parent PID and raw creation FILETIME. Both endpoints verify
// OS process identities before exchanging readiness bytes. ctx only controls
// setup; the returned connection must be closed by its owner. Closing it never
// terminates a process. No application request is sent by this package.
func Start(ctx context.Context) (io.ReadWriteCloser, error) {
	return start(ctx, shellLaunch, true)
}

// Accept consumes exactly the three arguments after Mode in the elevated host.
// It verifies elevation and the server's PID, creation time, executable, user
// SID and session. ctx only controls setup, as for Start.
func Accept(ctx context.Context, args []string) (io.ReadWriteCloser, error) {
	return accept(ctx, args, true)
}

type launchFunc func(string, []string) (windows.Handle, error)

func start(ctx context.Context, launch launchFunc, elevated bool) (io.ReadWriteCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	local, err := identify(windows.CurrentProcess())
	if err != nil {
		return nil, err
	}
	exe, err := os.Executable()
	if err != nil {
		return nil, errLaunch
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, errTransport
	}
	name := pipePrefix + hex.EncodeToString(random[:])
	c, err := listen(name, local.sid)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { c.Close() })
	delivered := false
	defer func() {
		stop()
		if !delivered {
			c.Close()
		}
	}()
	args := []string{Mode, name, strconv.FormatUint(uint64(local.pid), 10), strconv.FormatUint(local.creation, 10)}
	type result struct {
		h   windows.Handle
		err error
	}
	launched := make(chan result)
	// ShellExecuteEx may remain inside a UAC prompt after cancellation. Its
	// late result owns its handle until handed off, and never sends a request.
	go func() {
		if ctx.Err() != nil {
			return
		}
		h, err := launch(exe, args)
		select {
		case launched <- result{h, err}:
		case <-ctx.Done():
			if h != 0 {
				windows.CloseHandle(h)
			}
		}
	}()
	var child windows.Handle
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-launched:
		child = r.h
		if child != 0 {
			defer windows.CloseHandle(child)
		}
		if r.err != nil || child == 0 {
			return nil, errLaunch
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	expected, err := identify(child)
	if err != nil || !samePrincipal(expected, local) {
		return nil, errIdentity
	}
	if elevated && !expected.elevated {
		return nil, errElevation
	}
	if err := c.connect(); err != nil {
		return nil, setupError(ctx, err)
	}
	if err := verifyPeer(c, expected, local, false); err != nil {
		return nil, setupError(ctx, err)
	}
	if _, err := c.Write([]byte{1}); err != nil {
		return nil, setupError(ctx, err)
	}
	var ack [1]byte
	if _, err := io.ReadFull(c, ack[:]); err != nil {
		return nil, setupError(ctx, err)
	}
	if ack[0] != 1 {
		return nil, errIdentity
	}
	if !stop() || ctx.Err() != nil {
		return nil, ctx.Err()
	}
	delivered = true
	return c, nil
}

func accept(ctx context.Context, args []string, elevated bool) (io.ReadWriteCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	name, pid, creation, err := hostArguments(args)
	if err != nil {
		return nil, err
	}
	local, err := identify(windows.CurrentProcess())
	if err != nil {
		return nil, err
	}
	if elevated && !local.elevated {
		return nil, errElevation
	}
	c, err := dial(ctx, name)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { c.Close() })
	delivered := false
	defer func() {
		stop()
		if !delivered {
			c.Close()
		}
	}()
	if err := verifyPeer(c, identity{pid: pid, creation: creation}, local, true); err != nil {
		return nil, setupError(ctx, err)
	}
	var ready [1]byte
	if _, err := io.ReadFull(c, ready[:]); err != nil {
		return nil, setupError(ctx, err)
	}
	if ready[0] != 1 {
		return nil, errIdentity
	}
	if _, err := c.Write([]byte{1}); err != nil {
		return nil, setupError(ctx, err)
	}
	if !stop() || ctx.Err() != nil {
		return nil, ctx.Err()
	}
	delivered = true
	return c, nil
}

func setupError(ctx context.Context, err error) error {
	if canceled := ctx.Err(); canceled != nil {
		return canceled
	}
	return err
}

func hostArguments(args []string) (string, uint32, uint64, error) {
	if len(args) != 3 || len(args[0]) != len(pipePrefix)+32 || !strings.HasPrefix(args[0], pipePrefix) {
		return "", 0, 0, errArguments
	}
	if _, err := hex.DecodeString(args[0][len(pipePrefix):]); err != nil {
		return "", 0, 0, errArguments
	}
	pid, err := strconv.ParseUint(args[1], 10, 32)
	if err != nil || pid == 0 {
		return "", 0, 0, errArguments
	}
	creation, err := strconv.ParseUint(args[2], 10, 64)
	if err != nil || creation == 0 {
		return "", 0, 0, errArguments
	}
	return args[0], uint32(pid), creation, nil
}

func listen(name, sid string) (*conn, error) {
	// Protected DACL grants the current account only; there is no Everyone or
	// Administrators ACE. MAX_INSTANCES=1 and FIRST_INSTANCE reject squatting.
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;GA;;;" + sid + ")")
	if err != nil {
		return nil, errTransport
	}
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	h, err := windows.CreateNamedPipe(windows.StringToUTF16Ptr(name), windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED|windows.FILE_FLAG_FIRST_PIPE_INSTANCE, windows.PIPE_TYPE_BYTE|windows.PIPE_READMODE_BYTE|windows.PIPE_WAIT|windows.PIPE_REJECT_REMOTE_CLIENTS, 1, 4096, 4096, 0, &sa)
	if err != nil {
		return nil, errTransport
	}
	return newConn(h)
}

func dial(ctx context.Context, name string) (*conn, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// Identification level prevents a malicious pipe server impersonating
		// the elevated client token, including before identity is verified.
		h, err := windows.CreateFile(windows.StringToUTF16Ptr(name), windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED|windows.SECURITY_SQOS_PRESENT|windows.SECURITY_IDENTIFICATION, 0)
		if err == nil {
			return newConn(h)
		}
		if !errors.Is(err, windows.ERROR_PIPE_BUSY) {
			return nil, errTransport
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
