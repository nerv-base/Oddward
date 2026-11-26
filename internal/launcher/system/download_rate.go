// SPDX-License-Identifier: GPL-3.0-or-later

package system

import (
	"math"
	"time"
)

const (
	throughputBucketWidth = 100 * time.Millisecond
	throughputWindow      = 5 * time.Second
	throughputMinElapsed  = 100 * time.Millisecond
	throughputMinETARate  = 16 << 10
)

type throughputBucket struct {
	tick  int64
	bytes int64
}

// throughputMeter measures real byte throughput over a 5-second sliding window.
// Callers must only pass bytes genuinely read or written so cached/skipped work
// never inflates reported speed.
type throughputMeter struct {
	now     func() time.Time
	start   time.Time
	started bool
	samples [50]throughputBucket
}

func newThroughputMeter() *throughputMeter {
	return &throughputMeter{now: time.Now}
}

func (m *throughputMeter) clock() time.Time {
	if m.now != nil {
		return m.now()
	}
	return time.Now()
}

func (m *throughputMeter) Start() {
	if !m.started {
		m.start = m.clock()
		m.started = true
	}
}

func (m *throughputMeter) Record(n int64) {
	if n <= 0 {
		return
	}
	now := m.clock()
	if !m.started {
		m.start = now
		m.started = true
	}
	elapsed := now.Sub(m.start)
	if elapsed < 0 {
		return
	}
	tick := int64(elapsed / throughputBucketWidth)
	sample := &m.samples[tick%int64(len(m.samples))]
	if sample.tick != tick {
		*sample = throughputBucket{tick: tick}
	}
	sample.bytes += n
}

func (m *throughputMeter) Snapshot(done, total int64) (rate int64, eta int32) {
	if !m.started {
		return 0, 0
	}
	now := m.clock()
	elapsed := now.Sub(m.start)
	if elapsed < throughputMinElapsed {
		return 0, 0
	}
	tick := int64(elapsed / throughputBucketWidth)
	var bytes int64
	for _, s := range m.samples {
		if s.bytes > 0 && tick-s.tick >= 0 && tick-s.tick < int64(len(m.samples)) {
			bytes += s.bytes
		}
	}
	if bytes <= 0 {
		return 0, 0
	}
	window := min(elapsed, throughputWindow).Seconds()
	if window <= 0 {
		return 0, 0
	}
	speed := float64(bytes) / window
	if speed <= 0 || speed >= math.MaxInt64 {
		return 0, 0
	}
	rate = int64(speed)
	if rate >= throughputMinETARate && total > done {
		eta = int32(min(int64(float64(total-done)/speed), math.MaxInt32))
	}
	return rate, eta
}
