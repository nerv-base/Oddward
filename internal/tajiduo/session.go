// SPDX-License-Identifier: GPL-3.0-or-later
package tajiduo

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
)

// Session serializes token rotation. The persistence callback must atomically
// store the grant and must not call Access on this session. Grant/NeedsRelink
// remain safe to inspect during persistence. A cancelled waiter does not cancel
// another caller's refresh; the initiating caller owns that request's context.
type Session struct {
	mu          sync.Mutex
	client      *Client
	grant       Grant
	access      Access
	persist     func(Grant) error
	flight      *refreshFlight
	needsRelink bool
	terminal    error
}
type refreshFlight struct {
	done   chan struct{}
	access Access
	err    error
}

func NewSession(c *Client, g Grant, persist func(Grant) error) *Session {
	return &Session{client: c, grant: g, persist: persist}
}

// Access refreshes only when needed. A persistence failure returns a usable
// process-local Access alongside ErrNeedsRelink on this and subsequent calls.
func (s *Session) Access(ctx context.Context) (Access, error) {
	if e := ctx.Err(); e != nil {
		return Access{}, e
	}
	s.mu.Lock()
	if s.flight != nil {
		f := s.flight
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return Access{}, ctx.Err()
		case <-f.done:
			return f.access, f.err
		}
	}
	if s.terminal != nil {
		e := s.terminal
		s.mu.Unlock()
		return Access{}, e
	}
	if s.client == nil {
		s.mu.Unlock()
		return Access{}, ErrInvalidInput
	}
	if s.access.Token != "" && s.client.now().Before(s.access.ExpiresAt) {
		a := s.access
		var e error
		if s.needsRelink {
			e = ErrNeedsRelink
		}
		s.mu.Unlock()
		return a, e
	}
	f := &refreshFlight{done: make(chan struct{})}
	s.flight = f
	g := s.grant
	s.mu.Unlock()
	next, a, e := s.client.Refresh(ctx, g)
	persistenceFailed := false
	if e == nil {
		if s.persist == nil || s.persist(next) != nil {
			persistenceFailed = true
			e = ErrNeedsRelink
		}
	}
	s.mu.Lock()
	if e == nil || persistenceFailed {
		s.grant = next
		s.access = a
	}
	if persistenceFailed || errors.Is(e, ErrAuthExpired) {
		s.needsRelink = true
	}
	// Expired authentication is terminal: never send another refresh or re-login.
	if errors.Is(e, ErrAuthExpired) {
		s.terminal = ErrAuthExpired
	}
	if s.needsRelink && e == nil {
		e = ErrNeedsRelink
	}
	f.access = a
	f.err = e
	s.flight = nil
	close(f.done)
	s.mu.Unlock()
	return a, e
}
func (s *Session) NeedsRelink() bool          { s.mu.Lock(); defer s.mu.Unlock(); return s.needsRelink }
func (s *Session) Grant() Grant               { s.mu.Lock(); defer s.mu.Unlock(); return s.grant }
func (s *Session) String() string             { return "tajiduo.Session{redacted}" }
func (s *Session) GoString() string           { return s.String() }
func (s *Session) Format(f fmt.State, v rune) { io.WriteString(f, s.String()) }
func (s *Session) LogValue() slog.Value       { return slog.StringValue(s.String()) }
