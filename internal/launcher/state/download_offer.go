// SPDX-License-Identifier: GPL-3.0-or-later

package state

// CanPollDownloadOffer shares idle eligibility with the shell. Cadence, positive
// jitter and the check-only network operation belong to the shell and system.
func CanPollDownloadOffer(m Model) bool {
	return !m.Download.OfferPending && downloadOfferIdle(m)
}
func downloadOfferIdle(m Model) bool {
	d := m.Download
	phaseOK := d.Phase == DownloadIdle || d.Phase == DownloadDone ||
		d.Kind == DownloadPreload && (d.Phase == DownloadStale || d.Phase == DownloadError)
	preloadOK := d.Preload == PreloadStateUnavailable || d.Preload == PreloadStateAvailable ||
		d.Preload == PreloadStateStaged || d.Preload == PreloadStateWaitingRelease ||
		d.Preload == PreloadStateApplicable || d.Preload == PreloadStateCompleted ||
		d.Preload == PreloadStateStale || d.Preload == PreloadStateFailed
	return m.NormalMode && m.Managed.Root != "" && (d.Located || d.Verified) && phaseOK && !downloadUnfinished(d) && !ManagedBusy(m.Managed.Phase) && !ManagedActive(m.Managed.Phase) && m.Flow != Launching && m.Flow != Running && !d.GameRunning && !d.OfficialRunning && preloadOK
}
func reduceDownloadOffer(m Model, e Event) (Model, []Effect, bool) {
	d := &m.Download
	switch e.Kind {
	case DownloadOfferPollRequest:
		if !CanPollDownloadOffer(m) {
			break
		}
		d.OfferRequest++
		d.OfferPending = true
		d.offerRoot = m.Managed.Root
		d.offerTaskRequest = d.Request
		d.offerManagedRequest = m.Managed.Request
		return m, []Effect{{Kind: EffectDownloadOfferPoll, Token: d.OfferRequest, Path: d.offerRoot}}, true
	case DownloadOfferPolled, DownloadOfferPollFailed:
		if !d.OfferPending || e.Token == 0 || e.Token != d.OfferRequest || e.Path == "" || !SameManagedRoot(e.Path, d.offerRoot) {
			break
		}
		d.OfferPending = false
		if e.Kind == DownloadOfferPollFailed || !SameManagedRoot(m.Managed.Root, d.offerRoot) || d.Request != d.offerTaskRequest || m.Managed.Request != d.offerManagedRequest || !downloadOfferIdle(m) {
			break
		}
		d.OfferVersion = e.Message
		d.PreloadOfferVersion = ""
		d.offerResultRoot = m.Managed.Root
		if e.Enabled {
			d.PreloadOfferVersion = e.Body
		}
	default:
		return m, nil, false
	}
	return m, nil, true
}
