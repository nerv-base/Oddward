// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package broker

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/cnlogin/cnproto"
	"github.com/nerv-base/Oddward/internal/cnlogin/sdkhost"
	"github.com/nerv-base/Oddward/internal/process"
)

type fakeHost struct {
	events chan sdkhost.Event
	closed atomic.Bool
	send   func(context.Context, cnproto.LoginIdentity) error
}

func (h *fakeHost) Events() <-chan sdkhost.Event { return h.events }
func (h *fakeHost) SendLogin(ctx context.Context, id cnproto.LoginIdentity) error {
	if h.send != nil {
		return h.send(ctx, id)
	}
	return nil
}
func (h *fakeHost) Reply(context.Context, cnproto.PreLoginReply) error { return nil }
func (h *fakeHost) CompleteLoginRequest()                              {}
func (h *fakeHost) Close() error                                       { h.closed.Store(true); return nil }

type fakeGame struct {
	exit     chan uint32
	waiting  chan struct{}
	closed   atomic.Bool
	once     sync.Once
	failWait atomic.Bool
}

func (g *fakeGame) Identity() process.Identity { return process.Identity{PID: 123, CreationTime: 456} }
func (g *fakeGame) Wait(ctx context.Context) (uint32, error) {
	g.once.Do(func() { close(g.waiting) })
	if g.failWait.Swap(false) {
		return 0, errors.New("synthetic wait failure")
	}
	select {
	case code := <-g.exit:
		return code, nil
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}
func (g *fakeGame) Close() error { g.closed.Store(true); return nil }

func fakeSession(t *testing.T, host *fakeHost, setupErr error) (*Client, *fakeGame, <-chan error) {
	t.Helper()
	a, b := net.Pipe()
	game := &fakeGame{exit: make(chan uint32, 1), waiting: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- serve(context.Background(), a, func(context.Context, string) (*owned, error) {
			return &owned{host: host, game: game, installedVersion: "2.0.9"}, setupErr
		})
	}()
	c, err := startClient(testContext(t), b, `C:\synthetic`)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = c.Close()
		select {
		case game.exit <- 0:
		default:
		}
	})
	return c, game, done
}

