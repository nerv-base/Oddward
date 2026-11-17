//go:build windows

// SPDX-License-Identifier: GPL-3.0-or-later
package session

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/cnlogin/broker"
	"github.com/nerv-base/Oddward/internal/cnlogin/cnproto"
	"github.com/nerv-base/Oddward/internal/cnlogin/laohu"
	"github.com/nerv-base/Oddward/internal/cnlogin/sdkhost"
	"github.com/nerv-base/Oddward/internal/process"
)

type fakeBrokerHost struct {
	events            chan broker.Event
	identity          process.Identity
	sent              chan cnproto.LoginIdentity
	ack               chan error
	completed, closed int
	bind              func(context.Context, process.Identity, uintptr, uintptr) error
	embed             func(context.Context, process.Identity, uintptr, uintptr) error
}

func (f *fakeBrokerHost) Events() <-chan broker.Event { return f.events }
func (f *fakeBrokerHost) Identity() process.Identity  { return f.identity }
func (f *fakeBrokerHost) SendLogin(ctx context.Context, id cnproto.LoginIdentity) error {
	select {
	case f.sent <- id:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-f.ack:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}
func (f *fakeBrokerHost) Reply(context.Context, cnproto.PreLoginReply) error { return nil }
func (f *fakeBrokerHost) BindOverlay(ctx context.Context, ui process.Identity, window, gameWindow uintptr) error {
	return f.bind(ctx, ui, window, gameWindow)
}
func (f *fakeBrokerHost) EmbedOverlay(ctx context.Context, ui process.Identity, window, gameWindow uintptr) error {
	return f.embed(ctx, ui, window, gameWindow)
}
func (f *fakeBrokerHost) CompleteLoginRequest() { f.completed++ }
func (f *fakeBrokerHost) Close() error          { f.closed++; close(f.events); return nil }

func TestBrokerWindowBindingRetainsOwnedIdentityAndNativeACK(t *testing.T) {
	for _, embed := range []bool{false, true} {
		t.Run(map[bool]string{false: "owner", true: "child"}[embed], func(t *testing.T) { testBrokerWindowBinding(t, embed) })
	}
}

func testBrokerWindowBinding(t *testing.T, embed bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	called := make(chan struct{}, 1)
	ack := make(chan error)
	identity := process.Identity{PID: 4321, CreationTime: 998877}
	binding := func(ctx context.Context, ui process.Identity, window, gameWindow uintptr) error {
		if ui != identity || window != 23 || gameWindow != 42 {
			return errors.New("binding identity changed")
		}
		called <- struct{}{}
		select {
		case err := <-ack:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	fake := &fakeBrokerHost{events: make(chan broker.Event)}
	if embed {
		fake.embed = binding
	} else {
		fake.bind = binding
	}
	host := newBrokerHost(fake)
	defer host.Close()
	bind := host.BindOverlay
	if embed {
		bind = host.EmbedOverlay
	}
	result := make(chan error, 1)
	go func() { result <- bind(ctx, identity, 23, 42) }()
	select {
	case <-called:
	case <-result:
		t.Fatal("window binding succeeded without native request")
	case <-ctx.Done():
		t.Fatal("window binding was not forwarded")
	}
	select {
	case <-result:
		t.Fatal("window binding returned before native ACK")
	default:
	}
	want := errors.New("synthetic native ownership rejected")
	ack <- want
	if err := <-result; !errors.Is(err, want) {
		t.Fatal("native ownership error lost")
	}
}

func TestBrokerHostForwardsDistinctKeysAndOwnedIdentity(t *testing.T) {
	fake := &fakeBrokerHost{events: make(chan broker.Event, 2), identity: process.Identity{PID: 1234, CreationTime: 998877}}
	host := newBrokerHost(fake)
	defer host.Close()
	if err := host.BindGame(1234, 998876); !errors.Is(err, sdkhost.ErrIdentity) {
		t.Fatalf("accepted wrong creation time: %v", err)
	}
	if err := host.BindGame(1234, 998877); err != nil {
		t.Fatal(err)
	}
	var tail, pre [16]byte
	copy(tail[:], "abcdefghijklmnop")
	copy(pre[:], "0123456789ABCDEF")
	fake.events <- broker.Event{Kind: sdkhost.EventRegistration, ConfigKey: tail, PreLoginKey: pre}
	select {
	case event := <-host.Events():
		if event.Kind != HostRegistered || event.ConfigKey != tail || !reflect.DeepEqual(event.Key, laohu.NewDerivedAppKey(tail, pre)) {
			t.Fatal("registration keys were lost or conflated")
		}
		event.Key.Zero()
	case <-time.After(5 * time.Second):
		t.Fatal("registration not forwarded")
	}
	fake.events <- broker.Event{Kind: sdkhost.EventLoginRequested}
	select {
	case event := <-host.Events():
		if event.Kind != HostLoginRequested {
			t.Fatal("login notification lost")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("login notification not forwarded")
	}
	host.CompleteLoginRequest()
	if fake.completed != 1 {
		t.Fatal("request completion not forwarded")
	}
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	if err := host.Close(); err != nil {
		t.Fatal(err)
	}
	if fake.closed != 1 {
		t.Fatalf("remote connection closed %d times", fake.closed)
	}
}

func TestBrokerHostDoesNotReportLoginDeliveryBeforeRemoteACK(t *testing.T) {
	fake := &fakeBrokerHost{events: make(chan broker.Event), sent: make(chan cnproto.LoginIdentity), ack: make(chan error)}
	host := newBrokerHost(fake)
	defer host.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- host.SendLogin(ctx, cnproto.LoginIdentity{}) }()
	select {
	case <-fake.sent:
	case <-ctx.Done():
		t.Fatal("login was not sent")
	}
	select {
	case <-result:
		t.Fatal("delivery reported before remote ACK")
	default:
	}
	expected := errors.New("synthetic native delivery failed")
	fake.ack <- expected
	if err := <-result; !errors.Is(err, expected) {
		t.Fatal("native delivery failure lost")
	}
}
