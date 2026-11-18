// SPDX-License-Identifier: GPL-3.0-or-later

package state

// DownloadCacheRoot binds a validated fresh-install scratch choice to the
// installation selected when validation completed. It is never an update path.
func DownloadCacheRoot(m Model) string {
	d := m.Download
	if d.CacheRoot == "" || d.Current != "" || d.Kind != DownloadInstall || !SameManagedRoot(d.cacheInstallRoot, m.Managed.Root) {
		return ""
	}
	return d.CacheRoot
}

func canSelectDownloadCache(m Model) bool {
	d := m.Download
	return m.NormalMode && m.Managed.Root != "" && d.Current == "" && d.Kind != DownloadPreload && !downloadBusy(d) && DownloadWriteBlocked(m) == "" && (d.Phase == DownloadIdle || d.Phase == DownloadError || d.Phase == DownloadStale || d.Phase == DownloadPreparing && d.AwaitingConfirmation) && (d.Preload == PreloadStateUnavailable || d.Preload == PreloadStateCompleted)
}

func reduceDownloadCache(m Model, e Event) (Model, []Effect, bool) {
	d := &m.Download
	switch e.Kind {
	case DownloadCacheSelect:
		if !canSelectDownloadCache(m) {
			break
		}
		d.Request++
		d.operation = downloadOperationCacheLocate
		d.cacheSelectionRoot = m.Managed.Root
		d.Error = ""
		return m, []Effect{{Kind: EffectInstallCacheLocate, Token: d.Request, Path: m.Managed.Root}}, true
	case DownloadCacheLocated, DownloadCacheLocateFailed:
		if e.Token == 0 || e.Token != d.Request || d.operation != downloadOperationCacheLocate {
			break
		}
		d.operation = downloadOperationNone
		if !SameManagedRoot(d.cacheSelectionRoot, m.Managed.Root) {
			break
		}
		if e.Kind == DownloadCacheLocateFailed {
			d.Error = e.Message
			break
		}
		if e.Path == "" {
			break
		}
		d.CacheRoot, d.cacheInstallRoot = e.Path, m.Managed.Root
		d.Kind = DownloadInstall
		d.Phase, d.Stage = DownloadIdle, StageIdle
		d.Version, d.Note, d.Error = "", "", ""
		d.Route = RouteUnknown
		d.FallbackNote = false
		d.AwaitingConfirmation, d.SpaceShort, d.CrossVolume, d.CacheBudget = false, false, false, false
		d.Downloaded, d.Total, d.PatchBytes, d.Reused, d.Rate, d.ETA = 0, 0, 0, 0, 0, 0
		d.FilesDone, d.FilesTotal = 0, 0
		d.Needed, d.Free, d.Suggested = 0, 0, 0
	default:
		return m, nil, false
	}
	return m, nil, true
}