func syntheticRegistration(t *testing.T) *cnproto.Registration {
	t.Helper()
	const key = "ABCDEFGHIJKLMNOPQRSTUVWXYZ012345"
	const pid = 123
	p := cnproto.KnownProfiles()[1]
	b := make([]byte, cnproto.RegistrationSize)
	binary.LittleEndian.PutUint32(b, cnproto.Magic)
	binary.LittleEndian.PutUint32(b[4:], cnproto.OpRegister)
	binary.LittleEndian.PutUint32(b[8:], uint32(len(b)))
	put := func(off int, s string) {
		for i, v := range s {
			binary.LittleEndian.PutUint16(b[cnproto.HeaderSize+off+2*i:], uint16(v))
		}
	}
	put(4, cnproto.AppIDString)
	put(0x104, key)
	put(0x204, cnproto.ConfigName)
	put(0x40c, p.SDKVersion)
	for i := cnproto.HeaderSize; i < len(b); i++ {
		b[i] ^= pid
	}
	r, err := cnproto.ParseRegistration(b, pid, p)
	clear(b)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestRegistrationDerivesOnlyKeysAndRedactsErrors(t *testing.T) {
	host := &fakeHost{events: make(chan sdkhost.Event)}
	c, game, done := fakeSession(t, host, errors.New("synthetic-private-error"))
	failure := <-c.Events()
	if failure.Kind != sdkhost.EventFailure || strings.Contains(failure.Err.Error(), "synthetic-private-error") {
		t.Fatal("setup failure lost or exposed data")
	}
	reg := syntheticRegistration(t)
	host.events <- sdkhost.Event{Kind: sdkhost.EventRegistration, Registration: reg}
	event := <-c.Events()
	if event.Kind != sdkhost.EventRegistration || string(event.ConfigKey[:]) != "QRSTUVWXYZ012345" || string(event.PreLoginKey[:]) != "BCHIJKLPQRZ01234" {
		t.Fatal("derived registration keys mismatch")
	}
	if _, err := reg.AppKeyTail(); !errors.Is(err, cnproto.ErrZeroed) {
		t.Fatal("registration not zeroed before forwarding")
	}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s", "%x", "%q"} {
		if s := fmt.Sprintf(verb, event); strings.Contains(s, "QRSTUVWXYZ") || strings.Contains(s, "81 82") {
			t.Fatal("event formatter exposed key material")
		}
	}
	event.Zero()
	game.exit <- 6
	if code, err := c.Wait(testContext(t)); err != nil || code != 6 {
		t.Fatalf("post-start error lost game: %d %v", code, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServerRejectsIllegalCommandsBeforeStarting(t *testing.T) {
	for _, input := range []string{
		`{"version":1,"kind":"exec","id":1,"payload":{"command":"secret-value"}}`,
		`{"version":1,"kind":"start","id":1,"payload":{"root":"C:\\synthetic","arguments":"secret-value"}}`,
		`{"version":1,"kind":"start","id":1,"payload":{"root":"relative"}}`,
	} {
		t.Run(input[21:25], func(t *testing.T) {
			a, b := net.Pipe()
			var calls atomic.Int32
			done := make(chan error, 1)
			go func() {
				done <- serve(context.Background(), a, func(context.Context, string) (*owned, error) { calls.Add(1); return nil, nil })
			}()
			_, _ = b.Write(framedJSON(input))
			_ = b.Close()
			select {
			case err := <-done:
				if err == nil || strings.Contains(err.Error(), "secret-value") {
					t.Fatal("illegal request succeeded or leaked")
				}
			case <-time.After(time.Second):
				t.Fatal("illegal request hung")
			}
			if calls.Load() != 0 {
				t.Fatal("illegal request started a game")
			}
		})
	}
}

func TestCancelBeforeStartDoesNotCallFactory(t *testing.T) {
	a, b := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var calls atomic.Int32
	if err := serve(ctx, a, func(context.Context, string) (*owned, error) { calls.Add(1); return nil, nil }); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	_ = b.Close()
	if calls.Load() != 0 {
		t.Fatal("canceled start executed")
	}
	if c, err := Start(ctx, `C:\synthetic`); c != nil || !errors.Is(err, context.Canceled) {
		t.Fatal("public Start ignored pre-cancel")
	}
}

func TestServerCancelStopsActiveRequestButStillReportsExit(t *testing.T) {
	entered := make(chan struct{})
	canceled := make(chan struct{})
	var retained []byte
	host := &fakeHost{events: make(chan sdkhost.Event), send: func(ctx context.Context, id cnproto.LoginIdentity) error {
		retained = id.GameToken
		close(entered)
		<-ctx.Done()
		close(canceled)
		return ctx.Err()
	}}
	c, game, done := fakeSession(t, host, nil)
	ctx, cancel := context.WithCancel(testContext(t))
	result := make(chan error, 1)
	go func() { result <- c.SendLogin(ctx, syntheticLogin()) }()
	<-entered
	cancel()
	if err := <-result; !errors.Is(err, ErrUnobserved) {
		t.Fatalf("canceled delivery known incorrectly: %v", err)
	}
	<-canceled
	// A following command's acknowledgement establishes the previous command
	// finished and its decoded identity storage has been wiped.
	c.CompleteLoginRequest()
	for _, v := range retained {
		if v != 0 {
			t.Fatal("decoded login bytes retained after delivery")
		}
	}
	game.exit <- 11
	if code, err := c.Wait(testContext(t)); err != nil || code != 11 {
		t.Fatalf("cancel lost exit: %d %v", code, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestInitializationContextsDetachAfterStarted(t *testing.T) {
	a, b := net.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	host := &fakeHost{events: make(chan sdkhost.Event)}
	game := &fakeGame{exit: make(chan uint32, 1), waiting: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- serve(ctx, a, func(context.Context, string) (*owned, error) { return &owned{host: host, game: game}, nil })
	}()
	c, err := startClient(ctx, b, `C:\synthetic`)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	cancel()
	c.CompleteLoginRequest()
	host.events <- sdkhost.Event{Kind: sdkhost.EventLoginRequested}
	if e := <-c.Events(); e.Kind != sdkhost.EventLoginRequested {
		t.Fatal("initialization cancellation detached running client")
	}
	game.exit <- 31
	if code, err := c.Wait(testContext(t)); err != nil || code != 31 {
		t.Fatalf("init context canceled game observation: %d %v", code, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestExpiredQueuedRequestDoesNotReachHost(t *testing.T) {
	a, b := net.Pipe()
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	host := &fakeHost{events: make(chan sdkhost.Event), send: func(context.Context, cnproto.LoginIdentity) error {
		calls.Add(1)
		close(entered)
		<-release
		return nil
	}}
	game := &fakeGame{exit: make(chan uint32, 1), waiting: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		done <- serve(context.Background(), a, func(context.Context, string) (*owned, error) { return &owned{host: host, game: game}, nil })
	}()
	sendFrame(t, b, "start", 1, startPayload{Root: `C:\synthetic`})
	_ = getFrame(t, b)
	sendFrame(t, b, "sendlogin", 2, loginFrom(syntheticLogin()))
	<-entered
	queued, _ := makeFrame("sendlogin", 3, loginFrom(syntheticLogin()))
	queued.Deadline = time.Now().Add(-time.Second).UnixNano()
	if err := writeFrame(b, queued); err != nil {
		t.Fatal(err)
	}
	queued.zero()
	close(release)
	first := getFrame(t, b)
	second := getFrame(t, b)
	var result replyPayload
	if first.ID != 2 || second.ID != 3 || decode(second.Payload, &result) != nil || result.ErrorCode != "canceled" {
		t.Fatal("expired queued request not rejected")
	}
	if calls.Load() != 1 {
		t.Fatal("expired queued request executed late")
	}
	_ = b.Close()
	game.exit <- 0
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestGameWaitFailureDoesNotForgeExitOrCloseHost(t *testing.T) {
	host := &fakeHost{events: make(chan sdkhost.Event)}
	// A failure once before the true exit models a transient process observation
	// error; no SDK resource may be released because of that failed observation.
	a, b := net.Pipe()
	game := &fakeGame{exit: make(chan uint32, 1), waiting: make(chan struct{})}
	game.failWait.Store(true)
	done := make(chan error, 1)
	go func() {
		done <- serve(context.Background(), a, func(context.Context, string) (*owned, error) { return &owned{host: host, game: game}, nil })
	}()
	c, err := startClient(testContext(t), b, `C:\synthetic`)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if e := <-c.Events(); e.Kind != sdkhost.EventFailure {
		t.Fatal("missing observation failure")
	}
	if host.closed.Load() || game.closed.Load() {
		t.Fatal("wait failure destroyed running resources")
	}
	game.exit <- 23
	if code, err := c.Wait(testContext(t)); err != nil || code != 23 {
		t.Fatalf("transient wait failure forged exit: %d %v", code, err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestServerDisconnectKeepsHostUntilRealGameExit(t *testing.T) {
	a, b := net.Pipe()
	host := &fakeHost{events: make(chan sdkhost.Event)}
	game := &fakeGame{exit: make(chan uint32, 1), waiting: make(chan struct{})}
	returned := make(chan error, 1)
	go func() {
		returned <- serve(context.Background(), a, func(context.Context, string) (*owned, error) {
			return &owned{host: host, game: game, installedVersion: "2.0.9"}, nil
		})
	}()
	sendFrame(t, b, "start", 1, startPayload{Root: `C:\synthetic`})
	f := getFrame(t, b)
	if f.Kind != "started" {
		t.Fatal("missing started")
	}
	_ = b.Close()
	select {
	case <-game.waiting:
	case <-time.After(time.Second):
		t.Fatal("game wait not running")
	}
	select {
	case err := <-returned:
		t.Fatalf("parent EOF released resources: %v", err)
	default:
	}
	if host.closed.Load() || game.closed.Load() {
		t.Fatal("resources closed while game running")
	}
	drained := make(chan struct{})
	go func() { host.events <- sdkhost.Event{Kind: sdkhost.EventLoginRequested}; close(drained) }()
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("disconnected host stopped draining SDK events")
	}
	game.exit <- 19
	select {
	case err := <-returned:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("server did not finish after real exit")
	}
	if !host.closed.Load() || !game.closed.Load() {
		t.Fatal("resources leaked after real exit")
	}
}
