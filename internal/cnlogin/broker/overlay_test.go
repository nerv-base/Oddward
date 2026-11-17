// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package broker

import (
	"context"
	"errors"
	"github.com/nerv-base/Oddward/internal/cnlogin/sdkhost"
	"github.com/nerv-base/Oddward/internal/process"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

func TestBindOverlayServerKeepsAckAndCancellationSemantics(t *testing.T) {
	for _, prefer := range []bool{false, true} {
		name := "owner"
		if prefer {
			name = "child"
		}
		t.Run(name, func(t *testing.T) { testOverlayCancellation(t, prefer) })
	}
}
func testOverlayCancellation(t *testing.T, prefer bool) {
	t.Helper()
	a, b := net.Pipe()
	host := &fakeHost{events: make(chan sdkhost.Event)}
	game := &fakeGame{exit: make(chan uint32, 1), waiting: make(chan struct{})}
	finished := make(chan error, 1)
	entered := make(chan struct{})
	canceled := make(chan struct{})
	go func() {
		finished <- serve(context.Background(), a, func(context.Context, string) (*owned, error) {
			return &owned{host: host, game: game, bindOverlay: func(ctx context.Context, p overlayPayload) error {
				if p.UI.PID != 222 || p.UI.CreationTime != 789 || p.Window != 100 || p.GameWindow != 200 || p.PreferChild != prefer {
					return errRemote
				}
				close(entered)
				<-ctx.Done()
				close(canceled)
				return ctx.Err()
			}}, nil
		})
	}()
	c, err := startClient(testContext(t), b, `C:\synthetic`)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithCancel(testContext(t))
	defer cancel()
	done := make(chan error, 1)
	go func() {
		bind := c.BindOverlay
		if prefer {
			bind = c.EmbedOverlay
		}
		done <- bind(ctx, process.Identity{PID: 222, CreationTime: 789}, 100, 200)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("binder was not called")
	}
	select {
	case err := <-done:
		t.Fatalf("completed before native binding returned: %v", err)
	default:
	}
	cancel()
	if err := <-done; !errors.Is(err, ErrUnobserved) || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel result: %v", err)
	}
	select {
	case <-canceled:
	case <-time.After(time.Second):
		t.Fatal("binder did not receive cancellation")
	}
	c.CompleteLoginRequest()
	game.exit <- 41
	if code, err := c.Wait(testContext(t)); err != nil || code != 41 {
		t.Fatalf("binding cancellation lost observation: %d %v", code, err)
	}
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
}
func TestBindOverlayProtocolRejectsExtraAuthority(t *testing.T) {
	called := false
	o := &owned{bindOverlay: func(context.Context, overlayPayload) error { called = true; return nil }}
	for _, payload := range []string{
		`{"ui":{"pid":222,"creation_time":"789"},"window":"100","game_window":"200","game_pid":999}`,
		`{"ui":{"pid":222,"creation_time":"789"},"window":100,"game_window":"200"}`,
		`{"ui":{"pid":222,"creation_time":"0"},"window":"100","game_window":"200"}`,
		`{"ui":{"pid":222,"creation_time":"789"},"window":"100","game_window":"200","command":"synthetic-private-value"}`,
	} {
		if err := execute(request{ctx: context.Background(), frame: wireFrame{Kind: "bindoverlay", Payload: []byte(payload)}}, o); !errors.Is(err, errProtocol) {
			t.Fatal("overlay accepted unknown authority or bad identity")
		}
	}
	if called {
		t.Fatal("malformed overlay reached native binder")
	}
}

