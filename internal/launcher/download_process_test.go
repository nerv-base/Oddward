// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

type downloadObservedBackend struct {
	downloadBackendFunc
	observe func(context.Context) (bool, bool, error)
}

func (b downloadObservedBackend) ObserveDownloadProcesses(ctx context.Context) (bool, bool, error) {
	return b.observe(ctx)
}

func TestDownloadProcessHeartbeatClearsExitedOfficialWhilePaused(t *testing.T) {
	m := downloadFixture(state.DownloadSuspended, state.StageNetwork)
	m.Download.OfficialRunning = true
	m.Download.GameRunning = true
	s := NewShell(m)
	defer s.Dispose()
	calls := make(chan struct{}, 4)
	wake := make(chan struct{}, 4)
	s.RequestFrame = func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	s.GameDownloads = downloadObservedBackend{observe: func(context.Context) (bool, bool, error) { calls <- struct{}{}; return false, false, nil }}
	tt := ui.NewTester(s.View, 1280, 720)
	receiveDownload(t, calls)
	receiveDownload(t, wake)
	tt.Frame()
	if s.Model.Download.OfficialRunning || s.Model.Download.GameRunning || !state.PresentDownload(s.Model).CanResume {
		t.Fatal("exited official launcher still blocks paused task")
	}
	for range 10 {
		tt.Frame()
	}
	select {
	case <-calls:
		t.Fatal("rendering immediately re-sampled processes")
	default:
	}
	tt.Advance(2 * time.Second)
	receiveDownload(t, calls)
}

func TestDownloadProcessHeartbeatIsSingleFlightAndFencesSelection(t *testing.T) {
	for _, changeRoot := range []bool{false, true} {
		t.Run(map[bool]string{false: "token", true: "root"}[changeRoot], func(t *testing.T) {
			m := downloadFixture(state.DownloadSuspended, state.StageNetwork)
			m.Download.OfficialRunning = true
			s := NewShell(m)
			defer s.Dispose()
			calls := make(chan struct{}, 4)
			release := make(chan struct{})
			wake := make(chan struct{}, 4)
			s.RequestFrame = func() {
				select {
				case wake <- struct{}{}:
				default:
				}
			}
			s.GameDownloads = downloadObservedBackend{observe: func(ctx context.Context) (bool, bool, error) {
				calls <- struct{}{}
				select {
				case <-release:
					return false, false, nil
				case <-ctx.Done():
					return false, false, ctx.Err()
				}
			}}
			tt := ui.NewTester(s.View, 1280, 720)
			receiveDownload(t, calls)
			tt.Advance(10 * time.Second)
			select {
			case <-calls:
				t.Fatal("heartbeat started a concurrent process observation")
			default:
			}
			if changeRoot {
				s.Model.Managed.Root = `C:\synthetic\other`
			} else {
				s.Model.Download.Request++
			}
			close(release)
			receiveDownload(t, wake)
			tt.Frame()
			if !s.Model.Download.OfficialRunning {
				t.Fatal("stale process observation changed current selection")
			}
		})
	}
}

func TestDownloadProcessObservationErrorPreservesKnownFlags(t *testing.T) {
	m := downloadFixture(state.DownloadSuspended, state.StageNetwork)
	m.Download.OfficialRunning = true
	s := NewShell(m)
	defer s.Dispose()
	wake := make(chan struct{}, 1)
	s.RequestFrame = func() {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
	s.GameDownloads = downloadObservedBackend{observe: func(context.Context) (bool, bool, error) {
		return false, false, errors.New("synthetic observation unavailable")
	}}
	tt := ui.NewTester(s.View, 1280, 720)
	receiveDownload(t, wake)
	tt.Frame()
	if !s.Model.Download.OfficialRunning {
		t.Fatal("failed observation cleared known process flags")
	}
}
