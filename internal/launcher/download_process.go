// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"context"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// DownloadProcessObserver optionally supplies fresh process facts independently
// of download workers, including tasks paused by the official launcher.
// The system implementation owns process discovery and identity checks.
type DownloadProcessObserver interface {
	ObserveDownloadProcesses(context.Context) (official, game bool, err error)
}

const downloadProcessInterval = 2 * time.Second

type downloadProcessObservation struct {
	root           string
	token          uint64
	official, game bool
	err            error
}

type downloadProcessWork struct {
	out     chan downloadProcessObservation
	pending bool
	next    time.Time
}

func (s *Shell) syncDownloadProcesses(c *ui.Context) {
	work := &s.download.process
	if work.out != nil {
		select {
		case result := <-work.out:
			work.pending = false
			if result.err == nil && result.root == s.Model.Managed.Root && result.token == s.Model.Download.Request && result.token != 0 {
				s.Dispatch(state.Event{Kind: state.DownloadProcessesObserved, Token: result.token, Enabled: result.official, Focused: result.game, Since: time.Now()})
			}
		default:
		}
	}
	observer, ok := s.GameDownloads.(DownloadProcessObserver)
	if !ok || !s.Model.NormalMode || s.Model.Managed.Root == "" || s.Model.Download.Request == 0 || s.ctx.Err() != nil {
		return
	}
	if work.pending {
		c.After(downloadProcessInterval)
		return
	}
	if !work.next.IsZero() && s.now.Before(work.next) {
		c.After(work.next.Sub(s.now))
		return
	}
	if work.out == nil {
		work.out = make(chan downloadProcessObservation, 1)
	}
	work.pending = true
	work.next = s.now.Add(downloadProcessInterval)
	c.After(downloadProcessInterval)
	root, token, out, lifetime, wake := s.Model.Managed.Root, s.Model.Download.Request, work.out, s.ctx, s.RequestFrame
	go func() {
		ctx, cancel := context.WithTimeout(lifetime, downloadProcessInterval)
		defer cancel()
		official, game, err := observer.ObserveDownloadProcesses(ctx)
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		result := downloadProcessObservation{root: root, token: token, official: official, game: game, err: err}
		select {
		case out <- result:
		case <-lifetime.Done():
			return
		}
		if wake != nil {
			wake()
		}
	}()
}
