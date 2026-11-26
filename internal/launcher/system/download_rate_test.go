// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"testing"
	"time"
)

func TestThroughputMeterSlidingWindowAndWarmup(t *testing.T) {
	clock := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	m := &throughputMeter{now: func() time.Time { return clock }}

	if rate, eta := m.Snapshot(0, 100<<20); rate != 0 || eta != 0 {
		t.Fatalf("unstarted meter reported rate=%d eta=%d", rate, eta)
	}
	m.Start()
	m.Record(2 << 20)
	clock = clock.Add(40 * time.Millisecond)
	if rate, eta := m.Snapshot(2<<20, 100<<20); rate != 0 || eta != 0 {
		t.Fatalf("sub-100ms warmup spiked rate=%d eta=%d", rate, eta)
	}

	// After 1 second with 10 MiB recorded, rate should be 10 MiB/s and remaining 90 MiB -> 9s.
	m.Record(8 << 20)
	clock = clock.Add(960 * time.Millisecond) // total elapsed = 1s
	rate, eta := m.Snapshot(10<<20, 100<<20)
	if rate != 10<<20 || eta != 9 {
		t.Fatalf("1s snapshot got rate=%d eta=%d, want %d and 9", rate, eta, 10<<20)
	}

	// Advance past the 5s window so early 10 MiB ages out; record 25 MiB across 5s -> 5 MiB/s.
	for range 5 {
		clock = clock.Add(time.Second)
		m.Record(5 << 20)
	}
	rate, eta = m.Snapshot(35<<20, 100<<20)
	if rate != 5<<20 || eta != 13 {
		t.Fatalf("sliding window got rate=%d eta=%d, want %d and 13", rate, eta, 5<<20)
	}

	// Completion must never report a positive ETA.
	rate, eta = m.Snapshot(100<<20, 100<<20)
	if rate != 5<<20 || eta != 0 {
		t.Fatalf("completed snapshot got rate=%d eta=%d, want %d and 0", rate, eta, 5<<20)
	}

	// Silence past 5s window drops throughput to zero.
	clock = clock.Add(6 * time.Second)
	if rate, eta := m.Snapshot(35<<20, 100<<20); rate != 0 || eta != 0 {
		t.Fatalf("idle window kept stale rate=%d eta=%d", rate, eta)
	}
}
