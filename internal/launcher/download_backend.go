// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// GameDownloads runs native download effects off the UI thread. The callback
// carries aggregate facts; HandleDownload returns its final acknowledgement.
// Only progress events may be coalesced. All events inherit Effect.Token.
// Pause/cancel run after the prior worker has stopped, with a fresh context.
// The backend owns task identity, journal checkpoints, cleanup and system checks.
type GameDownloads interface {
	HandleDownload(context.Context, DownloadRequest, func(state.Event)) state.Event
}

// DownloadRequest snapshots UI-thread inputs before a worker starts. Settings
// is used only by EffectSaveDownloads; Root is the selected single installation.
// CacheRoot is an optional validated scratch directory for fresh installs.
type DownloadRequest struct {
	Effect    state.Effect
	Kind      state.DownloadKind
	Root      string
	CacheRoot string
	Settings  state.DownloadPreferences
}

type downloadDelivery struct {
	event state.Event
	order uint64
}

type downloadWork struct {
	offer         downloadOfferWork
	process       downloadProcessWork
	out           chan downloadDelivery
	progress      chan downloadDelivery
	sequence      atomic.Uint64
	workersMu     sync.Mutex
	workers       map[uint64]context.CancelFunc
	nextWorker    uint64
	done          <-chan struct{}
	recoveredRoot string
	closing       bool
	closeReady    chan struct{}
}

func (s *Shell) initDownloadWork() {
	if s.download.out == nil {
		s.download.out = make(chan downloadDelivery, 16)
		s.download.progress = make(chan downloadDelivery, 1)
		s.download.closeReady = make(chan struct{}, 1)
	}
}

func (s *Shell) applyDownloadEffect(f state.Effect) bool {
	if f.Kind == state.EffectDownloadOfferPoll {
		s.startDownloadOfferWork(f)
		return true
	}
	if (f.Kind < state.EffectDownloadPlan || f.Kind > state.EffectSaveDownloads) && f.Kind != state.EffectInstallCacheLocate {
		return false
	}
	s.initDownloadWork()
	if f.Kind == state.EffectSaveDownloads {
		req := DownloadRequest{Effect: f, Kind: s.Model.Download.Kind, Root: s.Model.Managed.Root, Settings: s.Model.Download.PendingSettings}
		s.startDownloadWork(req, false)
		return true
	}
	if f.Kind == state.EffectInstallLocate || f.Kind == state.EffectInstallCacheLocate {
		failed, located := state.InstallLocateFailed, state.InstallLocated
		if f.Kind == state.EffectInstallCacheLocate {
			failed, located = state.DownloadCacheLocateFailed, state.DownloadCacheLocated
		}
		if s.PickGameDirectory == nil {
			s.Dispatch(state.Event{Kind: failed, Token: f.Token, Message: "系统目录选择器不可用，请稍后重试"})
			return true
		}
		// The native picker creates a window and must run on the dispatch/UI thread.
		root, err := s.PickGameDirectory(s.ctx)
		if err != nil {
			s.Dispatch(state.Event{Kind: failed, Token: f.Token, Message: "无法选择目录，请重试"})
			return true
		}
		if root == "" {
			s.Dispatch(state.Event{Kind: located, Token: f.Token})
			return true
		}
		f.Path = root
	}
	if f.Kind == state.EffectDownloadPause || f.Kind == state.EffectDownloadCancel {
		s.download.workersMu.Lock()
		for _, stop := range s.download.workers {
			stop()
		}
		s.download.workersMu.Unlock()
	}
	req := DownloadRequest{Effect: f, Kind: s.Model.Download.Kind, Root: f.Path, CacheRoot: state.DownloadCacheRoot(s.Model)}
	if f.Kind == state.EffectInstallCacheLocate {
		req.Root = s.Model.Managed.Root
		req.CacheRoot = f.Path
	}
	if req.Root == "" {
		req.Root = s.Model.Managed.Root
	}
	s.startDownloadWork(req, true)
	return true
}

func downloadProgressEvent(k state.EventKind) bool {
	return k == state.DownloadProgress || k == state.PreloadProgress || k == state.InstallVerifyProgress || k == state.ApplyProgress
}

