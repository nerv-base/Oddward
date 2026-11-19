// SPDX-License-Identifier: GPL-3.0-or-later

package state

import (
	"testing"
	"time"
)

func TestForecastStamina(t *testing.T) {
	base := time.Date(2026, 11, 19, 12, 0, 0, 0, time.Local)
	role := func(current, max int, fetched time.Time) CheckInRole {
		return CheckInRole{Stamina: current, StaminaMax: max, FetchedAt: fetched}
	}
	tests := []struct {
		name    string
		role    CheckInRole
		at      time.Time
		ok      bool
		current int
		full    bool
		nextIn  time.Duration
		fullIn  time.Duration
	}{
		{name: "full", role: role(320, 320, base), at: base, ok: true, current: 320, full: true},
		{name: "one point away", role: role(319, 320, base), at: base, ok: true, current: 319, nextIn: 6 * time.Minute, fullIn: 6 * time.Minute},
		{name: "two points away", role: role(318, 320, base), at: base, ok: true, current: 318, nextIn: 6 * time.Minute, fullIn: 12 * time.Minute},
		{name: "five minutes later", role: role(318, 320, base), at: base.Add(5 * time.Minute), ok: true, current: 318, nextIn: time.Minute, fullIn: 7 * time.Minute},
		{name: "one point recovered", role: role(318, 320, base), at: base.Add(6 * time.Minute), ok: true, current: 319, nextIn: 6 * time.Minute, fullIn: 6 * time.Minute},
		{name: "recovered to cap", role: role(318, 320, base), at: base.Add(12 * time.Minute), ok: true, current: 320, full: true},
		{name: "stale snapshot", role: role(0, 320, base), at: base.Add(32 * time.Hour), ok: true, current: 320, full: true},
		{name: "clock before snapshot", role: role(300, 320, base), at: base.Add(-time.Hour), ok: true, current: 300, nextIn: 6 * time.Minute, fullIn: 120 * time.Minute},
		{name: "no snapshot time", role: role(300, 320, time.Time{}), at: base, ok: false},
		{name: "no cap", role: role(300, 0, base), at: base, ok: false},
		{name: "negative value", role: role(-1, 320, base), at: base, ok: false},
		{name: "value above cap", role: role(400, 320, base), at: base, ok: true, current: 320, full: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := ForecastStamina(tt.role, tt.at)
			if ok != tt.ok {
				t.Fatalf("ok=%v, want %v", ok, tt.ok)
			}
			if !ok {
				return
			}
			if got.Current != tt.current || got.Full != tt.full || got.NextIn != tt.nextIn || got.FullIn != tt.fullIn {
				t.Fatalf("forecast=%+v, want current=%d full=%v next=%v fullIn=%v", got, tt.current, tt.full, tt.nextIn, tt.fullIn)
			}
		})
	}
}
