// SPDX-License-Identifier: GPL-3.0-or-later
//go:build windows

// Package native implements Oddward's pure Go + MyGo Native UI login window
// and welcome banner helper process. It speaks the exact bounded JSON frame
// protocol defined in internal/cnlogin/loginui over stdin/stdout and performs
// no network, account-store or credential I/O.
package native

import (
	"errors"
	"io"
	"os"
	"sync"
	"sync/atomic"

	"github.com/egoist/mygo"
	"golang.org/x/sys/windows"

	"github.com/nerv-base/Oddward/internal/cnlogin/loginui"
)

var errWindowCreate = errors.New("loginui/native: failed to create native window")

// Run checks whether args invoke the isolated login UI helper role
// (`--cnlogin-ui`). When handled is true, the caller must exit immediately with
// exitCode without opening databases, account stores or the main launcher UI.
//
// Security hardening:
//   - Rejects any extra or polluted command-line arguments (len(args) != 1).
//   - Requires pipe transport for stdin and stdout. FILE_TYPE_PIPE does not
//     authenticate the caller. Privileged window operations belong to the broker.
//   - Never spawns child processes, touches the network, or reads user stores.
func Run(args []string) (handled bool, exitCode int) {
	if len(args) == 0 || args[0] != loginui.Mode {
		return false, 0
	}
	if len(args) != 1 {
		return true, 1
	}
	if !isPipe(os.Stdin) || !isPipe(os.Stdout) {
		return true, 1
	}
	return true, RunStreams(os.Stdin, os.Stdout)
}

func isPipe(f *os.File) bool {
	if f == nil {
		return false
	}
	ft, err := windows.GetFileType(windows.Handle(f.Fd()))
	return err == nil && ft == windows.FILE_TYPE_PIPE
}

// RunStreams executes the helper event loop over in and out until closed, EOF,
// game exit, user cancel or protocol rejection. It must be called on the main
// OS thread (before any other mygo.App.Run call).
func RunStreams(in io.Reader, out io.Writer) int {
	var (
		writeMu sync.Mutex
		closed  atomic.Bool
		code    atomic.Int32
	)
	outbound := make(chan loginui.Envelope, 64)
	writerDone := make(chan struct{})

	var ctrl *controller
	finish := func(c int) {
		if closed.Swap(true) {
			return
		}
		code.Store(int32(c))
		writeMu.Lock()
		close(outbound)
		writeMu.Unlock()
		mygo.App.Quit()
	}

	emit := func(ev loginui.Event) bool {
		if closed.Load() {
			return false
		}
		env := loginui.Envelope{Version: 1, Type: "event", Event: &ev}
		writeMu.Lock()
		defer writeMu.Unlock()
		if closed.Load() {
			return false
		}
		select {
		case outbound <- env:
			return true
		default:
			return false
		}
	}

	// Dedicated writer goroutine ensures Win32 UI thread never blocks on pipe I/O.
	go func() {
		defer close(writerDone)
		for env := range outbound {
			if err := loginui.WriteFrame(out, env); err != nil {
				go mygo.RunOnMain(func() {
					if ctrl != nil {
						ctrl.Stop(1)
					} else {
						finish(1)
					}
				})
				return
			}
		}
	}()

	// Closing an overlay window during controlled recreation must not terminate
	// the helper; controller.Stop explicitly calls mygo.App.Quit().
	mygo.App.OnWindowAllClosed(func() {})
	mygo.App.WhenReady(func() {
		ctrl = newController(emit, finish)
		if !emit(loginui.Event{Kind: "ready"}) {
			ctrl.Stop(1)
			return
		}
		go readLoop(in, ctrl)
	})

	if err := mygo.App.Run(); err != nil {
		if !closed.Swap(true) {
			code.Store(1)
			writeMu.Lock()
			close(outbound)
			writeMu.Unlock()
		}
	}
	<-writerDone
	return int(code.Load())
}

func readLoop(in io.Reader, ctrl *controller) {
	for {
		frame, err := loginui.ReadFrame(in)
		if err != nil {
			if errors.Is(err, io.EOF) {
				mygo.RunOnMain(func() { ctrl.Stop(0) })
				return
			}
			mygo.RunOnMain(func() { ctrl.Stop(1) })
			return
		}
		switch frame.Type {
		case "model":
			model := *frame.Model
			mygo.RunOnMain(func() { ctrl.Apply(model) })
		case "window_bound":
			ev := *frame.Event
			mygo.RunOnMain(func() { ctrl.WindowBound(ev) })
		case "close":
			mygo.RunOnMain(func() { ctrl.Stop(0) })
			return
		default:
			mygo.RunOnMain(func() { ctrl.Stop(1) })
			return
		}
	}
}
