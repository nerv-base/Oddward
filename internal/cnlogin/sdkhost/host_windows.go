// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

// Package sdkhost owns the CN SDK's local Windows IPC resources. It does not
// launch processes, authenticate accounts, or make network requests.
package sdkhost

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nerv-base/Oddward/internal/cnlogin/cnproto"
	"golang.org/x/sys/windows"
)

// Options supplies the verified protocol profile and the existing host image.
// Compatibility hashing and public install metadata validation belong to launch.
type Options struct {
	Profile        cnproto.Profile
	HostExecutable string
}

// EventKind identifies a host event. EventFailure never tears down the host:
// its owner must keep the game's dependencies alive until the game exits.
type EventKind uint8

const (
	EventRegistration EventKind = iota + 1
	EventQuery
	EventLoginRequested
	EventLogout
	EventFailure
)

// EventRegistration transfers Registration ownership to the receiver, which
// must call Zero after deriving its keys. No key material is retained by Host.
type Event struct {
	Kind         EventKind
	Registration *cnproto.Registration
	Query        cnproto.Query
	Err          error
}

var (
	ErrClosed        = errors.New("sdkhost: closed")
	ErrIdentity      = errors.New("sdkhost: game process identity mismatch")
	ErrNotRegistered = errors.New("sdkhost: client not registered")
	ErrAlreadyExists = errors.New("sdkhost: platform object already exists")
)

// Host has an explicit lifetime. New's context only bounds initialization.
// Once a game starts, the caller must call Close only after its handle signals
// exit; cancellation of a UI/authentication context must not destroy Host.
type Host struct {
	opts                                                              Options
	env                                                               cnproto.EnvValues
	pipeName, contextName                                             string
	ready, contextEvent, pipe, mapping, mapMutex, instance, stopEvent windows.Handle
	view                                                              uintptr
	hwnd                                                              uintptr
	windowStarted                                                     bool
	windowDone                                                        chan struct{}
	done, bound                                                       chan struct{}
	events                                                            chan Event
	incoming                                                          chan Event
	failures                                                          chan error
	workers                                                           sync.WaitGroup
	closeOnce                                                         sync.Once
	closeErr                                                          error
	loginPending                                                      atomic.Bool
	mu                                                                sync.Mutex
	closed                                                            bool
	game                                                              windows.Handle
	gamePID                                                           uint32
	creation                                                          uint64
	callback                                                          uintptr
	sendMu                                                            sync.Mutex
}

// New creates a fresh private pipe, platform context and hidden top-level
// window. Production names cannot be overridden through Options.
func New(ctx context.Context, opts Options) (*Host, error) {
	return newHost(ctx, opts, `Global\CoreClient_StartEvent_1289`, `Local\Collapse.NTE.LoginHost.1289`)
}

