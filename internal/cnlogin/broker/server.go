// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package broker

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/nerv-base/Oddward/internal/cnlogin/cnproto"
	"github.com/nerv-base/Oddward/internal/cnlogin/sdkhost"
	"github.com/nerv-base/Oddward/internal/process"
)

type host interface {
	Events() <-chan sdkhost.Event
	SendLogin(context.Context, cnproto.LoginIdentity) error
	Reply(context.Context, cnproto.PreLoginReply) error
	CompleteLoginRequest()
	Close() error
}
type game interface {
	Identity() process.Identity
	Wait(context.Context) (uint32, error)
	Close() error
}
type owned struct {
	host             host
	game             game
	installedVersion string
	bindOverlay      overlayBinder
}
type startOwned func(context.Context, string) (*owned, error)
type request struct {
	frame  wireFrame
	ctx    context.Context
	cancel context.CancelFunc
}
type outgoing struct {
	frame   wireFrame
	written chan struct{}
}

// link's independent reader handles cancellation even while a synchronous SDK
// callback is running. Both work queues and the cancellation map are bounded.
type link struct {
	conn                io.ReadWriteCloser
	ctx                 context.Context
	cancel              context.CancelFunc
	once                sync.Once
	requests            chan request
	outgoing            chan outgoing
	readDone, writeDone chan struct{}
	mu                  sync.Mutex
	calls               map[uint64]context.CancelFunc
}

func newLink(conn io.ReadWriteCloser) *link {
	ctx, cancel := context.WithCancel(context.Background())
	l := &link{conn: conn, ctx: ctx, cancel: cancel, requests: make(chan request, 32), outgoing: make(chan outgoing, 32), readDone: make(chan struct{}), writeDone: make(chan struct{}), calls: make(map[uint64]context.CancelFunc)}
	go l.read()
	go l.write()
	return l
}
func (l *link) close() { l.once.Do(func() { l.cancel(); _ = l.conn.Close() }) }
func (l *link) finish(r request) {
	r.cancel()
	r.frame.zero()
	l.mu.Lock()
	delete(l.calls, r.frame.ID)
	l.mu.Unlock()
}
func (l *link) shutdown() {
	l.close()
	<-l.readDone
	<-l.writeDone
	for {
		select {
		case r := <-l.requests:
			l.finish(r)
		default:
			goto outbound
		}
	}
outbound:
	for {
		select {
		case o := <-l.outgoing:
			o.frame.zero()
		default:
			return
		}
	}
}
func (l *link) read() {
	defer close(l.readDone)
	defer l.close()
	var last uint64
	for {
		f, err := readFrame(l.conn)
		if err != nil {
			return
		}
		if f.ID == 0 || f.Deadline < 0 {
			f.zero()
			return
		}
		if f.Kind == "cancel" {
			if f.ID > last || f.Deadline != 0 || decode(f.Payload, &struct{}{}) != nil {
				f.zero()
				return
			}
			l.mu.Lock()
			cancel := l.calls[f.ID]
			if cancel != nil {
				cancel()
			}
			l.mu.Unlock()
			f.zero()
			continue
		}
		if f.ID != last+1 || (last == 0 && f.Kind != "start") || (last != 0 && f.Kind == "start") {
			f.zero()
			return
		}
		switch f.Kind {
		case "start", "sendlogin", "reply", "completelogin", "bindoverlay":
		default:
			f.zero()
			return
		}
		last = f.ID
		var ctx context.Context
		var cancel context.CancelFunc
		if f.Deadline != 0 {
			ctx, cancel = context.WithDeadline(l.ctx, time.Unix(0, f.Deadline))
		} else {
			ctx, cancel = context.WithCancel(l.ctx)
		}
		r := request{frame: f, ctx: ctx, cancel: cancel}
		l.mu.Lock()
		full := len(l.calls) >= 32
		if !full {
			l.calls[f.ID] = cancel
		}
		l.mu.Unlock()
		if full {
			cancel()
			f.zero()
			return
		}
		select {
		case l.requests <- r:
		case <-l.ctx.Done():
			l.finish(r)
			return
		default:
			l.finish(r)
			return
		}
	}
}
func (l *link) write() {
	defer close(l.writeDone)
	defer l.close()
	for {
		select {
		case <-l.ctx.Done():
			return
		case o := <-l.outgoing:
			timer := time.AfterFunc(10*time.Second, l.close)
			err := writeFrame(l.conn, o.frame)
			timer.Stop()
			o.frame.zero()
			close(o.written)
			if err != nil {
				return
			}
		}
	}
}

// send transfers frame ownership to the writer. Queue overflow detaches rather
// than blocking SDK events or retaining an unbounded collection of secrets.
func (l *link) send(kind string, id uint64, payload any) <-chan struct{} {
	if l.ctx.Err() != nil {
		return l.ctx.Done()
	}
	f, err := makeFrame(kind, id, payload)
	if err != nil {
		l.close()
		return l.ctx.Done()
	}
	o := outgoing{frame: f, written: make(chan struct{})}
	select {
	case l.outgoing <- o:
		return o.written
	default:
		f.zero()
		l.close()
		return l.ctx.Done()
	}
}
func (l *link) sendFinal(kind string, id uint64, payload any) {
	written := l.send(kind, id, payload)
	select {
	case <-written:
	case <-l.ctx.Done():
	}
}
func errorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "canceled"
	}
	return "failed"
}

