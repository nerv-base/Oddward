// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

package loginui

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/process"
	"golang.org/x/sys/windows"
)

func TestWindowBindingUsesOwnedHelperIdentityBeforeDeliveringEvents(t *testing.T) {
	for _, kind := range []string{"bind_overlay", "embed_overlay"} {
		t.Run(kind, func(t *testing.T) { testWindowBindingIdentity(t, kind) })
	}
}

func testWindowBindingIdentity(t *testing.T, kind string) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ODDWARD_SYNTHETIC_LOGINUI", "1")
	t.Setenv("ODDWARD_SYNTHETIC_WINDOW_BIND", kind)
	called := make(chan process.Identity, 1)
	release := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cfg := Config{Executable: exe}
	bind := func(ctx context.Context, id process.Identity, window, game uintptr) error {
		if window != 23 || game != 42 {
			return ErrProtocol
		}
		called <- id
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if kind == "embed_overlay" {
		cfg.EmbedWindow = bind
	} else {
		cfg.BindWindow = bind
	}
	s, err := Start(ctx, cfg, Model{Screen: "notice"})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	select {
	case e := <-s.Events():
		if e.Kind != "ready" {
			t.Fatal("first event is not ready")
		}
	case <-ctx.Done():
		t.Fatal("ready timeout")
	}
	var identity process.Identity
	select {
	case identity = <-called:
	case e := <-s.Events():
		t.Fatalf("internal bind leaked as UI action: %s", e.Kind)
	case <-ctx.Done():
		t.Fatal("binding not dispatched")
	}
	if identity.PID == 0 || identity.PID == uint32(os.Getpid()) || identity.CreationTime == 0 {
		t.Fatal("helper identity missing or spoofed")
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, identity.PID)
	if err != nil {
		t.Fatal(err)
	}
	var created, exited, kernel, user windows.Filetime
	err = windows.GetProcessTimes(h, &created, &exited, &kernel, &user)
	windows.CloseHandle(h)
	if err != nil || uint64(created.HighDateTime)<<32|uint64(created.LowDateTime) != identity.CreationTime {
		t.Fatal("binding identity does not match the owned child")
	}
	select {
	case e := <-s.Events():
		t.Fatalf("event passed unacknowledged binding: %s", e.Kind)
	default:
	}
	close(release)
	select {
	case e := <-s.Events():
		if e.Kind != "acknowledge" {
			t.Fatalf("binding event escaped: %s", e.Kind)
		}
	case <-ctx.Done():
		t.Fatal("post-bind event timeout")
	}
}

func TestRejectedOrUnavailableWindowBindingClosesOnlyHelper(t *testing.T) {
	for _, kind := range []string{"bind_overlay", "embed_overlay"} {
		t.Run(kind, func(t *testing.T) { testRejectedBinding(t, kind) })
	}
}

func testRejectedBinding(t *testing.T, kind string) {
	for _, reject := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing", true: "rejected"}[reject], func(t *testing.T) {
			exe, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("ODDWARD_SYNTHETIC_LOGINUI", "1")
			t.Setenv("ODDWARD_SYNTHETIC_WINDOW_BIND", kind)
			cfg := Config{Executable: exe}
			if reject {
				bind := func(context.Context, process.Identity, uintptr, uintptr) error {
					return errors.New("synthetic binding rejected")
				}
				if kind == "embed_overlay" {
					cfg.EmbedWindow = bind
				} else {
					cfg.BindWindow = bind
				}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			s, err := Start(ctx, cfg, Model{Screen: "notice"})
			if err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			select {
			case <-s.Done():
			case <-ctx.Done():
				t.Fatal("failed binding left helper alive")
			}
			if !errors.Is(s.Err(), ErrHelper) {
				t.Fatalf("binding error not sanitized: %v", s.Err())
			}
			for e := range s.Events() {
				if e.Kind != "ready" {
					t.Fatalf("action delivered after rejected binding: %s", e.Kind)
				}
			}
		})
	}
}
