//go:build windows

// SPDX-License-Identifier: GPL-3.0-or-later
package session

import (
	"context"
	"sync"

	"github.com/nerv-base/Oddward/internal/cnlogin/broker"
	"github.com/nerv-base/Oddward/internal/cnlogin/cnproto"
	"github.com/nerv-base/Oddward/internal/cnlogin/laohu"
	"github.com/nerv-base/Oddward/internal/cnlogin/sdkhost"
	"github.com/nerv-base/Oddward/internal/process"
)

type brokerHost interface {
	Identity() process.Identity
	Events() <-chan broker.Event
	SendLogin(context.Context, cnproto.LoginIdentity) error
	Reply(context.Context, cnproto.PreLoginReply) error
	BindOverlay(context.Context, process.Identity, uintptr, uintptr) error
	EmbedOverlay(context.Context, process.Identity, uintptr, uintptr) error
	CompleteLoginRequest()
	Close() error
}

type remoteHost struct {
	client   brokerHost
	events   chan HostEvent
	done     chan struct{}
	once     sync.Once
	closeErr error
}

func newBrokerHost(client brokerHost) *remoteHost {
	h := &remoteHost{client: client, events: make(chan HostEvent), done: make(chan struct{})}
	go h.forward()
	return h
}
func (h *remoteHost) forward() {
	defer close(h.events)
	for event := range h.client.Events() {
		out := HostEvent{Kind: hostKind(event.Kind), Query: event.Query, Err: event.Err}
		if event.Kind == sdkhost.EventRegistration {
			out.Key = laohu.NewDerivedAppKey(event.ConfigKey, event.PreLoginKey)
			out.ConfigKey = event.ConfigKey
		}
		event.Zero()
		select {
		case h.events <- out:
		case <-h.done:
			out.Key.Zero()
			clear(out.ConfigKey[:])
			return
		}
	}
}

// The SDK pipe is wholly owned by the elevated host; the session has no reason
// to connect to it or to change which process it authenticates.
func (h *remoteHost) PipeName() string { return "" }
func (h *remoteHost) BindGame(pid uint32, createdAt uint64) error {
	if id := h.client.Identity(); id.PID != pid || id.CreationTime != createdAt || pid == 0 || createdAt == 0 {
		return sdkhost.ErrIdentity
	}
	return nil
}
func (h *remoteHost) Events() <-chan HostEvent { return h.events }
func (h *remoteHost) SendLogin(ctx context.Context, id cnproto.LoginIdentity) error {
	return h.client.SendLogin(ctx, id)
}
func (h *remoteHost) Reply(ctx context.Context, r cnproto.PreLoginReply) error {
	return h.client.Reply(ctx, r)
}
func (h *remoteHost) BindOverlay(ctx context.Context, ui process.Identity, window, gameWindow uintptr) error {
	return h.client.BindOverlay(ctx, ui, window, gameWindow)
}
func (h *remoteHost) EmbedOverlay(ctx context.Context, ui process.Identity, window, gameWindow uintptr) error {
	return h.client.EmbedOverlay(ctx, ui, window, gameWindow)
}
func (h *remoteHost) CompleteLoginRequest() { h.client.CompleteLoginRequest() }
func (h *remoteHost) Close() error {
	h.once.Do(func() { close(h.done); h.closeErr = h.client.Close() })
	return h.closeErr
}