// Alternate singleton names are package-private so synthetic tests never
// collide with a real platform readiness event.
func newHost(ctx context.Context, opts Options, readyName, instanceName string) (host *Host, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p, err := cnproto.ResolveProfile(opts.Profile.LauncherSHA256, opts.Profile.BridgeSHA256)
	if err != nil || p.SDKVersion != opts.Profile.SDKVersion {
		return nil, cnproto.ErrUnsupportedSDK
	}
	if !filepath.IsAbs(opts.HostExecutable) {
		return nil, errors.New("sdkhost: host executable must be absolute")
	}
	stat, err := os.Stat(opts.HostExecutable)
	if err != nil || !stat.Mode().IsRegular() {
		return nil, errors.New("sdkhost: host executable unavailable")
	}
	id, err := randomID()
	if err != nil {
		return nil, err
	}
	contextID, err := randomID()
	if err != nil {
		return nil, err
	}
	h := &Host{opts: opts, pipeName: "Collapse.NTE.Login." + id, contextName: contextID, done: make(chan struct{}), bound: make(chan struct{}), windowDone: make(chan struct{}), events: make(chan Event), incoming: make(chan Event, 32), failures: make(chan error, 1)}
	h.env = cnproto.EnvValues{HostPID: uint32(os.Getpid()), Abbreviation: cnproto.AppIDString, MapName: `Local\Collapse.NTE.SdkProbe.Map.` + id, MutexName: `Local\Collapse.NTE.SdkProbe.Mutex.` + id, PlatformExecutables: [3]string{opts.HostExecutable, opts.HostExecutable, opts.HostExecutable}}
	h.workers.Add(1)
	go h.deliverEvents()
	defer func() {
		if err != nil {
			h.Close()
		}
	}()
	if err = h.createObjects(readyName, instanceName); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	ready := make(chan error, 1)
	h.windowStarted = true
	go h.windowLoop(id, ready)
	startCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	select {
	case err = <-ready:
	case <-startCtx.Done():
		err = startCtx.Err()
		// The creating OS thread must finish before Close can post to its HWND.
		<-ready
	}
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	// Reject oversized host paths before a game can be started.
	packet, err := cnproto.BuildEnvReply(h.env)
	clear(packet)
	if err != nil {
		return nil, err
	}
	h.workers.Add(1)
	go h.serve()
	return h, nil
}

func randomID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", errors.New("sdkhost: random identifier unavailable")
	}
	return hex.EncodeToString(b[:]), nil
}

// PipeName is the ArcGameP value, without the Win32 pipe path prefix.
func (h *Host) PipeName() string { return h.pipeName }

// ConnectedClientPID observes connection metadata without reading a frame or
// authorizing the peer. BindGame still validates the exact process identity.
func (h *Host) ConnectedClientPID() uint32 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return 0
	}
	var pid uint32
	if err := windows.GetNamedPipeClientProcessId(h.pipe, &pid); err != nil {
		return 0
	}
	return pid
}

// BindGame verifies the exact PID and raw FILETIME read from the caller's
// original CreateProcess handle. The caller retains that original handle.
// Host also holds a query/synchronize handle to prevent identity recycling.
func (h *Host) BindGame(pid uint32, createdAt uint64) error {
	if pid == 0 || createdAt == 0 {
		return ErrIdentity
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return ErrClosed
	}
	if h.game != 0 {
		return ErrIdentity
	}
	game, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return ErrIdentity
	}
	creation, err := creationTime(game)
	state, waitErr := windows.WaitForSingleObject(game, 0)
	if err != nil || waitErr != nil || creation != createdAt || state != uint32(windows.WAIT_TIMEOUT) {
		windows.CloseHandle(game)
		return ErrIdentity
	}
	h.game, h.gamePID, h.creation = game, pid, creation
	close(h.bound)
	return nil
}

func creationTime(handle windows.Handle) (uint64, error) {
	var c, e, k, u windows.Filetime
	err := windows.GetProcessTimes(handle, &c, &e, &k, &u)
	return uint64(c.HighDateTime)<<32 | uint64(c.LowDateTime), err
}

// Events closes only when Close has joined all event producers.
func (h *Host) Events() <-chan Event { return h.events }

func (h *Host) deliverEvents() {
	defer h.workers.Done()
	defer close(h.events)
	defer func() {
		for {
			select {
			case e := <-h.incoming:
				if e.Registration != nil {
					e.Registration.Zero()
				}
			default:
				return
			}
		}
	}()
	for {
		var e Event
		select {
		case <-h.done:
			return
		case err := <-h.failures:
			e = Event{Kind: EventFailure, Err: err}
		case e = <-h.incoming:
		}
		select {
		case h.events <- e:
		case <-h.done:
			if e.Registration != nil {
				e.Registration.Zero()
			}
			return
		}
	}
}

