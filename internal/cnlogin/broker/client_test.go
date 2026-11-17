// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package broker

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/cnlogin/cnproto"
	"github.com/nerv-base/Oddward/internal/cnlogin/sdkhost"
	"github.com/nerv-base/Oddward/internal/process"
)

func TestStartedRequiresFullProcessIdentity(t *testing.T) {
	for _, id := range []process.Identity{{PID: 123}, {CreationTime: 456}} {
		c := &Client{next: 1, pending: &pendingCall{id: 1, kind: "start", reply: make(chan error, 1)}}
		f, err := makeFrame("started", 1, startedPayload{Identity: id, InstalledVersion: "2.0.9"})
		if err != nil {
			t.Fatal(err)
		}
		accepted := c.receive(f)
		f.zero()
		if accepted || c.started {
			t.Fatal("incomplete process identity accepted")
		}
	}
}

func TestSendLoginRequiresAckWhileQueryIsDelivered(t *testing.T) {
	c, peer := startedClient(t)
	if c.Identity().CreationTime != 456 || c.InstalledVersion() != "2.0.9" {
		t.Fatal("high host metadata lost")
	}
	done := make(chan error, 1)
	go func() { done <- c.SendLogin(testContext(t), syntheticLogin()) }()
	request := getFrame(t, peer)
	if request.Kind != "sendlogin" {
		t.Fatal("wrong request")
	}
	select {
	case err := <-done:
		t.Fatalf("completed before host acknowledgement: %v", err)
	default:
	}
	sendFrame(t, peer, "query", 0, queryPayload{Type: cnproto.QueryPreLogin, ContextID: 71})
	select {
	case e := <-c.Events():
		if e.Kind != sdkhost.EventQuery || e.Query.ContextID != 71 {
			t.Fatal("query lost")
		}
	case <-time.After(time.Second):
		t.Fatal("query blocked behind pending ack")
	}
	sendFrame(t, peer, "reply", request.ID, replyPayload{})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	sendFrame(t, peer, "exited", 0, exitPayload{Code: 29})
	if code, err := c.Wait(testContext(t)); err != nil || code != 29 {
		t.Fatalf("real exit: %d %v", code, err)
	}
}

func TestCancelLoginKeepsObservationAndIgnoresLateAck(t *testing.T) {
	c, peer := startedClient(t)
	ctx, cancel := context.WithCancel(testContext(t))
	done := make(chan error, 1)
	go func() { done <- c.SendLogin(ctx, syntheticLogin()) }()
	request := getFrame(t, peer)
	cancel()
	control := getFrame(t, peer)
	if control.Kind != "cancel" || control.ID != request.ID {
		t.Fatal("cancel did not identify active request")
	}
	if err := <-done; !errors.Is(err, ErrUnobserved) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel result: %v", err)
	}
	sendFrame(t, peer, "reply", request.ID, replyPayload{})
	next := make(chan error, 1)
	go func() { next <- c.SendLogin(testContext(t), syntheticLogin()) }()
	request2 := getFrame(t, peer)
	select {
	case err := <-next:
		t.Fatalf("late ack completed new login: %v", err)
	default:
	}
	sendFrame(t, peer, "reply", request2.ID, replyPayload{})
	if err := <-next; err != nil {
		t.Fatal(err)
	}
	sendFrame(t, peer, "exited", 0, exitPayload{Code: 7})
	if code, err := c.Wait(testContext(t)); err != nil || code != 7 {
		t.Fatalf("cancellation lost game observation: %d %v", code, err)
	}
}

func TestStartSentWithoutResultIsUnobserved(t *testing.T) {
	a, b := net.Pipe()
	result := make(chan error, 1)
	go func() {
		c, err := startClient(testContext(t), a, `C:\synthetic`)
		if c != nil {
			_ = c.Close()
		}
		result <- err
	}()
	request := getFrame(t, b)
	if request.Kind != "start" {
		t.Fatal("start not sent")
	}
	_ = b.Close()
	if err := <-result; !errors.Is(err, ErrUnobserved) {
		t.Fatalf("unacknowledged start claimed known failure: %v", err)
	}
}

func TestEventQueueOverflowDetachesWithoutInventingExit(t *testing.T) {
	c, peer := startedClient(t)
	for i := 0; i < cap(c.events)+1; i++ {
		f, _ := makeFrame("login", 0, struct{}{})
		err := writeFrame(peer, f)
		f.zero()
		if err != nil {
			break
		}
	}
	if _, err := c.Wait(testContext(t)); !errors.Is(err, ErrUnobserved) {
		t.Fatalf("overflow: %v", err)
	}
}

func TestCanceledQueuedClientCallIsNeverSent(t *testing.T) {
	c, peer := startedClient(t)
	first := make(chan error, 1)
	go func() { first <- c.SendLogin(testContext(t), syntheticLogin()) }()
	request := getFrame(t, peer)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := c.SendLogin(ctx, syntheticLogin()); !errors.Is(err, context.Canceled) {
		t.Fatal("canceled queued call was sent")
	}
	sendFrame(t, peer, "reply", request.ID, replyPayload{})
	if err := <-first; err != nil {
		t.Fatal(err)
	}
}

type blockedPayload struct{ entered, release chan struct{} }

func (p blockedPayload) MarshalJSON() ([]byte, error) {
	close(p.entered)
	<-p.release
	return []byte(`{}`), nil
}
func TestCanceledBeforeWriteDoesNotConsumeRequestID(t *testing.T) {
	c, peer := startedClient(t)
	ctx, cancel := context.WithCancel(testContext(t))
	payload := blockedPayload{entered: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	go func() { done <- c.call(ctx, "completelogin", payload) }()
	<-payload.entered
	cancel()
	close(payload.release)
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("before-write cancel: %v", err)
	}
	next := make(chan struct{})
	go func() { c.CompleteLoginRequest(); close(next) }()
	f := getFrame(t, peer)
	if f.ID != 2 {
		t.Errorf("unsent request consumed ID: next=%d want=2", f.ID)
	}
	sendFrame(t, peer, "reply", f.ID, replyPayload{})
	<-next
}

func TestClientEOFIsNotGameExit(t *testing.T) {
	local, remote := net.Pipe()
	c := newClient(local)
	defer c.Close()
	_ = remote.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if code, err := c.Wait(ctx); code != 0 || !errors.Is(err, ErrUnobserved) {
		t.Fatalf("EOF forged exit: code=%d err=%v", code, err)
	}
}
