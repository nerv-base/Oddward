// SPDX-License-Identifier: GPL-3.0-or-later

package state

// FeatureState is a manually advanced, in-memory preview, never a service status.
// Its zero value is an honest empty preview; scalar fields keep Model comparable.
type FeatureState uint8

const (
	FeatureEmpty FeatureState = iota
	FeatureLoading
	FeatureReady
	FeatureFailed
)

const (
	StartFeature EventKind = 100 + iota
	CompleteFeature
	FailFeature
	ResetFeature
)

// reduceFeature consumes only feature intents. Rejected feature events are still
// handled, so they cannot fall through to the game reducer. No I/O or effects.
func ReduceFeature(m Model, e Event) (Model, bool) {
	switch e.Kind {
	case StartFeature, CompleteFeature, FailFeature, ResetFeature:
	default:
		return m, false
	}
	if m.Closed || m.Overlay != NoOverlay || e.Page != m.Page {
		return m, true
	}
	if m.Page != CheckIn {
		return m, true
	}
	current := &m.CheckIn
	switch e.Kind {
	case StartFeature:
		if *current == FeatureEmpty || *current == FeatureFailed {
			*current = FeatureLoading
		}
	case CompleteFeature:
		if *current == FeatureLoading {
			*current = FeatureReady
		}
	case FailFeature:
		if *current == FeatureLoading {
			*current = FeatureFailed
		}
	case ResetFeature:
		*current = FeatureEmpty
	}
	return m, true
}