func serve(ctx context.Context, conn io.ReadWriteCloser, start startOwned) error {
	if err := ctx.Err(); err != nil {
		_ = conn.Close()
		return err
	}
	l := newLink(conn)
	defer l.shutdown()
	stopInit := context.AfterFunc(ctx, l.close)
	defer stopInit()
	var first request
	select {
	case first = <-l.requests:
	case <-l.ctx.Done():
		return ErrUnobserved
	}
	var p startPayload
	if first.frame.Kind != "start" || decode(first.frame.Payload, &p) != nil || !validRoot(p.Root) {
		l.finish(first)
		return errProtocol
	}
	var o *owned
	err := first.ctx.Err()
	if err == nil {
		err = ctx.Err()
	}
	if err == nil {
		o, err = start(first.ctx, p.Root)
	}
	id := first.frame.ID
	l.finish(first)
	if o == nil || o.game == nil {
		if err == nil {
			err = errRemote
		}
		l.sendFinal("reply", id, replyPayload{ErrorCode: errorCode(err)})
		return errRemote
	}
	// Past this point initialization cancellation and parent EOF must never
	// destroy host objects or the game handle before a successful Wait.
	stopInit()
	defer o.game.Close()
	defer o.host.Close()
	l.send("started", id, startedPayload{Identity: o.game.Identity(), InstalledVersion: o.installedVersion})
	if err != nil {
		l.send("failure", 0, replyPayload{ErrorCode: "failed"})
	}
	type result struct {
		code uint32
		err  error
	}
	waited := make(chan result, 1)
	go func() {
		for {
			code, err := o.game.Wait(context.Background())
			waited <- result{code, err}
			if err == nil {
				return
			}
			// A wait error does not prove exit. Keep the resources and retry observation.
			time.Sleep(250 * time.Millisecond)
		}
	}()
	events := o.host.Events()
	requests := l.requests
	disconnected := l.ctx.Done()
	for {
		select {
		case <-disconnected:
			// Discard queued secret-bearing frames now, not hours later when the
			// independently owned game exits. Host events continue to be drained.
			l.shutdown()
			disconnected = nil
			requests = nil
		case r := <-requests:
			err := execute(r, o)
			l.send("reply", r.frame.ID, replyPayload{ErrorCode: errorCode(err)})
			l.finish(r)
			if errors.Is(err, errProtocol) {
				l.close()
			}
		case e, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			forwardEvent(l, e)
		case result := <-waited:
			if result.err != nil {
				l.send("failure", 0, replyPayload{ErrorCode: "failed"})
				continue
			}
			l.sendFinal("exited", 0, exitPayload{Code: result.code})
			return nil
		}
	}
}
func execute(r request, o *owned) error {
	h := o.host
	if err := r.ctx.Err(); err != nil {
		return err
	}
	switch r.frame.Kind {
	case "bindoverlay":
		var p overlayPayload
		if decode(r.frame.Payload, &p) != nil || !p.valid() {
			return errProtocol
		}
		if err := r.ctx.Err(); err != nil {
			return err
		}
		if o.bindOverlay == nil {
			return errRemote
		}
		return o.bindOverlay(r.ctx, p)
	case "sendlogin":
		var p loginPayload
		defer func() { p.zero() }()
		if decode(r.frame.Payload, &p) != nil || !p.valid() {
			return errProtocol
		}
		clear(r.frame.Payload)
		if err := r.ctx.Err(); err != nil {
			return err
		}
		return h.SendLogin(r.ctx, p.identity())
	case "reply":
		var p preLoginPayload
		defer func() { p.zero() }()
		if decode(r.frame.Payload, &p) != nil || !p.valid() {
			return errProtocol
		}
		clear(r.frame.Payload)
		if err := r.ctx.Err(); err != nil {
			return err
		}
		return h.Reply(r.ctx, cnproto.PreLoginReply{Code: p.Code, ContextID: p.ContextID, Message: string(p.Message), Body: p.Body})
	case "completelogin":
		if decode(r.frame.Payload, &struct{}{}) != nil {
			return errProtocol
		}
		if err := r.ctx.Err(); err != nil {
			return err
		}
		h.CompleteLoginRequest()
		return nil
	default:
		return errProtocol
	}
}
func forwardEvent(l *link, e sdkhost.Event) {
	if e.Registration != nil {
		defer e.Registration.Zero()
	}
	if l.ctx.Err() != nil {
		return
	}
	switch e.Kind {
	case sdkhost.EventRegistration:
		if e.Registration == nil {
			l.send("failure", 0, replyPayload{ErrorCode: "failed"})
			return
		}
		config, err := e.Registration.AppKeyTail()
		defer clear(config[:])
		pre, preErr := e.Registration.PreLoginKey()
		defer clear(pre[:])
		e.Registration.Zero()
		if err != nil || preErr != nil {
			l.send("failure", 0, replyPayload{ErrorCode: "failed"})
			return
		}
		l.send("registration", 0, registrationPayload{ConfigKey: config[:], PreLoginKey: pre[:]})
	case sdkhost.EventQuery:
		l.send("query", 0, queryPayload{Type: e.Query.Type, ContextID: e.Query.ContextID})
	case sdkhost.EventLoginRequested:
		l.send("login", 0, struct{}{})
	case sdkhost.EventLogout:
		l.send("logout", 0, struct{}{})
	default:
		l.send("failure", 0, replyPayload{ErrorCode: "failed"})
	}
}