func (s *Shell) startDownloadWork(req DownloadRequest, serial bool) {
	ctx, cancel := context.WithCancel(s.ctx)
	previous := s.download.done
	done := make(chan struct{})
	var workerID uint64
	if serial {
		s.download.done = done
		s.download.nextWorker++
		workerID = s.download.nextWorker
		s.download.workersMu.Lock()
		if s.download.workers == nil {
			s.download.workers = make(map[uint64]context.CancelFunc)
		}
		s.download.workers[workerID] = cancel
		s.download.workersMu.Unlock()
	}
	backend, lifetime, wake := s.GameDownloads, s.ctx, s.RequestFrame
	out, progress, sequence := s.download.out, s.download.progress, &s.download.sequence
	post := func(ev state.Event, reliable bool) {
		if ev.Kind == 0 || lifetime.Err() != nil || ctx.Err() != nil {
			return
		}
		ev.Token = req.Effect.Token
		message := downloadDelivery{event: ev, order: sequence.Add(1)}
		if reliable {
			select {
			case out <- message:
			case <-ctx.Done():
				return
			case <-lifetime.Done():
				return
			}
		} else {
			if ctx.Err() != nil {
				return
			}
			select {
			case <-progress:
			default:
			}
			select {
			case progress <- message:
			default:
			}
		}
		if wake != nil {
			wake()
		}
	}
	go func() {
		defer close(done)
		defer cancel()
		if serial {
			defer func() {
				s.download.workersMu.Lock()
				delete(s.download.workers, workerID)
				s.download.workersMu.Unlock()
			}()
		}
		// A cancelled queued stage retains its place until its predecessor stops.
		if serial && previous != nil {
			select {
			case <-previous:
			case <-lifetime.Done():
				return
			}
		}
		if ctx.Err() != nil {
			return
		}
		if backend == nil {
			kind := state.DownloadFailed
			switch req.Effect.Kind {
			case state.EffectInstallCacheLocate:
				kind = state.DownloadCacheLocateFailed
			case state.EffectInstallLocate:
				kind = state.InstallLocateFailed
			case state.EffectInstallAdopt:
				kind = state.InstallAdoptFailed
			case state.EffectInstallVerify:
				kind = state.InstallVerifyFailed
			case state.EffectDownloadPlan:
				kind = state.DownloadPlanFailed
			case state.EffectSaveDownloads:
				kind = state.DownloadSettingsFailed
			}
			post(state.Event{Kind: kind, Message: "游戏下载服务不可用，请稍后重试"}, true)
			return
		}
		result := backend.HandleDownload(ctx, req, func(ev state.Event) {
			if ctx.Err() != nil {
				return
			}
			post(ev, !downloadProgressEvent(ev.Kind))
		})
		// A cancelled worker cannot publish later success. Its fresh control effect
		// reports the checkpoint/cleanup ACK after this worker has fully returned.
		post(result, true)
	}()
}

func (s *Shell) syncDownloads() {
	if s.download.out == nil {
		if s.GameDownloads == nil {
			return
		}
		s.initDownloadWork()
	}
	// Bound each UI turn even when a worker continuously reports progress.
	limit := len(s.download.out) + len(s.download.progress)
	events := make([]downloadDelivery, 0, limit)
collect:
	for range limit {
		select {
		case ev := <-s.download.out:
			events = append(events, ev)
		case ev := <-s.download.progress:
			events = append(events, ev)
		default:
			break collect
		}
	}
	// Coalesced progress retains its place relative to reliable stage/result
	// events, including Started -> Progress -> Verified in one native frame.
	sort.Slice(events, func(i, j int) bool { return events[i].order < events[j].order })
	for _, ev := range events {
		s.Dispatch(ev.event)
	}
	select {
	case <-s.download.closeReady:
		s.finishCloseWindow()
		return
	default:
	}
	root := s.Model.Managed.Root
	if s.GameDownloads != nil && s.Model.NormalMode && root != "" && s.download.recoveredRoot != root && s.Model.Download.Request == 0 {
		s.download.recoveredRoot = root
		s.Dispatch(state.Event{Kind: state.DownloadRecoveryScanRequest})
	}
}

func (s *Shell) closeAfterDownloads() {
	if s.download.closing {
		return
	}
	if s.download.done == nil {
		s.finishCloseWindow()
		return
	}
	select {
	case <-s.download.done:
		s.finishCloseWindow()
		return
	default:
	}
	s.download.closing = true
	done, ready, wake := s.download.done, s.download.closeReady, s.RequestFrame
	go func() {
		timer := time.NewTimer(3 * time.Second)
		defer timer.Stop()
		select {
		case <-done:
		case <-timer.C:
		}
		ready <- struct{}{}
		if wake != nil {
			wake()
		}
	}()
}

func (s *Shell) finishCloseWindow() {
	s.cancel()
	if s.CloseWindow != nil {
		s.CloseWindow()
	}
}
