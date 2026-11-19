// SPDX-License-Identifier: GPL-3.0-or-later

package state

import "time"

// StaminaPointInterval is the official recovery cadence for 本性像素: one
// point every six minutes while the role is below the storage cap.
const StaminaPointInterval = 6 * time.Minute

// StaminaForecast is a pure estimate anchored to the role-home snapshot. The
// server reports only the current value and the cap; it does not report the
// reserve pool or the exact instant the value was sampled, so this forecast
// never invents a reserve number.
type StaminaForecast struct {
	Current int
	Max     int
	NextIn  time.Duration // time until the next point; zero when full
	FullIn  time.Duration // time until the cap; zero when full
	Full    bool
}

// ForecastStamina projects the current 本性像素 value forward from the
// snapshot time. It returns false when the server did not provide a usable
// snapshot.
func ForecastStamina(role CheckInRole, at time.Time) (StaminaForecast, bool) {
	if role.StaminaMax <= 0 || role.Stamina < 0 || role.FetchedAt.IsZero() {
		return StaminaForecast{}, false
	}
	current := min(role.Stamina, role.StaminaMax)
	f := StaminaForecast{Current: current, Max: role.StaminaMax, Full: current == role.StaminaMax}
	if f.Full {
		return f, true
	}

	elapsed := at.Sub(role.FetchedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	if recovered := int(elapsed / StaminaPointInterval); recovered > 0 {
		current += recovered
		if current >= role.StaminaMax {
			f.Current = role.StaminaMax
			f.Full = true
			return f, true
		}
		f.Current = current
		elapsed -= time.Duration(recovered) * StaminaPointInterval
	}

	f.NextIn = StaminaPointInterval - elapsed%StaminaPointInterval
	f.FullIn = f.NextIn + time.Duration(role.StaminaMax-current-1)*StaminaPointInterval
	return f, true
}
