//go:build windows

// SPDX-License-Identifier: GPL-3.0-or-later

package process

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// Only this test executable, with an explicit marker and owned signal directory,
// can enter helper mode. There is no scan or interaction with an installed game.
func TestOwnedHelperProcess(t *testing.T) {
	if os.Getenv("ODDWARD_OWNED_PROCESS_TEST") != "1" {
		return
	}
	args := os.Args
	if len(args) < 3 {
		os.Exit(90)
	}
	dir := args[len(args)-2]
	code, err := strconv.Atoi(args[len(args)-1])
	if err != nil {
		os.Exit(91)
	}
	if err = os.WriteFile(filepath.Join(dir, "ready"), []byte("ready"), 0600); err != nil {
		os.Exit(92)
	}
	deadline := time.NewTimer(15 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-deadline.C:
			os.Exit(93)
		case <-tick.C:
			if _, err := os.Stat(filepath.Join(dir, "stop")); err == nil {
				os.Exit(code)
			}
		}
	}
}
func startOwned(t *testing.T, code int) (*exec.Cmd, func()) {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestOwnedHelperProcess$", "--", dir, strconv.Itoa(code))
	cmd.Env = append(os.Environ(), "ODDWARD_OWNED_PROCESS_TEST=1")
	// No piped stdio or lifetime dependency on a parent copy goroutine.
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if !waited {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})
	limit := time.After(3 * time.Second)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
			break
		}
		select {
		case <-limit:
			t.Fatal("owned helper did not become ready")
		case <-tick.C:
		}
	}
	stop := func() {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, "stop"), []byte("stop"), 0600); err != nil {
			t.Fatal(err)
		}
		err := cmd.Wait()
		waited = true
		if code == 0 && err != nil {
			t.Fatal(err)
		}
		if code != 0 {
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != code {
				t.Fatalf("helper exit: %v", err)
			}
		}
	}
	return cmd, stop
}
func TestWindowsCaptureAttachAndConfirmedExit(t *testing.T) {
	for _, code := range []int{0, 17, 259} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			cmd, stop := startOwned(t, code)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			h, err := Capture(ctx, cmd.Process)
			if err != nil {
				t.Fatal(err)
			}
			defer h.Close()
			id := h.Identity()
			if id.PID != uint32(cmd.Process.Pid) || id.CreationTime == 0 {
				t.Fatalf("invalid captured identity %+v", id)
			}
			attached, err := Attach(ctx, id)
			if err != nil {
				t.Fatal(err)
			}
			defer attached.Close()
			if got, err := attached.Poll(ctx); err != nil || got.Exited || got.Identity != id {
				t.Fatalf("live %+v %v", got, err)
			}
			wrong := id
			wrong.CreationTime++
			if other, err := Attach(ctx, wrong); !errors.Is(err, ErrIdentityMismatch) {
				if other != nil {
					other.Close()
				}
				t.Fatalf("mismatched identity accepted %v", err)
			}
			stop() // Go closes its original handle; our retained duplicate must still work.
			got, err := h.Wait(ctx)
			if err != nil || !got.Exited || got.ExitCode != uint32(code) || got.ExitTime == 0 {
				t.Fatalf("exit %+v %v", got, err)
			}
			got2, err := attached.Poll(ctx)
			if err != nil || got2 != got {
				t.Fatalf("retained handles disagree %+v %+v %v", got, got2, err)
			}
		})
	}
}
func TestWindowsCancellationAndCloseLeaveOwnedTargetAlive(t *testing.T) {
	cmd, stop := startOwned(t, 0)
	defer stop()
	h, err := Capture(context.Background(), cmd.Process)
	if err != nil {
		t.Fatal(err)
	}
	defer h.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err = h.Wait(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("wait cancel %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("cancellation exceeded test budget: %v", elapsed)
	}
	if got, err := h.Poll(context.Background()); err != nil || got.Exited {
		t.Fatalf("cancel changed target %+v %v", got, err)
	}
	id := h.Identity()
	if err = h.Close(); err != nil {
		t.Fatal(err)
	}
	other, err := Attach(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if got, err := other.Poll(context.Background()); err != nil || got.Exited {
		t.Fatalf("close changed target %+v %v", got, err)
	}
}
func TestWindowsAcquisitionRejectsInvalidOrCanceledInputs(t *testing.T) {
	for _, id := range []Identity{{}, {PID: 1}, {CreationTime: 1}} {
		if h, err := Attach(context.Background(), id); !errors.Is(err, ErrInvalidIdentity) {
			if h != nil {
				h.Close()
			}
			t.Fatalf("invalid accepted: %+v %v", id, err)
		}
	}
	if h, err := Capture(context.Background(), nil); err == nil {
		if h != nil {
			h.Close()
		}
		t.Fatal("nil process accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if h, err := Attach(ctx, sampleIdentity); !errors.Is(err, context.Canceled) {
		if h != nil {
			h.Close()
		}
		t.Fatalf("pre-canceled acquisition: %v", err)
	}
}
