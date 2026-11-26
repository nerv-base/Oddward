// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"context"
	"math/rand/v2"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

const (
	downloadOfferMinimumInterval = 10 * time.Minute
	downloadOfferMaximumJitter   = 2 * time.Minute
	downloadOfferTimeout         = 30 * time.Second
)

type downloadOfferResult struct {
	event state.Event
	root  string
	task  uint64
}

type downloadOfferWork struct {
	out     chan downloadOfferResult
	pending bool
	next    time.Time
	root    string
}

func downloadOfferDelay() time.Duration {
	return downloadOfferMinimumInterval + time.Second + time.Duration(rand.Int64N(int64(downloadOfferMaximumJitter-time.Second)))
}

// Offer polling has a separate bounded mailbox and never joins the installation
// worker chain. Its check-only backend result cannot schedule plans or downloads.
func (s *Shell) startDownloadOfferWork(f state.Effect) {
	work := &s.download.offer
	if work.pending {
		return
	}
	if work.out == nil {
		work.out = make(chan downloadOfferResult, 1)
	}
	work.pending = true
	work.next = time.Time{}
	root, task, backend, out, lifetime, wake := f.Path, s.Model.Download.Request, s.GameDownloads, work.out, s.ctx, s.RequestFrame
	if root == "" {
		root = s.Model.Managed.Root
	}
	req := DownloadRequest{Effect: f, Kind: state.DownloadPreload, Root: root}
	go func() {
		ctx, cancel := context.WithTimeout(lifetime, downloadOfferTimeout)
		defer cancel()
		result := state.Event{Kind: state.DownloadOfferPollFailed}
		if backend != nil {
			result = backend.HandleDownload(ctx, req, func(state.Event) {})
		}
		if ctx.Err() != nil || result.Kind != state.DownloadOfferPolled && result.Kind != state.DownloadOfferPollFailed {
			result = state.Event{Kind: state.DownloadOfferPollFailed}
		}
		result.Token = f.Token
		result.Path = root
		select {
		case out <- downloadOfferResult{event: result, root: root, task: task}:
		case <-lifetime.Done():
			return
		}
		if wake != nil {
			wake()
		}
	}()
}

func (s *Shell) syncDownloadOffers(c *ui.Context) {
	work := &s.download.offer
	if work.out != nil {
		select {
		case result := <-work.out:
			work.pending = false
			work.next = time.Time{}
			// Even stale results acknowledge the independent poll, clearing its latch.
			// They cannot replace an offer after a task or selection changed meanwhile.
			if result.root != s.Model.Managed.Root || result.task != s.Model.Download.Request {
				result.event.Kind = state.DownloadOfferPollFailed
			}
			s.Dispatch(result.event)
		default:
		}
	}
	if s.GameDownloads == nil || s.ctx.Err() != nil || !state.CanPollDownloadOffer(s.Model) || work.pending {
		return
	}
	root := s.Model.Managed.Root
	if work.root != root {
		work.root = root
		work.next = time.Time{}
		// The startup recovery scan has checked this root for unfinished work.
		// Discover its first offer as soon as the state becomes idle.
		if s.download.recoveredRoot == root {
			work.next = s.now
		}
	}
	if work.next.IsZero() {
		work.next = s.now.Add(downloadOfferDelay())
	}
	if s.now.Before(work.next) {
		c.After(work.next.Sub(s.now))
		return
	}
	s.Dispatch(state.Event{Kind: state.DownloadOfferPollRequest})
}
