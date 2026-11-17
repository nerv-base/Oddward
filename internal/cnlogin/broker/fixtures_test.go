// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package broker

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/cnlogin/cnproto"
	"github.com/nerv-base/Oddward/internal/process"
)

func testContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return ctx
}
func sendFrame(t *testing.T, c net.Conn, kind string, id uint64, payload any) {
	t.Helper()
	f, err := makeFrame(kind, id, payload)
	if err != nil {
		t.Fatal(err)
	}
	defer f.zero()
	if err := writeFrame(c, f); err != nil {
		t.Fatal(err)
	}
}
func getFrame(t *testing.T, c net.Conn) wireFrame {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	f, err := readFrame(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.zero)
	return f
}
func startedClient(t *testing.T) (*Client, net.Conn) {
	t.Helper()
	a, b := net.Pipe()
	c := newClient(a)
	t.Cleanup(func() { _ = c.Close(); _ = b.Close() })
	done := make(chan error, 1)
	go func() { done <- c.call(testContext(t), "start", startPayload{Root: `C:\synthetic`}) }()
	f := getFrame(t, b)
	sendFrame(t, b, "started", f.ID, startedPayload{Identity: process.Identity{PID: 123, CreationTime: 456}, InstalledVersion: "2.0.9"})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return c, b
}
func syntheticLogin() cnproto.LoginIdentity {
	return cnproto.LoginIdentity{GameUID: []byte("synthetic-game"), GameToken: []byte("synthetic-token"), ChannelUID: []byte("synthetic-channel"), ChannelToken: []byte("synthetic-channel-token"), Info: []byte(`{}`)}
}
