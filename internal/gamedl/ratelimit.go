// SPDX-License-Identifier: GPL-3.0-or-later
package gamedl

import (
	"context"
	"sync"
	"time"
)

// rateLimiter paces reservations across every file and connection. There is no
// initial per-connection burst and cancellation interrupts the wait.
type rateLimiter struct {
	mu             sync.Mutex
	next           time.Time
	bytesPerSecond int64
	changed        chan struct{}
}

// SetRateLimitKiB changes the shared download budget without waiting for Run.
// Zero means unlimited. In-flight rate waits observe changes immediately.
func (e *Engine) SetRateLimitKiB(rate int64) error {
	if rate < 0 || rate > 1048576 {
		return failure(ErrPathUnsafe, "rate limit", "", nil)
	}
	l := &e.limiter
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.bytesPerSecond == rate*1024 {
		return nil
	}
	l.bytesPerSecond = rate * 1024
	l.next = time.Time{}
	if l.changed != nil {
		close(l.changed)
	}
	l.changed = make(chan struct{})
	return nil
}
func (l *rateLimiter) wait(ctx context.Context, n int) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		l.mu.Lock()
		if l.bytesPerSecond == 0 || n == 0 {
			l.mu.Unlock()
			return ctx.Err()
		}
		if l.changed == nil {
			l.changed = make(chan struct{})
		}
		changed := l.changed
		now := time.Now()
		if l.next.Before(now) {
			l.next = now
		}
		l.next = l.next.Add(time.Duration(float64(n) * float64(time.Second) / float64(l.bytesPerSecond)))
		delay := time.Until(l.next)
		l.mu.Unlock()
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-changed:
			timer.Stop()
			continue
		case <-timer.C:
			l.mu.Lock()
			same := changed == l.changed
			l.mu.Unlock()
			if same {
				return ctx.Err()
			}
		}
	}
}
