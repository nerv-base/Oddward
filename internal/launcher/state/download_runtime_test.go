// SPDX-License-Identifier: GPL-3.0-or-later

package state

import (
	"testing"
	"time"
)

func TestGameObservationRecordsAndClearsTheStart(t *testing.T) {
	m := Initial()
	m.NormalMode = true
	m.Managed.Root = `C:\fixture\nte`
	m.Download.Request = 3
	start := time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)
	m, _ = Reduce(m, Event{Kind: DownloadProcessesObserved, Token: 3, Focused: true, Since: start})
	if !m.Download.GameRunning || !m.Download.GameSince.Equal(start) {
		t.Fatalf("game observation = %+v", m.Download)
	}
	// Later observations keep the first start instead of sliding it.
	m, _ = Reduce(m, Event{Kind: DownloadProcessesObserved, Token: 3, Focused: true, Since: start.Add(time.Minute)})
	if !m.Download.GameSince.Equal(start) {
		t.Fatalf("start slid to %v", m.Download.GameSince)
	}
	m, _ = Reduce(m, Event{Kind: DownloadProcessesObserved, Token: 3})
	if m.Download.GameRunning || !m.Download.GameSince.IsZero() {
		t.Fatalf("exit did not clear: %+v", m.Download)
	}
}

func TestGameObservationIgnoresStaleTokens(t *testing.T) {
	m := Initial()
	m.NormalMode = true
	m.Download.Request = 4
	m, _ = Reduce(m, Event{Kind: DownloadProcessesObserved, Token: 3, Focused: true, Since: time.Now()})
	if m.Download.GameRunning || !m.Download.GameSince.IsZero() {
		t.Fatalf("stale observation applied: %+v", m.Download)
	}
}