func TestBindOverlayRequiresAck(t *testing.T) {
	c, peer := startedClient(t)
	done := make(chan error, 1)
	go func() { done <- c.BindOverlay(testContext(t), process.Identity{PID: 222, CreationTime: 789}, 100, 200) }()
	f := getFrame(t, peer)
	if f.Kind != "bindoverlay" {
		t.Fatal("wrong overlay command")
	}
	var p overlayPayload
	if decode(f.Payload, &p) != nil || p.UI.PID != 222 || p.Window != 100 || p.GameWindow != 200 {
		t.Fatal("overlay request lost identity or windows")
	}
	select {
	case err := <-done:
		t.Fatalf("binding completed before ACK: %v", err)
	default:
	}
	sendFrame(t, peer, "reply", f.ID, replyPayload{})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type fakeOverlaySystem struct {
	processes map[process.Identity]overlayProcess
	windows   map[uintptr]overlayWindow
	writes    int
	mismatch  bool
	closes    *atomic.Int32
}

func (s *fakeOverlaySystem) open(id process.Identity) (overlayProcess, error) {
	p, ok := s.processes[id]
	if !ok {
		return overlayProcess{}, errRemote
	}
	return p, nil
}
func (s *fakeOverlaySystem) window(w uintptr) (overlayWindow, error) {
	v, ok := s.windows[w]
	if !ok {
		return overlayWindow{}, errRemote
	}
	return v, nil
}
func (s *fakeOverlaySystem) setOwner(w, owner uintptr) error {
	s.writes++
	if !s.mismatch {
		v := s.windows[w]
		v.owner = owner
		s.windows[w] = v
	}
	return nil
}
func overlayFixture() (*fakeOverlaySystem, process.Identity, process.Identity, overlayPayload) {
	parent := process.Identity{PID: 10, CreationTime: 100}
	game := process.Identity{PID: 20, CreationTime: 200}
	ui := process.Identity{PID: 30, CreationTime: 300}
	closes := new(atomic.Int32)
	s := &fakeOverlaySystem{processes: make(map[process.Identity]overlayProcess), windows: map[uintptr]overlayWindow{100: {pid: 30, topLevel: true}, 200: {pid: 20, topLevel: true, visible: true}}, closes: closes}
	for _, id := range []process.Identity{parent, game, ui} {
		s.processes[id] = overlayProcess{identity: id, parentPID: 10, sid: "synthetic-user", session: 1, ordinary: true, alive: func() bool { return true }, close: func() { closes.Add(1) }}
	}
	return s, parent, game, overlayPayload{UI: ui, Window: 100, GameWindow: 200}
}
func TestBindOverlayOnlyWritesOwnWindowAndReadsBack(t *testing.T) {
	for _, owner := range []uintptr{0, 300, 200} {
		s, parent, game, p := overlayFixture()
		v := s.windows[100]
		v.owner = owner
		s.windows[100] = v
		s.windows[300] = overlayWindow{pid: p.UI.PID, topLevel: true}
		if err := bindOverlay(context.Background(), parent, game, p, s); err != nil {
			t.Fatalf("valid owner %d rejected: %v", owner, err)
		}
		if s.writes != 1 || s.windows[100].owner != 200 || s.windows[200].owner != 0 {
			t.Fatal("unexpected window mutation")
		}
		if s.closes.Load() != 3 {
			t.Fatal("process handles not closed")
		}
	}
}
func TestBindOverlayRejectsUntrustedProcessesAndWindows(t *testing.T) {
	cases := map[string]func(*fakeOverlaySystem, process.Identity, process.Identity, *overlayPayload){
		"zero creation": func(s *fakeOverlaySystem, parent, game process.Identity, p *overlayPayload) { p.UI.CreationTime = 0 },
		"reused pid":    func(s *fakeOverlaySystem, parent, game process.Identity, p *overlayPayload) { p.UI.CreationTime++ },
		"wrong parent": func(s *fakeOverlaySystem, parent, game process.Identity, p *overlayPayload) {
			v := s.processes[p.UI]
			v.parentPID++
			s.processes[p.UI] = v
		},
		"older than parent": func(s *fakeOverlaySystem, parent, game process.Identity, p *overlayPayload) {
			v := s.processes[p.UI]
			v.identity.CreationTime = 99
			s.processes[p.UI] = v
		},
		"different SID": func(s *fakeOverlaySystem, parent, game process.Identity, p *overlayPayload) {
			v := s.processes[p.UI]
			v.sid = "other-user"
			s.processes[p.UI] = v
		},
		"different session": func(s *fakeOverlaySystem, parent, game process.Identity, p *overlayPayload) {
			v := s.processes[p.UI]
			v.session++
			s.processes[p.UI] = v
		},
		"elevated UI": func(s *fakeOverlaySystem, parent, game process.Identity, p *overlayPayload) {
			v := s.processes[p.UI]
			v.ordinary = false
			s.processes[p.UI] = v
		},
		"exited root": func(s *fakeOverlaySystem, parent, game process.Identity, p *overlayPayload) {
			v := s.processes[parent]
			v.alive = func() bool { return false }
			s.processes[parent] = v
		},
		"exited UI": func(s *fakeOverlaySystem, parent, game process.Identity, p *overlayPayload) {
			v := s.processes[p.UI]
			v.alive = func() bool { return false }
			s.processes[p.UI] = v
		},
		"exited game": func(s *fakeOverlaySystem, parent, game process.Identity, p *overlayPayload) {
			v := s.processes[game]
			v.alive = func() bool { return false }
			s.processes[game] = v
		},
		"foreign UI window": func(s *fakeOverlaySystem, parent, game process.Identity, p *overlayPayload) {
			v := s.windows[100]
			v.pid = 90
			s.windows[100] = v
		},
		"child UI window": func(s *fakeOverlaySystem, parent, game process.Identity, p *overlayPayload) {
			v := s.windows[100]
			v.topLevel = false
			s.windows[100] = v
		},
		"topmost UI window": func(s *fakeOverlaySystem, parent, game process.Identity, p *overlayPayload) {
			v := s.windows[100]
			v.topmost = true
			s.windows[100] = v
		},
		"foreign game window": func(s *fakeOverlaySystem, parent, game process.Identity, p *overlayPayload) {
			v := s.windows[200]
			v.pid = 90
			s.windows[200] = v
		},
		"hidden game window": func(s *fakeOverlaySystem, parent, game process.Identity, p *overlayPayload) {
			v := s.windows[200]
			v.visible = false
			s.windows[200] = v
		},
		"tool game window": func(s *fakeOverlaySystem, parent, game process.Identity, p *overlayPayload) {
			v := s.windows[200]
			v.tool = true
			s.windows[200] = v
		},
		"child game window": func(s *fakeOverlaySystem, parent, game process.Identity, p *overlayPayload) {
			v := s.windows[200]
			v.topLevel = false
			s.windows[200] = v
		},
		"foreign owner": func(s *fakeOverlaySystem, parent, game process.Identity, p *overlayPayload) {
			v := s.windows[100]
			v.owner = 300
			s.windows[100] = v
			s.windows[300] = overlayWindow{pid: 90, topLevel: true}
		},
		"visible UI owner": func(s *fakeOverlaySystem, parent, game process.Identity, p *overlayPayload) {
			v := s.windows[100]
			v.owner = 300
			s.windows[100] = v
			s.windows[300] = overlayWindow{pid: p.UI.PID, topLevel: true, visible: true}
		},
	}
	for name, change := range cases {
		for _, prefer := range []bool{false, true} {
			mode := "owner"
			if prefer {
				mode = "child"
			}
			t.Run(name+"/"+mode, func(t *testing.T) {
				s, parent, game, p := overlayFixture()
				p.PreferChild = prefer
				change(s, parent, game, &p)
				if err := bindOverlay(context.Background(), parent, game, p, s); err == nil {
					t.Fatal("untrusted overlay accepted")
				}
				if s.writes != 0 {
					t.Fatal("rejected overlay mutated window")
				}
			})
		}
	}
}
func TestBindOverlayReadbackMismatchAndCancellation(t *testing.T) {
	s, parent, game, p := overlayFixture()
	s.mismatch = true
	if err := bindOverlay(context.Background(), parent, game, p, s); err == nil || s.writes != 1 {
		t.Fatal("owner write was not read back")
	}
	s, parent, game, p = overlayFixture()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := bindOverlay(ctx, parent, game, p, s); !errors.Is(err, context.Canceled) || s.writes != 0 {
		t.Fatal("canceled overlay mutated window")
	}
}
