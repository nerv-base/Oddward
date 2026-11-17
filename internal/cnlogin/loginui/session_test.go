// SPDX-License-Identifier: GPL-3.0-or-later
package loginui

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// This subprocess is this package's synthetic test executable, never LoginHost/SDK/game.
func TestMain(m *testing.M) {
	if os.Getenv("ODDWARD_SYNTHETIC_LOGINUI") == "1" {
		os.Exit(runSyntheticHelper())
	}
	os.Exit(m.Run())
}

// The optional transcript observes frames received by the owned synthetic child;
// it never contains real credentials, HWNDs or account data.
func runSyntheticHelper() int {
	var trace *os.File
	if path := os.Getenv("ODDWARD_SYNTHETIC_FRAME_TRACE"); path != "" {
		var err error
		trace, err = os.Create(path)
		if err != nil {
			return 1
		}
		defer trace.Close()
	}
	if writeFrame(os.Stdout, envelope{Version: 1, Type: "event", Event: &Event{Kind: "ready"}}) != nil {
		return 1
	}
	binding := os.Getenv("ODDWARD_SYNTHETIC_WINDOW_BIND")
	if binding == "1" {
		binding = "bind_overlay"
	}
	kinds := strings.Split(binding, ",")
	ids := strings.Split(os.Getenv("ODDWARD_SYNTHETIC_BIND_IDS"), ",")
	if ids[0] == "" {
		ids = []string{"1"}
	}
	released := os.Getenv("ODDWARD_SYNTHETIC_BIND_GATED") != "1"
	var current Model
	var pending *Event
	round := 0
	sendRequest := func() error {
		pending = &Event{Kind: kinds[round%len(kinds)], Window: "23", GameWindow: "42", RequestID: ids[round]}
		typ := "event"
		if os.Getenv("ODDWARD_SYNTHETIC_REVERSE_ACK") == "1" {
			typ = "window_bound"
		}
		return writeFrame(os.Stdout, envelope{Version: 1, Type: typ, Event: pending})
	}
	for {
		v, err := readFrame(os.Stdin)
		if err != nil || v.Type == "close" {
			return 0
		}
		if trace != nil {
			if json.NewEncoder(trace).Encode(v) != nil {
				return 1
			}
		}
		switch v.Type {
		case "model":
			current = *v.Model
			if current.Status == "binding-released" {
				released = true
			}
			if binding != "" && round == 0 && pending == nil {
				if sendRequest() != nil {
					return 1
				}
			}
			if pending != nil {
				continue
			}
		case "window_bound":
			if !released || pending == nil || v.Event == nil || *v.Event != *pending {
				return 1
			}
			round++
			pending = nil
			if round < len(ids) {
				if sendRequest() != nil {
					return 1
				}
				continue
			}
		default:
			return 1
		}
		kind := "acknowledge"
		if current.Screen == "phone" {
			kind = "send_sms"
		}
		if writeFrame(os.Stdout, envelope{Version: 1, Type: "event", Event: &Event{Kind: kind}}) != nil {
			return 1
		}
	}
}

func TestSessionPreservesEventOrderAndClosesOwnedHelper(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ODDWARD_SYNTHETIC_LOGINUI", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	s, err := Start(ctx, Config{Executable: exe}, Model{Screen: "notice"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, kind := range []string{"ready", "acknowledge"} {
		select {
		case e := <-s.Events():
			if e.Kind != kind {
				t.Fatalf("expected %s got %s", kind, e.Kind)
			}
		case <-ctx.Done():
			t.Fatal("event timed out")
		}
	}
	if err := s.Update(ctx, Model{Screen: "phone"}); err != nil {
		t.Fatal(err)
	}
	select {
	case e := <-s.Events():
		if e.Kind != "send_sms" {
			t.Fatal("wrong event")
		}
	case <-ctx.Done():
		t.Fatal("update timed out")
	}
	cancel()
	select {
	case <-s.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("helper survived cancellation")
	}
	if err := s.Err(); err != nil {
		t.Fatal(err)
	}
	if err := s.Update(context.Background(), Model{Screen: "phone"}); !errors.Is(err, ErrClosed) {
		t.Fatal("closed session accepted model")
	}
}
func TestSessionRejectsInvalidModelBeforeStarting(t *testing.T) {
	if _, err := Start(context.Background(), Config{Executable: "missing.exe"}, Model{Screen: "invalid"}); !errors.Is(err, ErrProtocol) {
		t.Fatal("invalid initial model not rejected")
	}
}
