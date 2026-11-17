// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

// Package broker transfers a narrowly scoped SDK host over an authenticated
// elevatedipc connection. Account storage, HTTP and login windows stay in the
// ordinary launcher process. A disconnected connection never proves game exit.
package broker

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/nerv-base/Oddward/internal/cnlogin/cnproto"
	"github.com/nerv-base/Oddward/internal/cnlogin/sdkhost"
	"github.com/nerv-base/Oddward/internal/process"
)

// ErrUnobserved means a request may have taken effect, or a game may still be
// running, but its result was not observed. Callers must not retry identities,
// commit account credentials, or manufacture game exit from this error.
var ErrUnobserved = errors.New("cnlogin/broker: operation result unobserved")

// Event carries decoded SDK metadata, never a raw SDK frame. Registration keys
// and query values are sensitive and are omitted from all formatted output.
type Event struct {
	Kind        sdkhost.EventKind
	ConfigKey   [16]byte
	PreLoginKey [16]byte
	Query       cnproto.Query
	Err         error
}

func (e Event) Format(s fmt.State, _ rune) { _, _ = io.WriteString(s, "broker event{redacted}") }

// Zero releases sensitive event values after the consumer has copied them.
func (e *Event) Zero() { clear(e.ConfigKey[:]); clear(e.PreLoginKey[:]); e.Query = cnproto.Query{} }

type pendingCall struct {
	id    uint64
	kind  string
	reply chan error
}

// Client observes one high-integrity SDK host and game. Close disconnects; it
// never terminates the game or releases its high-integrity host resources.
type Client struct {
	conn             io.ReadWriteCloser
	gate             chan struct{}
	events           chan Event
	done             chan struct{}
	readDone         chan struct{}
	once             sync.Once
	mu               sync.Mutex
	pending          *pendingCall
	next             uint64
	identity         process.Identity
	installedVersion string
	started          bool
	exited           bool
	exitCode         uint32
}

func newClient(conn io.ReadWriteCloser) *Client {
	c := &Client{conn: conn, gate: make(chan struct{}, 1), events: make(chan Event, 32), done: make(chan struct{}), readDone: make(chan struct{})}
	c.gate <- struct{}{}
	go c.readLoop()
	return c
}
func (c *Client) Identity() process.Identity { c.mu.Lock(); defer c.mu.Unlock(); return c.identity }

// InstalledVersion is the metadata rechecked by the elevated launch.Prepare.
func (c *Client) InstalledVersion() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.installedVersion
}
func (c *Client) Events() <-chan Event { return c.events }
func (c *Client) stop()                { c.once.Do(func() { close(c.done); _ = c.conn.Close() }) }
func (c *Client) Close() error {
	c.stop()
	<-c.readDone
	for e := range c.events {
		e.Zero()
	}
	return nil
}
func (c *Client) Wait(ctx context.Context) (uint32, error) {
	select {
	case <-c.done:
	case <-ctx.Done():
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.exited {
		return c.exitCode, nil
	}
	if err := ctx.Err(); err != nil {
		return 0, errors.Join(ErrUnobserved, err)
	}
	return 0, ErrUnobserved
}
func (c *Client) SendLogin(ctx context.Context, id cnproto.LoginIdentity) error {
	p := loginFrom(id)
	if !p.valid() {
		return errProtocol
	}
	return c.call(ctx, "sendlogin", p)
}
func (c *Client) Reply(ctx context.Context, r cnproto.PreLoginReply) error {
	p := preLoginPayload{Code: r.Code, ContextID: r.ContextID, Message: []byte(r.Message), Body: r.Body}
	defer clear(p.Message)
	if !p.valid() {
		return errProtocol
	}
	return c.call(ctx, "reply", p)
}
func (c *Client) CompleteLoginRequest() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = c.call(ctx, "completelogin", struct{}{})
}
func (c *Client) call(ctx context.Context, kind string, payload any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return ErrUnobserved
	case <-c.gate:
	}
	defer func() { c.gate <- struct{}{} }()
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case <-c.done:
		return ErrUnobserved
	default:
	}
	f, err := makeFrame(kind, 0, payload)
	if err != nil {
		return err
	}
	defer f.zero()
	if deadline, ok := ctx.Deadline(); ok {
		f.Deadline = deadline.UnixNano()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// Only commit an ID once encoding and the last pre-write cancellation check
	// succeed. An unsent request must not create a gap in the peer's sequence.
	c.mu.Lock()
	c.next++
	call := &pendingCall{id: c.next, kind: kind, reply: make(chan error, 1)}
	c.pending = call
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.pending == call {
			c.pending = nil
		}
		c.mu.Unlock()
	}()
	f.ID = call.id
	err = c.write(f)
	f.zero()
	if err != nil {
		return ErrUnobserved
	}
	select {
	case err := <-call.reply:
		return err
	case <-ctx.Done():
		if kind == "start" {
			c.stop()
		} else {
			cancelFrame, _ := makeFrame("cancel", call.id, struct{}{})
			_ = c.write(cancelFrame)
			cancelFrame.zero()
		}
		return errors.Join(ErrUnobserved, ctx.Err())
	case <-c.done:
		select {
		case err := <-call.reply:
			return err
		default:
			return ErrUnobserved
		}
	}
}

