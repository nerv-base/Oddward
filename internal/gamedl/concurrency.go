// SPDX-License-Identifier: GPL-3.0-or-later
package gamedl

import (
	"context"
	"sync"
)

type concurrencyLimits struct {
	mu                              sync.Mutex
	files, connections, activeFiles int
	changed                         chan struct{}
}

func (l *concurrencyLimits) wakeLocked() {
	if l.changed != nil {
		close(l.changed)
	}
	l.changed = make(chan struct{})
}

// SetConcurrencyLimits updates admission limits within the configured ceilings.
// Existing files and requests drain normally. Waiting admissions wake on changes
// or cancellation; the setter is safe during Run and from progress callbacks.
func (e *Engine) SetConcurrencyLimits(files, connections int) error {
	if files < 1 || files > e.options.Parameters.FilesInFlight || connections < 1 || connections > e.options.Parameters.ConnectionsPerFile {
		return failure(ErrPathUnsafe, "concurrency limits", "", nil)
	}
	l := &e.admission
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.files != files || l.connections != connections {
		l.files = files
		l.connections = connections
		l.wakeLocked()
	}
	return nil
}
func (l *concurrencyLimits) acquire(ctx context.Context, f *fileTransfer) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		l.mu.Lock()
		available := l.activeFiles < l.files
		if f != nil {
			available = f.activeRequests < l.connections
		}
		if available {
			if f == nil {
				l.activeFiles++
			} else {
				f.activeRequests++
			}
			l.mu.Unlock()
			return nil
		}
		if l.changed == nil {
			l.changed = make(chan struct{})
		}
		changed := l.changed
		l.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-changed:
		}
	}
}
func (l *concurrencyLimits) release(f *fileTransfer) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if f == nil {
		l.activeFiles--
	} else {
		f.activeRequests--
	}
	l.wakeLocked()
}