func (h *Host) emit(e Event) bool {
	select {
	case h.incoming <- e:
		return true
	case <-h.done:
		if e.Registration != nil {
			e.Registration.Zero()
		}
		return false
	}
}
func (h *Host) fail(err error) {
	select {
	case h.failures <- err:
	default:
	}
}
func (h *Host) notify(command uintptr) {
	kind := EventKind(0)
	switch command {
	case cnproto.NotifyLogin:
		if !h.loginPending.CompareAndSwap(false, true) {
			return
		}
		kind = EventLoginRequested
	case cnproto.NotifyLogout:
		kind = EventLogout
	default:
		return
	}
	// Window procedures cannot wait for consumers: a consumer may itself be
	// inside SendMessageTimeout. Report bounded-queue overflow explicitly.
	select {
	case h.incoming <- Event{Kind: kind}:
	default:
		h.fail(errors.New("sdkhost: notification queue full"))
	}
}

// CompleteLoginRequest releases the coalescing latch, including after denial
// or cancellation. It is posted through the window queue so every login
// notification posted before this call is dispatched (and coalesced) before
// the latch opens; a later notification still requests a fresh login.
func (h *Host) CompleteLoginRequest() {
	h.mu.Lock()
	closed, hwnd := h.closed, h.hwnd
	h.mu.Unlock()
	if closed || hwnd == 0 {
		h.loginPending.Store(false)
		return
	}
	if posted, _, _ := winPostMessage.Call(hwnd, wmLoginComplete, 0, 0); posted == 0 {
		h.loginPending.Store(false)
	}
}

// SendLogin delivers the two login callback packets to the registered game
// window in order. Packet buffers are wiped after the synchronous send.
func (h *Host) SendLogin(ctx context.Context, id cnproto.LoginIdentity) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	extra, login, err := cnproto.BuildLoginPackets(id, h.env.HostPID)
	if err != nil {
		return err
	}
	defer clear(extra)
	defer clear(login)
	h.sendMu.Lock()
	defer h.sendMu.Unlock()
	if err := h.sendPacket(ctx.Err(), extra); err != nil {
		return err
	}
	return h.sendPacket(ctx.Err(), login)
}

// Reply delivers one pre-login reply packet to the registered game window.
func (h *Host) Reply(ctx context.Context, r cnproto.PreLoginReply) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	packet, err := cnproto.BuildPreLoginReply(r, h.env.HostPID)
	if err != nil {
		return err
	}
	defer clear(packet)
	h.sendMu.Lock()
	defer h.sendMu.Unlock()
	return h.sendPacket(ctx.Err(), packet)
}

// Close cancels and drains pending overlapped I/O before freeing its storage,
// joins the window's OS thread, zeroes the mapping and closes every handle.
// It is safe to call repeatedly or concurrently.
func (h *Host) Close() error {
	h.closeOnce.Do(func() {
		h.mu.Lock()
		h.closed = true
		close(h.done)
		h.mu.Unlock()
		if h.stopEvent != 0 {
			windows.SetEvent(h.stopEvent)
		}
		h.workers.Wait()
		h.sendMu.Lock()
		defer h.sendMu.Unlock()
		if h.windowStarted {
			if h.hwnd != 0 {
				winPostMessage.Call(h.hwnd, wmClose, 0, 0)
			}
			<-h.windowDone
		}
		if h.view != 0 {
			clear(mappingBytes(h.view))
			h.closeErr = errors.Join(h.closeErr, windows.UnmapViewOfFile(h.view))
		}
		for _, handle := range []windows.Handle{h.pipe, h.mapping, h.mapMutex, h.ready, h.contextEvent, h.instance, h.game, h.stopEvent} {
			if handle != 0 && handle != windows.InvalidHandle {
				h.closeErr = errors.Join(h.closeErr, windows.CloseHandle(handle))
			}
		}
	})
	return h.closeErr
}

func objectError(kind string, err error) error {
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return fmt.Errorf("%w: %s", ErrAlreadyExists, kind)
	}
	return fmt.Errorf("sdkhost: create %s: %w", kind, err)
}
