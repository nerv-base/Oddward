// SPDX-License-Identifier: GPL-3.0-or-later

package state

// UpdatePhase is where the launcher's own update stands. The shell checks
// and stages off the UI thread; Reduce only records what it reported.
type UpdatePhase uint8

const (
	// SelfUpdateUnsupported: a development or portable build; Note says why.
	SelfUpdateUnsupported UpdatePhase = iota
	SelfUpdateIdle
	SelfUpdateChecking
	SelfUpdateCurrent
	SelfUpdateFailed
	SelfUpdateAvailable
	SelfUpdateDownloading
	SelfUpdateReady
)

// UpdateState is the self-update projection the titlebar and panels draw.
type UpdateState struct {
	Phase   UpdatePhase
	Current string
	Note    string
	// The offered release.
	Version, Notes string
	Size           int64
	Downloaded     int64
	Error          string
	// Skipped is the version the user chose to skip; automatic checks stay
	// quiet about it.
	Skipped string
	manual  bool
	Request uint64
}

// UpdateDetails is the update panel.
const UpdateDetails Overlay = 40

const (
	// UpdateCheck asks for a check; Enabled marks a manual one.
	UpdateCheck EventKind = 220 + iota
	// UpdateChecked: Message version, Body notes, Total size, Enabled newer.
	UpdateChecked
	UpdateCheckFailed
	UpdateDownload
	UpdateProgress
	UpdateStaged
	UpdateStageFailed
	UpdateSkip
	UpdateRestart
	UpdateRestarted
)

const (
	CheckForUpdate EffectKind = 220 + iota
	StageUpdate
	// SaveSkippedVersion persists Effect.Path.
	SaveSkippedVersion
	RestartForUpdate
)

// UpdateOffered reports whether a release is waiting for the user.
func UpdateOffered(p UpdatePhase) bool {
	return p == SelfUpdateAvailable || p == SelfUpdateDownloading || p == SelfUpdateReady
}

// UpdateBlocked reports whether restarting into a new version now would cut
// off a running game, login or task the launcher is watching.
func UpdateBlocked(m Model) bool {
	return downloadLaunchBlocked(m) || m.Download.GameRunning || Active(m.Flow) || m.Flow == Paused || m.Flow == Running ||
		m.NormalMode && (ManagedBusy(m.Managed.Phase) || ManagedActive(m.Managed.Phase))
}

func reduceUpdate(m Model, e Event) (Model, []Effect, bool) {
	u := &m.Update
	switch e.Kind {
	case OpenOverlay:
		if e.Overlay != UpdateDetails {
			return m, nil, false
		}
		if UpdateOffered(u.Phase) && m.Overlay != ConfirmQuit {
			m.Overlay = UpdateDetails
			m.Bar = MediaBarHidden
		}
	case UpdateCheck:
		switch u.Phase {
		case SelfUpdateIdle, SelfUpdateCurrent, SelfUpdateFailed, SelfUpdateAvailable:
			u.Request++
			u.Phase, u.Error, u.manual = SelfUpdateChecking, "", e.Enabled
			return m, []Effect{{Kind: CheckForUpdate, Token: u.Request}}, true
		case SelfUpdateChecking:
			// Asking during an automatic check makes its result a manual one.
			u.manual = u.manual || e.Enabled
		}
	case UpdateChecked, UpdateCheckFailed:
		if e.Token != u.Request || u.Phase != SelfUpdateChecking {
			break
		}
		if e.Kind == UpdateCheckFailed {
			u.Phase, u.Error = SelfUpdateFailed, e.Message
			if u.Error == "" {
				u.Error = "检查更新失败"
			}
			break
		}
		if !e.Enabled || e.Message == u.Skipped && !u.manual {
			u.Phase = SelfUpdateCurrent
			break
		}
		u.Phase, u.Version, u.Notes, u.Size, u.Downloaded = SelfUpdateAvailable, e.Message, e.Body, e.Total, 0
	case UpdateDownload:
		if u.Phase == SelfUpdateAvailable {
			u.Request++
			u.Phase, u.Error, u.Downloaded = SelfUpdateDownloading, "", 0
			return m, []Effect{{Kind: StageUpdate, Token: u.Request}}, true
		}
	case UpdateProgress:
		if e.Token == u.Request && u.Phase == SelfUpdateDownloading && e.Downloaded >= 0 && (e.Total <= 0 || e.Downloaded <= e.Total) {
			u.Downloaded = e.Downloaded
		}
	case UpdateStaged, UpdateStageFailed:
		if e.Token != u.Request || u.Phase != SelfUpdateDownloading {
			break
		}
		if e.Kind == UpdateStaged {
			u.Phase, u.Error = SelfUpdateReady, ""
		} else {
			u.Phase, u.Error = SelfUpdateAvailable, e.Message
			if u.Error == "" {
				u.Error = "下载更新失败"
			}
		}
	case UpdateSkip:
		if u.Phase == SelfUpdateAvailable {
			u.Skipped, u.Phase = u.Version, SelfUpdateCurrent
			if m.Overlay == UpdateDetails {
				m.Overlay = NoOverlay
			}
			return m, []Effect{{Kind: SaveSkippedVersion, Path: u.Version}}, true
		}
	case UpdateRestart:
		if u.Phase == SelfUpdateReady && !UpdateBlocked(m) {
			return m, []Effect{{Kind: RestartForUpdate}}, true
		}
	case UpdateRestarted:
		// Message reports a stub that could not be started; stay open.
		if u.Phase == SelfUpdateReady && e.Message != "" {
			u.Error = e.Message
		} else if u.Phase == SelfUpdateReady {
			m.Closed = true
			m.Token++
			return m, []Effect{{Kind: CancelOperation}, {Kind: CloseWindow}}, true
		}
	default:
		return m, nil, false
	}
	return m, nil, true
}
