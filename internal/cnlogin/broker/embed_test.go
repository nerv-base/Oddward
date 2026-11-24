// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package broker

import (
	"context"
	"errors"
	"github.com/nerv-base/Oddward/internal/process"
	"testing"
)

type fakeEmbedSystem struct {
	*fakeOverlaySystem
	calls       []string
	fail        string
	unsupported bool
	wrongParent bool
	step        func(string)
}

func embedFixture() (*fakeEmbedSystem, process.Identity, process.Identity, overlayPayload) {
	base, root, game, p := overlayFixture()
	p.PreferChild = true
	w := base.windows[100]
	w.style = overlayPopup
	w.dpi = 1
	w.tid = 11
	base.windows[100] = w
	g := base.windows[200]
	g.style = overlayPopup | overlayVisible
	g.dpi = 1
	g.tid = 22
	base.windows[200] = g
	return &fakeEmbedSystem{fakeOverlaySystem: base}, root, game, p
}
func (s *fakeEmbedSystem) sameDPI(a, b uintptr) bool { return !s.unsupported && a != 0 && a == b }
func (s *fakeEmbedSystem) operation(name string, w uintptr) error {
	if w != 100 {
		return errors.New("fixture detected foreign window mutation")
	}
	s.calls = append(s.calls, name)
	if s.step != nil {
		s.step(name)
	}
	if s.fail == name {
		s.fail = ""
		return errRemote
	}
	return nil
}
func (s *fakeEmbedSystem) setStyle(w, style uintptr) error {
	if err := s.operation("style", w); err != nil {
		return err
	}
	v := s.windows[w]
	v.style = style
	v.topLevel = style&overlayChild == 0
	if !v.topLevel {
		v.parent = 0 // Native style change leaves a detached desktop child and preserves owner.
	}
	s.windows[w] = v
	return nil
}
func (s *fakeEmbedSystem) setParent(w, parent uintptr) error {
	if err := s.operation("parent", w); err != nil {
		return err
	}
	if s.wrongParent {
		s.wrongParent = false
		return nil
	}
	v := s.windows[w]
	v.parent = parent
	s.windows[w] = v
	return nil
}
func (s *fakeEmbedSystem) frameChanged(w uintptr) error { return s.operation("frame", w) }