// Requests are serialized by gate. Cancellation after a complete frame sends a
// cancel(id) control frame without disrupting game observation. A stuck writer
// is a transport failure and must close to avoid corrupting frame boundaries.
func (c *Client) write(f wireFrame) error {
	timer := time.AfterFunc(10*time.Second, c.stop)
	defer timer.Stop()
	if err := writeFrame(c.conn, f); err != nil {
		c.stop()
		return ErrUnobserved
	}
	return nil
}
func (c *Client) readLoop() {
	defer close(c.readDone)
	defer close(c.events)
	defer c.stop()
	for {
		f, err := readFrame(c.conn)
		if err != nil {
			c.emit(Event{Kind: sdkhost.EventFailure, Err: ErrUnobserved})
			return
		}
		ok := c.receive(f)
		f.zero()
		if !ok {
			return
		}
	}
}
func (c *Client) emit(e Event) bool {
	defer e.Zero()
	select {
	case c.events <- e:
		return true
	default:
		e.Zero()
		return false
	}
}
func (c *Client) receive(f wireFrame) bool {
	if f.Deadline != 0 {
		return false
	}
	if f.Kind == "started" || f.Kind == "reply" {
		c.mu.Lock()
		defer c.mu.Unlock()
		p := c.pending
		if f.ID == 0 || f.ID > c.next {
			return false
		}
		if p == nil || f.ID != p.id {
			// A canceled request may acknowledge later; its result cannot complete a
			// newer request and can never make the caller commit credentials.
			var late replyPayload
			return f.Kind == "reply" && decode(f.Payload, &late) == nil && (late.ErrorCode == "" || late.ErrorCode == "failed" || late.ErrorCode == "canceled")
		}
		var result error
		if f.Kind == "started" {
			var v startedPayload
			if p.kind != "start" || c.started || decode(f.Payload, &v) != nil || v.Identity.PID == 0 || v.Identity.CreationTime == 0 {
				return false
			}
			c.identity = v.Identity
			c.installedVersion = v.InstalledVersion
			c.started = true
		} else {
			var v replyPayload
			if decode(f.Payload, &v) != nil {
				return false
			}
			switch v.ErrorCode {
			case "":
				if p.kind == "start" {
					return false
				}
			case "failed":
				result = errRemote
			case "canceled":
				result = context.Canceled
			default:
				return false
			}
		}
		select {
		case p.reply <- result:
			return true
		default:
			return false
		}
	}
	c.mu.Lock()
	started := c.started
	c.mu.Unlock()
	if !started || f.ID != 0 {
		return false
	}
	var event Event
	defer event.Zero()
	switch f.Kind {
	case "exited":
		var v exitPayload
		if decode(f.Payload, &v) != nil {
			return false
		}
		c.mu.Lock()
		c.exited = true
		c.exitCode = v.Code
		c.mu.Unlock()
		return false
	case "registration":
		var p registrationPayload
		defer func() { clear(p.ConfigKey); clear(p.PreLoginKey) }()
		if decode(f.Payload, &p) != nil || len(p.ConfigKey) != 16 || len(p.PreLoginKey) != 16 {
			return false
		}
		event.Kind = sdkhost.EventRegistration
		copy(event.ConfigKey[:], p.ConfigKey)
		copy(event.PreLoginKey[:], p.PreLoginKey)
	case "query":
		var p queryPayload
		if decode(f.Payload, &p) != nil {
			return false
		}
		event = Event{Kind: sdkhost.EventQuery, Query: cnproto.Query{Type: p.Type, ContextID: p.ContextID}}
	case "login", "logout":
		if decode(f.Payload, &struct{}{}) != nil {
			return false
		}
		event.Kind = sdkhost.EventLoginRequested
		if f.Kind == "logout" {
			event.Kind = sdkhost.EventLogout
		}
	case "failure":
		var p replyPayload
		if decode(f.Payload, &p) != nil || p.ErrorCode != "failed" {
			return false
		}
		event = Event{Kind: sdkhost.EventFailure, Err: errRemote}
	default:
		return false
	}
	return c.emit(event)
}
