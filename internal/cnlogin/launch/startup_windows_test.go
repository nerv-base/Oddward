//go:build windows

// SPDX-License-Identifier: GPL-3.0-or-later
package launch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

// Use the full Prepared.Start path and a real pipe connection. The receiver is
// our own test executable in a synthetic install, never a game or official DLL.
func TestStartReturnsForConnectedSDKBeforeHandshakeTimeout(t *testing.T) {
	t.Run("direct", func(t *testing.T) { testConnectedStart(t, false) })
	t.Run("bootstrap", func(t *testing.T) { testConnectedStart(t, true) })
}

func testConnectedStart(t *testing.T, bootstrap bool) {
	t.Helper()
	f := newFakeInstall(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.game, data, 0700); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	pipeName := `\\.\pipe\Oddward.Launch.Startup.` + filepath.Base(dir)
	pipe, err := windows.CreateNamedPipe(windows.StringToUTF16Ptr(pipeName), windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED, windows.PIPE_TYPE_MESSAGE|windows.PIPE_READMODE_MESSAGE, 1, 8192, 8192, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(pipe)
	p, err := prepare(context.Background(), f.root, syntheticProfile, func(context.Context) error { return nil }, `Local\Oddward.Launch.Startup.`+filepath.Base(dir))
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	release := filepath.Join(dir, "release")
	env := append(os.Environ(), "ODDWARD_LAUNCH_HELPER=1", "SYNTHETIC_REPORT="+filepath.Join(dir, "report.json"), "SYNTHETIC_RELEASE="+release, "SYNTHETIC_PIPE="+pipeName)
	bootstrapReport := filepath.Join(dir, "bootstrap.txt")
	if bootstrap {
		env = append(env, "SYNTHETIC_BOOTSTRAP_REPORT="+bootstrapReport)
	}
	started := time.Now()
	game, startErr := p.Start(context.Background(), Options{
		PipeName: pipeName, ParentEnvironment: env,
		ConnectedClientPID: func() uint32 {
			var pid uint32
			if windows.GetNamedPipeClientProcessId(pipe, &pid) != nil {
				return 0
			}
			return pid
		},
	})
	elapsed := time.Since(started)
	if game == nil {
		t.Fatalf("start: %v", startErr)
	}
	defer func() {
		_ = os.WriteFile(release, nil, 0600)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if _, err := game.Wait(ctx); err != nil {
			t.Errorf("reap owned helper: %v", err)
		}
		if err := game.Close(); err != nil {
			t.Errorf("close owned helper: %v", err)
		}
	}()
	if startErr != nil {
		t.Fatal(startErr)
	}
	// A successor can be discovered before it connects. Wait for that owned
	// child to identify itself independently through the pipe.
	var peer uint32
	deadline := time.Now().Add(4 * time.Second)
	for {
		err = windows.GetNamedPipeClientProcessId(pipe, &peer)
		if err == nil && peer != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("owned child did not connect: %v", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if peer != game.Identity().PID {
		t.Fatalf("not the owned pipe client: pid=%d identity=%+v", peer, game.Identity())
	}
	peerHandle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, peer)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(peerHandle)
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(peerHandle, &creation, &exit, &kernel, &user); err != nil {
		t.Fatal(err)
	}
	created := uint64(creation.HighDateTime)<<32 | uint64(creation.LowDateTime)
	if game.Identity().CreationTime != created {
		t.Fatal("tracked creation time differs from connected process")
	}
	if bootstrap {
		data, err := os.ReadFile(bootstrapReport)
		if err != nil {
			t.Fatal(err)
		}
		parent, err := strconv.ParseUint(string(data), 10, 32)
		if err != nil || uint32(parent) == peer {
			t.Fatalf("bootstrap was not replaced: pid=%s peer=%d err=%v", data, peer, err)
		}
	}
	// SDK getStartInfo waits 5000 ms. Leave a margin for BindGame + op1001.
	if elapsed >= 4*time.Second {
		t.Fatalf("connected SDK cannot be bound before its 5s handshake deadline: Start took %s", elapsed)
	}
	t.Logf("connected child ready for binding after %s", elapsed)
}

func TestSuccessorWaitHonorsCancellationAndIgnoresForeignClient(t *testing.T) {
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	release := filepath.Join(dir, "release")
	game, err := startProcess(context.Background(), LaunchPlan{Image: exe, CommandLine: RequiredArguments, Directory: dir, Environment: append(os.Environ(), "ODDWARD_LAUNCH_HELPER=1", "SYNTHETIC_REPORT="+filepath.Join(dir, "report.json"), "SYNTHETIC_RELEASE="+release)}, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = os.WriteFile(release, nil, 0600)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, _ = game.Wait(ctx)
		_ = game.Close()
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Cancelling inside the callback proves the running-process loop observes
	// cancellation. Returning the parent PID must not signal game readiness.
	err = game.awaitSuccessor(ctx, exe, func() uint32 { cancel(); return uint32(os.Getpid()) })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("foreign client bypassed cancellable successor wait: %v", err)
	}
}
