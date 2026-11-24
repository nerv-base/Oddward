// SPDX-License-Identifier: GPL-3.0-or-later
package loginui

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	"github.com/nerv-base/Oddward/internal/process"
)

var ErrClosed = errors.New("login UI closed")
var ErrHelper = errors.New("login UI helper failed")

// Config names the trusted, packaged helper. No user data is passed on its command line.
type Config struct {
	Executable string
	// Args holds fixed internal role arguments (for example []string{Mode} when
	// invoking the launcher executable in its isolated login UI helper role).
	// Callers must never pass user input, phone numbers, tokens or paths here.
	Args []string
	// BindWindow authenticates an owned helper's window before the elevated
	// host associates it with the game. The helper never supplies its own PID.
	BindWindow func(context.Context, process.Identity, uintptr, uintptr) error
	// EmbedWindow permits a child relationship only after the host checks DPI
	// compatibility; it may retain a native owner relationship instead.
	EmbedWindow func(context.Context, process.Identity, uintptr, uintptr) error
}
type update struct {
	frame  envelope
	result chan error
}
type Session struct {
	ctx     context.Context
	cancel  context.CancelFunc
	events  chan Event
	updates chan update
	done    chan struct{}
	mu      sync.Mutex
	err     error
}

// Start inherits only the three explicit standard handles (Go's Windows process
// implementation supplies PROC_THREAD_ATTRIBUTE_HANDLE_LIST). stderr goes to NUL.
// It owns only the helper process handle, never a game process or process tree.
func Start(ctx context.Context, c Config, initial Model) (*Session, error) {
	if err := validateModel(initial); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(c.Executable) {
		return nil, ErrHelper
	}
	input, childInput, err := newInput()
	if err != nil {
		return nil, ErrHelper
	}
	output, childOutput, err := os.Pipe()
	if err != nil {
		input.Close()
		childInput.Close()
		return nil, ErrHelper
	}
	null, err := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	if err != nil {
		input.Close()
		childInput.Close()
		output.Close()
		childOutput.Close()
		return nil, ErrHelper
	}
	cmd := exec.Command(c.Executable, c.Args...)
	cmd.Stdin = childInput
	cmd.Stdout = childOutput
	cmd.Stderr = null
	err = cmd.Start()
	childInput.Close()
	childOutput.Close()
	null.Close()
	if err != nil {
		input.Close()
		output.Close()
		return nil, ErrHelper
	}
	var identity process.Identity
	if c.BindWindow != nil || c.EmbedWindow != nil {
		identity, err = helperIdentity(cmd.Process.Pid)
		if err != nil {
			input.Close()
			output.Close()
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			return nil, ErrHelper
		}
	}
	ownCtx, cancel := context.WithCancel(ctx)
	s := &Session{ctx: ownCtx, cancel: cancel, events: make(chan Event, 32), updates: make(chan update), done: make(chan struct{})}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for {
			select {
			case <-ownCtx.Done():
				return
			case u := <-s.updates:
				if ownCtx.Err() != nil {
					u.result <- ErrClosed
					return
				}
				err := writeFrame(input, u.frame)
				if err != nil {
					if ownCtx.Err() != nil {
						u.result <- ErrClosed
					} else {
						u.result <- ErrHelper
						s.fail(ErrHelper)
					}
					return
				}
				u.result <- nil
			}
		}
	}()
	// EOF requests graceful close. A hung helper is terminated after two seconds;
	// Process.Kill addresses only this owned helper handle, not any descendants.
	go func() { <-ownCtx.Done(); input.Close() }()
	go func() {
		defer close(s.done)
		defer close(s.events)
		defer output.Close()
		readDone := make(chan error, 1)
		go func() {
			var lastRequestID uint64
			for {
				m, err := readFrame(output)
				if err != nil {
					readDone <- err
					return
				}
				if m.Type != "event" || m.Event == nil {
					readDone <- ErrProtocol
					return
				}
				if m.Event.Kind == "bind_overlay" || m.Event.Kind == "embed_overlay" {
					requestID, valid := windowRequestID(m.Event.RequestID)
					if !valid || requestID <= lastRequestID {
						readDone <- ErrProtocol
						return
					}
					lastRequestID = requestID
					bind := c.BindWindow
					if m.Event.Kind == "embed_overlay" {
						bind = c.EmbedWindow
					}
					if bind == nil {
						readDone <- ErrHelper
						return
					}
					window, _ := nativeWindow(m.Event.Window)
					gameWindow, _ := nativeWindow(m.Event.GameWindow)
					bindCtx, stop := context.WithTimeout(ownCtx, 5*time.Second)
					err := bind(bindCtx, identity, window, gameWindow)
					if err == nil {
						err = bindCtx.Err()
					}
					stop()
					if err != nil {
						readDone <- ErrHelper
						return
					}
					// Only a completed host callback permits the helper to show its
					// window. Share the model writer so frame bytes cannot interleave.
					if err := s.write(ownCtx, envelope{Version: 1, Type: "window_bound", Event: m.Event}); err != nil {
						readDone <- ErrHelper
						return
					}
					continue
				}
				select {
				case s.events <- *m.Event:
				case <-ownCtx.Done():
					readDone <- nil
					return
				}
			}
		}()
		readComplete := false
		var callerCanceled bool
		select {
		case readErr := <-readDone:
			readComplete = true
			callerCanceled = ownCtx.Err() != nil
			if readErr != nil && !errors.Is(readErr, io.EOF) && !callerCanceled {
				s.fail(ErrHelper)
			}
			s.cancel()
		case <-ownCtx.Done():
			callerCanceled = true
		}
		select {
		case err := <-exited:
			if err != nil && !callerCanceled {
				s.fail(ErrHelper)
			}
		case <-time.After(2 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
		output.Close()
		if !readComplete {
			<-readDone
		}
		<-writerDone
	}()
	if err := s.Update(ctx, initial); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}
func newInput() (*os.File, *os.File, error) { r, w, err := os.Pipe(); return w, r, err }
func (s *Session) fail(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
	s.cancel()
}

// Update serializes snapshots; cancellation closes a blocked pipe rather than
// leaving a goroutine with user data. Callers should serialize backend effects.
func (s *Session) Update(ctx context.Context, m Model) error {
	return s.write(ctx, envelope{Version: 1, Type: "model", Model: &m})
}

// write serializes every outbound frame, including internal window acknowledgements.
func (s *Session) write(ctx context.Context, frame envelope) error {
	if err := validateEnvelope(frame); err != nil {
		return err
	}
	if s.ctx.Err() != nil {
		return ErrClosed
	}
	u := update{frame: frame, result: make(chan error, 1)}
	select {
	case s.updates <- u:
	case <-ctx.Done():
		return ctx.Err()
	case <-s.ctx.Done():
		return ErrClosed
	}
	select {
	case err := <-u.result:
		return err
	case <-ctx.Done():
		s.cancel()
		return ctx.Err()
	case <-s.ctx.Done():
		return ErrClosed
	}
}
func (s *Session) Events() <-chan Event  { return s.events }
func (s *Session) Done() <-chan struct{} { return s.done }
func (s *Session) Err() error            { s.mu.Lock(); defer s.mu.Unlock(); return s.err }
func (s *Session) Close() error          { s.cancel(); <-s.done; return s.Err() }