func TestEmbedOverlayRequestsPreferenceAndWaitsForAck(t *testing.T) {
	c, peer := startedClient(t)
	done := make(chan error, 1)
	go func() {
		done <- c.EmbedOverlay(testContext(t), process.Identity{PID: 222, CreationTime: 789}, 100, 200)
	}()
	f := getFrame(t, peer)
	var p overlayPayload
	if f.Kind != "bindoverlay" || decode(f.Payload, &p) != nil || !p.PreferChild {
		t.Fatal("child preference not carried in authenticated overlay request")
	}
	select {
	case <-done:
		t.Fatal("embed returned before ACK")
	default:
	}
	sendFrame(t, peer, "reply", f.ID, replyPayload{})
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
func TestEmbedOverlaySameDPIChangesOnlyHiddenUI(t *testing.T) {
	s, root, game, p := embedFixture()
	target := s.windows[200]
	if err := bindOverlay(context.Background(), root, game, p, s); err != nil {
		t.Fatal(err)
	}
	w := s.windows[100]
	if w.parent != 200 || w.style&overlayChild == 0 || w.style&overlayChildClear != 0 || w.visible || w.topmost || s.windows[200] != target || len(s.calls) != 3 {
		t.Fatal("same-DPI UI was not embedded without changing game")
	}
	if s.closes.Load() != 3 {
		t.Fatal("retained process handles leaked")
	}
	s.calls = nil
	if err := bindOverlay(context.Background(), root, game, p, s); err != nil || len(s.calls) != 0 {
		t.Fatal("same-game child retry was not idempotent")
	}
}
func TestEmbedOverlayStripsTopLevelDecorationsAndRestoresOnFailure(t *testing.T) {
	const framelessStyle = overlayCaption | overlaySysMenu | overlayMinimizeBox | overlayMaximizeBox | overlayThickFrame | 0x02000000 // WS_CLIPCHILDREN
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "embed", true: "rollback"}[fail], func(t *testing.T) {
			s, root, game, p := embedFixture()
			w := s.windows[100]
			w.style = framelessStyle
			s.windows[100] = w
			if fail {
				s.fail = "frame"
			}
			err := bindOverlay(context.Background(), root, game, p, s)
			actual := s.windows[100]
			if fail {
				if err == nil || actual.style != framelessStyle || !actual.topLevel || actual.parent != 0 {
					t.Fatalf("failed embed did not restore original frameless style: err=%v style=%#x", err, actual.style)
				}
				return
			}
			if err != nil || actual.style&overlayChild == 0 || actual.style&overlayChildClear != 0 || actual.style&0x02000000 == 0 || actual.parent != 200 {
				t.Fatalf("embed did not strip top-level decorations: err=%v style=%#x parent=%d", err, actual.style, actual.parent)
			}
		})
	}
}
func TestEmbedOverlayDPIUncertaintyFallsBackWithoutParentMutation(t *testing.T) {
	for _, mode := range []string{"different", "unsupported", "unknown"} {
		t.Run(mode, func(t *testing.T) {
			s, root, game, p := embedFixture()
			switch mode {
			case "different":
				v := s.windows[100]
				v.dpi = 2
				s.windows[100] = v
			case "unsupported":
				s.unsupported = true
			case "unknown":
				v := s.windows[100]
				v.dpi = 0
				s.windows[100] = v
			}
			if err := bindOverlay(context.Background(), root, game, p, s); err != nil {
				t.Fatal(err)
			}
			w := s.windows[100]
			if w.owner != 200 || !w.topLevel || w.style != overlayPopup || len(s.calls) != 0 {
				t.Fatal("DPI fallback attempted child conversion")
			}
		})
	}
}
func TestEmbedWindowFailureRestoresOnlyItsOriginalWindow(t *testing.T) {
	for _, stage := range []string{"style", "parent", "frame", "readback", "cancel"} {
		t.Run(stage, func(t *testing.T) {
			s, _, _, _ := embedFixture()
			w := s.windows[100]
			w.owner = 300
			s.windows[100] = w
			s.windows[300] = overlayWindow{pid: w.pid, tid: w.tid, topLevel: true}
			target := s.windows[200]
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			switch stage {
			case "readback":
				s.wrongParent = true
			case "cancel":
				s.step = func(name string) {
					if name == "parent" {
						cancel()
					}
				}
			default:
				s.fail = stage
			}
			if err := embedWindow(ctx, 100, 200, w, target, s, func() bool { return true }); err == nil {
				t.Fatal("partial child conversion reported success")
			}
			if len(s.calls) == 0 {
				t.Fatal("conversion never reached injected failure")
			}
			actual := s.windows[100]
			if actual.style != w.style || actual.owner != w.owner || actual.parent != 0 || s.windows[200] != target {
				t.Fatal("failed child conversion did not restore owned window")
			}
		})
	}
}
func TestEmbedOverlayRejectsVisibleForeignAndReusedChildren(t *testing.T) {
	for _, kind := range []string{"visible", "foreign-parent", "reused-ui", "reused-game", "child-dpi"} {
		t.Run(kind, func(t *testing.T) {
			s, root, game, p := embedFixture()
			w := s.windows[100]
			switch kind {
			case "visible":
				w.visible = true
				w.style |= overlayVisible
			case "foreign-parent":
				w.topLevel = false
				w.style = overlayChild
				w.parent = 999
			case "reused-ui":
				p.UI.CreationTime++
			case "reused-game":
				v := s.windows[200]
				v.pid++
				s.windows[200] = v
			case "child-dpi":
				w.topLevel = false
				w.style = overlayChild
				w.parent = 200
				w.dpi = 2
			}
			s.windows[100] = w
			if err := bindOverlay(context.Background(), root, game, p, s); err == nil {
				t.Fatal("untrusted child overlay accepted")
			}
			if len(s.calls) != 0 || s.writes != 0 {
				t.Fatal("rejected child overlay mutated a window")
			}
		})
	}
}
func TestEmbedWindowRechecksDPIBeforeParentMutation(t *testing.T) {
	s, _, _, _ := embedFixture()
	w, target := s.windows[100], s.windows[200]
	s.step = func(name string) {
		if name == "style" {
			g := s.windows[200]
			g.dpi = 2
			s.windows[200] = g
		}
	}
	if err := embedWindow(context.Background(), 100, 200, w, target, s, func() bool { return true }); err == nil {
		t.Fatal("DPI change accepted")
	}
	for _, call := range s.calls {
		if call == "parent" {
			t.Fatal("DPI changed before SetParent, but child was reparented")
		}
	}
	if s.windows[100].style != w.style {
		t.Fatal("DPI change left child style behind")
	}
}
func TestEmbedOverlayServerRoutesPreference(t *testing.T) {
	for _, prefer := range []bool{false, true} {
		called := false
		p := overlayPayload{UI: process.Identity{PID: 222, CreationTime: 789}, Window: 100, GameWindow: 200, PreferChild: prefer}
		o := &owned{bindOverlay: func(_ context.Context, actual overlayPayload) error {
			called = true
			if actual != p {
				return errRemote
			}
			return nil
		}}
		f, err := makeFrame("bindoverlay", 1, p)
		if err != nil {
			t.Fatal(err)
		}
		if err := execute(request{ctx: context.Background(), frame: f}, o); err != nil || !called {
			t.Fatal("server lost overlay preference or window identity")
		}
	}
}

func TestEmbedWindowDoesNotRecoverIntoReusedWindow(t *testing.T) {
	s, _, _, _ := embedFixture()
	w, g := s.windows[100], s.windows[200]
	s.step = func(name string) {
		if name == "style" {
			v := s.windows[100]
			v.tid++
			s.windows[100] = v
		}
	}
	if err := embedWindow(context.Background(), 100, 200, w, g, s, func() bool { return true }); !errors.Is(err, errOverlayRestore) {
		t.Fatal("window reuse was not a recovery failure")
	}
	if len(s.calls) != 1 || s.writes != 0 {
		t.Fatal("recovery mutated a reused window")
	}
}

func TestEmbedOverlayOptionalPreferenceIsStrict(t *testing.T) {
	for _, extra := range []string{``, `,"prefer_child":false`, `,"prefer_child":true`} {
		var p overlayPayload
		if decode([]byte(`{"ui":{"pid":222,"creation_time":"789"},"window":"100","game_window":"200"`+extra+`}`), &p) != nil {
			t.Fatal("valid optional preference rejected")
		}
	}
	for _, extra := range []string{`,"prefer_child":null`, `,"prefer_child":1`, `,"prefer_child":"true"`, `,"prefer_child":true,"prefer_child":false`, `,"PreferChild":true`} {
		var p overlayPayload
		if decode([]byte(`{"ui":{"pid":222,"creation_time":"789"},"window":"100","game_window":"200"`+extra+`}`), &p) == nil {
			t.Fatal("ambiguous child preference accepted")
		}
	}
}
