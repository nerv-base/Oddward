// SPDX-License-Identifier: GPL-3.0-or-later

package state

import (
	"strings"
	"time"
)

type DownloadKind uint8

const (
	DownloadNone DownloadKind = iota
	DownloadInstall
	DownloadUpdate
	DownloadPreload
)

type DownloadStage uint8

const (
	StageIdle DownloadStage = iota
	StagePlanning
	StageNetwork
	StageVerify
	StageApply
	StageCommit
	StageFinalize
	StageComponents
)

type DownloadPhase uint8

const (
	DownloadIdle DownloadPhase = iota
	DownloadPreparing
	DownloadRunning
	DownloadSuspended
	DownloadVerifying
	DownloadRebuilding
	DownloadCommitting
	DownloadDone
	// DownloadError avoids the event name DownloadFailed.
	DownloadError
	DownloadRecoveryRequired
	DownloadStale
)

type RouteKind uint8

const (
	RouteUnknown RouteKind = iota
	RouteAllReuse
	RoutePatch
	RouteFull
	RouteMixed
)

// Preload states retain the backend's fixed ordinal order. The State prefix
// distinguishes phases from completion events in the same package.
type PreloadState uint8

const (
	PreloadStateUnavailable PreloadState = iota
	PreloadStateAvailable
	PreloadStatePlanning
	PreloadStateDownloading
	PreloadStatePaused
	PreloadStateVerifying
	PreloadStateStaged
	PreloadStateWaitingRelease
	PreloadStateReconcile
	PreloadStateApplicable
	PreloadStateApplying
	PreloadStateCompleted
	PreloadStateStale
	PreloadStateRecoveryRequired
	PreloadStateFailed
)

// preloadStateNote maps the backend's restore notes onto the preload state a
// restart can prove from disk. An unknown note yields PreloadStateUnavailable.
func preloadStateNote(note string) PreloadState {
	switch note {
	case "waiting-release", "staged":
		return PreloadStateWaitingRelease
	case "applicable":
		return PreloadStateApplicable
	case "stale":
		return PreloadStateStale
	}
	return PreloadStateUnavailable
}

// DownloadPreferences carries only choices the user can still change.
// Parallel transfers, cache limits, automatic resume and patch application are
// fixed engine behavior.
type DownloadPreferences struct {
	RateLimitKiB int
}

// DownloadState contains only aggregate facts. Per-file data and task handles
// stay in the shell/backend; Model remains comparable.
type DownloadState struct {
	CacheRoot                                        string
	cacheInstallRoot, cacheSelectionRoot             string
	CrossVolume, CacheBudget                         bool
	Settings, PendingSettings                        DownloadPreferences
	SettingsSaving, SettingsSaved                    bool
	SettingsError                                    string
	SettingsRequest                                  uint64
	OfferRequest                                     uint64
	OfferPending                                     bool
	OfferVersion, PreloadOfferVersion                string
	offerRoot                                        string
	offerTaskRequest, offerManagedRequest            uint64
	offerResultRoot                                  string
	offerNeedsAdoption, offerNeedsVerification       bool
	preparingUpdateBaseline                          bool
	Phase                                            DownloadPhase
	Stage                                            DownloadStage
	Kind                                             DownloadKind
	Route                                            RouteKind
	Version, Current, Note, Error                    string
	FilesDone, FilesTotal                            uint16
	Downloaded, Total, PatchBytes, Reused, Rate, ETA int64
	FallbackNote                                     bool
	Request                                          uint64
	Preload                                          PreloadState
	PreloadVersion                                   string
	Verified, Located                                bool
	OfficialRunning, GameRunning                     bool
	// GameSince is the observation start reported by the backend; zero means
	// the launcher has not confirmed a running game yet.
	GameSince                               time.Time
	AwaitingConfirmation, SpaceShort        bool
	Needed, Free, Suggested                 int64
	operation                               downloadOperation
	pausePending, cancelPending             bool
	stageReported                           bool
	locateInstall                           bool
	verifyTask                              bool
	preloadAutoApply, preloadFallbackUpdate bool
	preloadReconcileAfterStage              bool
	preloadReconcileLoops                   uint8
}
type downloadOperation uint8

const (
	downloadOperationNone downloadOperation = iota
	downloadOperationPlan
	downloadOperationLocate
	downloadOperationAdopt
	downloadOperationVerify
	downloadOperationRun
	downloadOperationRecover
	downloadOperationCheckPreload
	downloadOperationCheckOnly
	downloadOperationReconcile
	downloadOperationDiscard
	downloadOperationCacheLocate
)

const OverlayInstallPath Overlay = 43

const (
	OpenInstallPath EventKind = 257 + iota
	// 258 and 259 retired with the home download panel. Their numbers stay
	// reserved so recorded events keep one meaning.
	_
	_
)
const (
	DownloadRequest EventKind = 260 + iota
	DownloadPlanned
	DownloadPlanFailed
	DownloadStarted
	DownloadProgress
	DownloadPause
	DownloadPaused
	DownloadResume
	DownloadCancel
	DownloadCancelled
	DownloadVerified
	DownloadCommitStart
	DownloadCommitted
	DownloadFailed
	DownloadRecoveryNeeded
	DownloadRecoveryCleared
	InstallAdoptRequest
	InstallAdopted
	InstallAdoptFailed
	InstallVerifyRequest
	InstallVerifyProgress
	InstallVerified
	InstallVerifyFailed
	PreloadRequest
	PreloadChecked
	PreloadStarted
	PreloadProgress
	PreloadStaged
	PreloadAvailableToApply
	PreloadApplyRequest
	PreloadApplied
	PreloadStale
	PreloadFailed
	InstallLocateRequest
	InstallLocated
	InstallLocateFailed
	DownloadRoutePlanned
	DownloadFallbackNotice
	ApplyStarted
	ApplyProgress
	ApplyFinished
	// Confirmation is separate from the offer: planning never starts downloads.
	DownloadConfirm
	DownloadRecoverRequest
	DownloadRecoveryScanRequest
	// Enabled reports official Patcher activity; Focused reports game activity.
	DownloadProcessesObserved
	PreloadCheckRequest
	PreloadReconcileRequest
	PreloadDiscardRequest
	PreloadDiscarded
	DownloadSettingsApply
	DownloadSettingsSaved
	DownloadSettingsFailed
	DownloadSettingsLoaded
	DownloadRestored
	DownloadOfferPollRequest
	DownloadOfferPolled
	DownloadOfferPollFailed
	DownloadCacheSelect
	DownloadCacheLocated
	DownloadCacheLocateFailed
)
const (
	EffectDownloadPlan EffectKind = 260 + iota
	EffectDownloadRun
	EffectDownloadPause
	EffectDownloadCancel
	EffectDownloadVerify
	EffectDownloadCommit
	EffectInstallAdopt
	EffectInstallVerify
	EffectPreloadCheck
	EffectPreloadRun
	EffectPreloadApply
	EffectPreloadDiscard
	EffectRecoverScan
	EffectInstallLocate
	EffectApplyPatches
)
const EffectSaveDownloads EffectKind = 275
const EffectDownloadOfferPoll EffectKind = 276
const EffectInstallCacheLocate EffectKind = 277

// DownloadNetworkBlocked permits network work alongside a game or login flow.
func DownloadNetworkBlocked(m Model) string {
	if m.Download.Phase == DownloadRecoveryRequired {
		return "recovery-required"
	}
	if m.Download.OfficialRunning {
		return "official-running"
	}
	return ""
}

// DownloadWriteBlocked protects the installation. Backend checks remain mandatory
// because the UI's process observations can become stale before a write.
func DownloadWriteBlocked(m Model) string {
	if reason := DownloadNetworkBlocked(m); reason != "" {
		return reason
	}
	if m.Download.GameRunning || m.Flow == Running || m.NormalMode && ManagedActive(m.Managed.Phase) {
		return "game-running"
	}
	if m.NormalMode && ManagedBusy(m.Managed.Phase) || m.Flow == Launching {
		return "login-running"
	}
	return ""
}
func DownloadNoteText(key string) string {
	switch key {
	case "":
		return ""
	case "waiting-release":
		return "预下载文件已准备就绪，等待版本开放"
	case "not-offered", "not-open":
		return "官方尚未开放预下载"
	case "unverified", "unknown":
		return "暂时读不到预下载信息"
	case "official-running":
		return "请先关闭官方启动器，再继续操作"
	case "game-running":
		return "请先退出游戏，再应用更新"
	case "login-running":
		return "请等待登录流程结束，再应用更新"
	case "disk-full":
		return "磁盘已满，任务已暂停，请释放空间后继续"
	case "disk-short":
		return "可用空间不足，请释放空间后重新规划"
	case "locked":
		return "安装目录正在使用，请结束当前任务后重试"
	case "patch-source-mismatch":
		return "检测到本地文件与官方补丁不匹配，已改用完整下载"
	case "fallback-full":
		return "部分文件改为完整下载"
	case "recovery-required":
		return "上次更新未完成，请先完成恢复"
	case "stale":
		return "目标版本信息已变化，请重新规划"
	case "cancelled":
		return "任务已取消，现有安装保持不变"
	default:
		return "操作暂时无法完成，请重试或联系维护者"
	}
}
func downloadMutation(d DownloadState) bool {
	return d.Phase == DownloadRebuilding || d.Phase == DownloadCommitting || d.Stage == StageCommit || d.Stage == StageFinalize
}
func downloadBusy(d DownloadState) bool {
	return d.operation != downloadOperationNone || d.Phase == DownloadRunning || d.Phase == DownloadVerifying || downloadMutation(d) || d.pausePending || d.cancelPending
}
func downloadUnfinished(d DownloadState) bool {
	return downloadBusy(d) || d.Phase == DownloadSuspended || d.AwaitingConfirmation || d.Phase == DownloadRecoveryRequired
}
func downloadCanReplan(d DownloadState) bool {
	return d.Phase == DownloadPreparing && d.AwaitingConfirmation && d.SpaceShort && !downloadBusy(d)
}
func downloadCanDiscoverPreload(d DownloadState) bool {
	return d.Preload == PreloadStateUnavailable || d.Preload == PreloadStateAvailable || d.Preload == PreloadStateStale || d.Preload == PreloadStateFailed || d.Preload == PreloadStateCompleted
}
func pendingGameInstall(d DownloadState) bool {
	return d.Kind == DownloadInstall && !d.Located && !d.Verified
}

// CanSelectInstallPath allows a validated draft to be replaced before starting.
func CanSelectInstallPath(m Model) bool {
	d := m.Download
	return m.Overlay == OverlayInstallPath && (!downloadUnfinished(d) || d.AwaitingConfirmation && !downloadBusy(d))
}

func downloadLaunchBlocked(m Model) bool {
	return downloadMutation(m.Download) || m.Download.Phase == DownloadRecoveryRequired || m.Download.OfficialRunning || m.Download.GameRunning
}

func reduceDownload(m Model, e Event) (Model, []Effect, bool) {
	if next, fx, handled := reduceDownloadCache(m, e); handled {
		if m.Overlay == OverlayInstallPath && e.Kind == DownloadCacheLocated && e.Path != "" &&
			m.Download.operation == downloadOperationCacheLocate && next.Download.operation == downloadOperationNone {
			return reduceDownload(next, Event{Kind: DownloadRequest, Enabled: true})
		}
		return next, fx, true
	}
	if next, fx, handled := reduceDownloadOffer(m, e); handled {
		return next, fx, true
	}
	if next, fx, handled := reduceDownloadSettings(m, e); handled {
		return next, fx, true
	}
	d := &m.Download
	recognized := e.Kind >= DownloadRequest && e.Kind <= DownloadRestored
	switch e.Kind {
	case OpenOverlay:
		if e.Overlay == ConfirmDownloadCancel {
			if m.NormalMode && m.Page == Home && m.Overlay == NoOverlay && PresentDownload(m).CanCancel {
				m.Overlay = ConfirmDownloadCancel
			}
			return m, nil, true
		}
	case OpenInstallPath:
		if m.NormalMode && m.Page == Home && m.Overlay == NoOverlay && m.Managed.Root == "" &&
			!downloadUnfinished(*d) && !ManagedBusy(m.Managed.Phase) && !ManagedActive(m.Managed.Phase) {
			m.Overlay = OverlayInstallPath
			m.Bar = MediaBarHidden
		}
		return m, nil, true
	}
	if m.Overlay == OverlayInstallPath {
		switch e.Kind {
		case OpenOverlay:
			return m, nil, true
		case Dismiss, RequestQuit:
			m.Overlay = NoOverlay
			d.Request++
			d.operation = downloadOperationNone
			d.locateInstall = false
			d.Phase, d.Stage = DownloadIdle, StageIdle
			d.AwaitingConfirmation, d.SpaceShort = false, false
			if m.Managed.Root != "" {
				m.Managed.Root, m.Managed.Phase = "", ManagedUnlocated
			}
			d.CacheRoot, d.cacheInstallRoot = "", ""
			d.Note, d.Error = "", ""
			if e.Kind == RequestQuit {
				next, fx := Reduce(m, e)
				return next, fx, true
			}
			return m, nil, true
		}
	}
	if e.Kind == RequestQuit && downloadUnfinished(*d) {
		m.Overlay = ConfirmQuit
		return m, nil, true
	}
	if e.Kind == ConfirmExit && m.Overlay == ConfirmQuit && downloadUnfinished(*d) {
		m.Closed = true
		m.Token++
		d.Request++
		return m, []Effect{{Kind: EffectDownloadPause, Token: d.Request, Path: m.Managed.Root}, {Kind: CancelOperation}, {Kind: CloseWindow}}, true
	}
	if e.Kind == ManagedAcceptLaunch && (downloadLaunchBlocked(m) || pendingGameInstall(*d)) {
		return m, nil, true
	}
	if e.Kind == Primary && m.Page == Home && m.Overlay == NoOverlay {
		if p, action, ok := homeDownloadPrimary(m); ok {
			if !p.Enabled || action == 0 {
				return m, nil, true
			}
			return reduceDownload(m, Event{Kind: action})
		}
	}
	if e.Kind == Primary && downloadLaunchBlocked(m) {
		if m.Page == Home && m.Overlay == NoOverlay && d.Phase == DownloadRecoveryRequired {
			return reduceDownload(m, Event{Kind: DownloadRecoverRequest})
		}
		return m, nil, true
	}
	// Legacy launch location controls must never rebind a live task to another root.
	if downloadUnfinished(*d) && (e.Kind == ManagedBrowse || e.Kind == ManagedSelectPath || e.Kind == ManagedLoad || e.Kind == ManagedLocated || e.Kind == ManagedLoaded || e.Kind == SelectScenario) {
		return m, nil, true
	}
	if !recognized {
		return m, nil, false
	}
	effect := func(k EffectKind) (Model, []Effect, bool) {
		return m, []Effect{{Kind: k, Token: d.Request, Path: m.Managed.Root}}, true
	}
	current := e.Token != 0 && e.Token == d.Request
	if e.Kind == DownloadProcessesObserved {
		if !current {
			return m, nil, true
		}
		d.OfficialRunning, d.GameRunning = e.Enabled, e.Focused
		if e.Focused {
			if d.GameSince.IsZero() {
				d.GameSince = e.Since
			}
		} else {
			d.GameSince = time.Time{}
		}
		if d.OfficialRunning && (d.Phase == DownloadRunning || d.Phase == DownloadVerifying) && !d.pausePending && !d.cancelPending {
			d.Note = "official-running"
			d.Request++
			d.pausePending = true
			d.Rate, d.ETA = 0, 0
			return effect(EffectDownloadPause)
		}
		return m, nil, true
	}
	if d.Phase == DownloadRecoveryRequired && e.Kind != DownloadRecoverRequest && e.Kind != DownloadRecoveryNeeded && e.Kind != DownloadRecoveryCleared {
		return m, nil, true
	}
	startPlan := func(kind DownloadKind) (Model, []Effect, bool) {
		d.Request++
		d.Kind = kind
		d.Phase, d.Stage = DownloadPreparing, StagePlanning
		d.Version, d.Note, d.Error = "", "", ""
		d.Route = RouteUnknown
		d.FallbackNote = false
		d.CrossVolume, d.CacheBudget = false, false
		d.Downloaded, d.Total, d.PatchBytes, d.Reused, d.Rate, d.ETA = 0, 0, 0, 0, 0, 0
		d.FilesDone, d.FilesTotal = 0, 0
		d.stageReported = false
		d.AwaitingConfirmation, d.SpaceShort = false, false
		d.Needed, d.Free, d.Suggested = 0, 0, 0
		d.operation = downloadOperationPlan
		d.verifyTask = false
		if kind == DownloadPreload {
			d.Preload = PreloadStatePlanning
		}
		return effect(EffectDownloadPlan)
	}
	startBaselineCheck := func(adopt, forUpdate bool) (Model, []Effect, bool) {
		d.Request++
		d.preparingUpdateBaseline = forUpdate
		d.Error, d.Note = "", ""
		if forUpdate {
			d.Kind = DownloadUpdate
		}
		if adopt {
			d.operation = downloadOperationAdopt
			if forUpdate {
				d.Phase, d.Stage = DownloadPreparing, StagePlanning
			}
			return effect(EffectInstallAdopt)
		}
		d.operation = downloadOperationVerify
		d.verifyTask = true
		d.Phase, d.Stage = DownloadVerifying, StageVerify
		d.Downloaded, d.Total, d.Rate, d.ETA = 0, 0, 0, 0
		d.FilesDone, d.FilesTotal = 0, 0
		d.stageReported = false
		return effect(EffectInstallVerify)
	}
	clearPreloadFlow := func() {
		d.preloadAutoApply = false
		d.preloadFallbackUpdate = false
		d.preloadReconcileAfterStage = false
		d.preloadReconcileLoops = 0
	}
	startPreloadReconcile := func(autoApply, fallbackUpdate bool) (Model, []Effect, bool) {
		d.Request++
		d.Kind = DownloadPreload
		d.Preload = PreloadStateReconcile
		d.operation = downloadOperationReconcile
		d.preloadAutoApply = autoApply
		d.preloadFallbackUpdate = fallbackUpdate
		d.preloadReconcileAfterStage = false
		d.Downloaded, d.Total, d.PatchBytes, d.Reused, d.Rate, d.ETA = 0, 0, 0, 0, 0, 0
		d.FilesDone, d.FilesTotal = 0, 0
		d.stageReported = false
		d.Route = RouteUnknown
		d.FallbackNote = false
		d.CrossVolume, d.CacheBudget = false, false
		d.AwaitingConfirmation, d.SpaceShort = false, false
		d.Error, d.Note = "", ""
		return m, []Effect{{Kind: EffectPreloadCheck, Token: d.Request, Path: m.Managed.Root, Enabled: true}}, true
	}
	beginCommit := func() (Model, []Effect, bool) {
		if reason := DownloadWriteBlocked(m); reason != "" {
			d.Note = reason
			d.Phase, d.Stage = DownloadVerifying, StageVerify
			d.AwaitingConfirmation = true
			return m, nil, true
		}
		d.AwaitingConfirmation = false
		d.Phase, d.Stage = DownloadCommitting, StageCommit
		d.Downloaded, d.Total, d.Rate, d.ETA = 0, 0, 0, 0
		d.FilesDone, d.FilesTotal = 0, 0
		d.stageReported = false
		return effect(EffectDownloadCommit)
	}
	switch e.Kind {
	case DownloadRequest:
		if m.Overlay == OverlayInstallPath && !e.Enabled || downloadUnfinished(*d) && !downloadCanReplan(*d) || m.Managed.Root == "" || DownloadNetworkBlocked(m) != "" {
			break
		}
		if !e.Enabled && hasFormalUpdateOffer(m) && (d.offerNeedsAdoption || d.offerNeedsVerification) {
			if reason := DownloadWriteBlocked(m); reason != "" {
				d.Note = reason
				break
			}
			return startBaselineCheck(d.offerNeedsAdoption, true)
		}
		if !e.Enabled && (d.Preload == PreloadStateWaitingRelease || d.Preload == PreloadStateStaged || d.Preload == PreloadStateApplicable || downloadCanReplan(*d) && d.Kind == DownloadPreload && d.preloadAutoApply) {
			d.preloadReconcileLoops = 0
			return startPreloadReconcile(true, true)
		}
		clearPreloadFlow()
		kind := DownloadUpdate
		if e.Enabled {
			kind = DownloadInstall
		}
		return startPlan(kind)
	case PreloadRequest, PreloadCheckRequest:
		if downloadUnfinished(*d) && !downloadCanReplan(*d) || !downloadCanDiscoverPreload(*d) && !(downloadCanReplan(*d) && d.Kind == DownloadPreload) || m.Managed.Root == "" || DownloadNetworkBlocked(m) != "" {
			break
		}
		clearPreloadFlow()
		d.Request++
		d.Kind = DownloadPreload
		d.operation = downloadOperationCheckPreload
		d.verifyTask = false
		d.AwaitingConfirmation, d.SpaceShort = false, false
		d.Phase, d.Stage = DownloadPreparing, StagePlanning
		d.Error, d.Note = "", ""
		if e.Kind == PreloadCheckRequest {
			d.operation = downloadOperationCheckOnly
		}
		return effect(EffectPreloadCheck)
	case PreloadChecked:
		if current && d.operation == downloadOperationReconcile && !e.Enabled {
			// Focused already carries the backend's numeric comparison between the
			// trusted formal version and the installed one; a mirror that lags must
			// not be read as evidence that an intermediate hotfix was skipped.
			if e.Body == "waiting-release" && d.preloadFallbackUpdate && e.Focused {
				d.operation = downloadOperationNone
				d.Preload = PreloadStateWaitingRelease
				clearPreloadFlow()
				return startPlan(DownloadUpdate)
			}
			switch e.Body {
			case "waiting-release", "disk-short", "game-running", "official-running", "locked":
				d.operation = downloadOperationNone
				d.Preload = PreloadStateWaitingRelease
				clearPreloadFlow()
				if e.Message != "" {
					d.PreloadVersion = e.Message
					d.Version = e.Message
				}
				d.Phase, d.Stage = DownloadDone, StageIdle
				d.Note = e.Body
				d.Error = ""
			default:
				d.operation = downloadOperationNone
				d.Preload = PreloadStateWaitingRelease
				clearPreloadFlow()
				d.Phase, d.Stage = DownloadError, StageIdle
				d.Error = downloadErrorText(e.Message)
			}
			break
		}
		if !current || d.operation != downloadOperationCheckPreload && d.operation != downloadOperationCheckOnly {
			break
		}
		checkOnly := d.operation == downloadOperationCheckOnly
		d.operation = downloadOperationNone
		if !e.Enabled {
			d.Phase, d.Stage = DownloadIdle, StageIdle
			d.Preload = PreloadStateUnavailable
			d.Note = e.Body
			if d.Note == "" {
				d.Note = "unverified"
			}
			break
		}
		d.PreloadVersion = e.Message
		if checkOnly {
			d.Phase, d.Stage = DownloadIdle, StageIdle
			d.Preload = PreloadStateAvailable
			break
		}
		return startPlan(DownloadPreload)
	case DownloadRoutePlanned:
		if !current || d.operation != downloadOperationPlan && d.operation != downloadOperationReconcile || e.Index > uint8(RouteMixed) || e.Total < 0 || e.PatchBytes < 0 || e.PatchBytes > e.Total || e.Reused < 0 {
			break
		}
		d.Route = RouteKind(e.Index)
		d.Total, d.PatchBytes, d.Reused = e.Total, e.PatchBytes, e.Reused
	case DownloadPlanned:
		if !current || d.operation != downloadOperationPlan && d.operation != downloadOperationReconcile || e.Message == "" || e.Total < 0 || e.Reused < 0 || e.Needed < 0 || e.Free < 0 || e.Suggested < 0 || d.PatchBytes > e.Total {
			break
		}
		fromReconcile := d.operation == downloadOperationReconcile
		d.Version, d.Total, d.Reused, d.FilesTotal = e.Message, e.Total, e.Reused, e.FilesTotal
		d.Phase, d.Stage = DownloadPreparing, StagePlanning
		d.Downloaded, d.FilesDone = 0, 0
		if d.Kind == DownloadPreload {
			d.Preload = PreloadStatePlanning
		}
		if fromReconcile {
			d.preloadReconcileAfterStage = true
		}
		d.operation = downloadOperationNone
		d.AwaitingConfirmation = true
		d.SpaceShort = e.Enabled
		d.Needed, d.Free, d.Suggested = e.Needed, e.Free, e.Suggested
		d.CrossVolume = d.Kind == DownloadInstall && e.Body == "cross-volume"
		d.CacheBudget = d.CrossVolume && e.Focused
		if d.SpaceShort {
			d.Note = "disk-short"
		}
		// A plan that fits and has a working network starts immediately: the
		// home action is the user's confirmation, so no second dialog stands
		// between the click and the transfer. Fresh installs keep their picker
		// open until the user presses start there.
		if d.Kind != DownloadInstall && !d.SpaceShort && DownloadNetworkBlocked(m) == "" {
			next, fx, handled := reduceDownload(m, Event{Kind: DownloadConfirm})
			if handled {
				return next, fx, true
			}
		}
	case DownloadConfirm:
		if !d.AwaitingConfirmation || d.SpaceShort || d.operation != downloadOperationNone || d.pausePending || d.cancelPending || DownloadNetworkBlocked(m) != "" {
			break
		}
		if d.Phase == DownloadVerifying {
			return beginCommit()
		}
		if d.Phase != DownloadPreparing {
			break
		}
		d.Request++
		d.AwaitingConfirmation = false
		d.operation = downloadOperationRun
		d.Stage = StageNetwork
		d.FilesDone, d.FilesTotal = 0, 0
		d.stageReported = false
		k := EffectDownloadRun
		if d.Kind == DownloadPreload {
			k = EffectPreloadRun
			d.Preload = PreloadStateDownloading
		}
		if m.Overlay == OverlayInstallPath {
			m.Overlay = NoOverlay
			return m, []Effect{{Kind: SaveManagedSettings, Token: m.Managed.Request, Path: m.Managed.Root}, {Kind: k, Token: d.Request, Path: m.Managed.Root}}, true
		}
		return effect(k)
	case DownloadStarted, PreloadStarted:
		if !current || d.operation != downloadOperationRun || d.pausePending || d.cancelPending || d.Phase != DownloadPreparing && d.Phase != DownloadSuspended {
			break
		}
		d.Phase, d.Stage = DownloadRunning, StageNetwork
		d.operation = downloadOperationNone
		d.FilesDone, d.FilesTotal = 0, 0
		d.stageReported = false
	case DownloadProgress, PreloadProgress, InstallVerifyProgress, ApplyProgress:
		if !current || d.pausePending || d.cancelPending {
			break
		}
		if e.Kind == InstallVerifyProgress && d.operation != downloadOperationVerify || e.Kind == ApplyProgress && d.Phase != DownloadRebuilding {
			break
		}
		if d.Phase != DownloadRunning && d.Phase != DownloadVerifying && d.Phase != DownloadRebuilding && d.Phase != DownloadCommitting {
			break
		}
		if !downloadAcceptProgress(d, e) {
			break
		}
	case DownloadPause:
		if d.pausePending || d.cancelPending || d.Phase != DownloadRunning && d.Phase != DownloadVerifying {
			break
		}
		d.Request++
		d.pausePending = true
		d.Rate, d.ETA = 0, 0
		return effect(EffectDownloadPause)
	case DownloadPaused:
		backendDiskFull := e.Enabled && e.Body == "disk-full" && d.Stage == StageNetwork && (d.Phase == DownloadRunning || d.Phase == DownloadPreparing && d.operation == downloadOperationRun)
		if !current || d.cancelPending || !d.pausePending && !backendDiskFull {
			break
		}
		if backendDiskFull && !d.pausePending {
			d.Request++
			d.Note, d.Error = "disk-full", ""
		}
		d.Rate, d.ETA = 0, 0
		d.pausePending = false
		d.operation = downloadOperationNone
		d.Phase = DownloadSuspended
		if d.Kind == DownloadPreload {
			d.Preload = PreloadStatePaused
		}
	case DownloadResume:
		if d.Phase != DownloadSuspended || d.pausePending || d.cancelPending || DownloadNetworkBlocked(m) != "" {
			break
		}
		d.Request++
		d.operation = downloadOperationRun
		d.stageReported = false
		d.Error, d.Note = "", ""
		if d.verifyTask {
			d.operation = downloadOperationVerify
			d.Phase, d.Stage = DownloadVerifying, StageVerify
			return effect(EffectInstallVerify)
		}
		k := EffectDownloadRun
		if d.Kind == DownloadPreload {
			k = EffectPreloadRun
		}
		return effect(k)
	case DownloadCancel:
		if m.Overlay == ConfirmDownloadCancel {
			m.Overlay = NoOverlay
		}
		if !PresentDownload(m).CanCancel {
			break
		}
		d.Request++
		d.cancelPending = true
		d.pausePending = false
		d.Rate, d.ETA = 0, 0
		d.AwaitingConfirmation = false
		return effect(EffectDownloadCancel)
	case DownloadCancelled:
		if !current || !d.cancelPending {
			break
		}
		d.cancelPending = false
		d.operation = downloadOperationNone
		d.preparingUpdateBaseline = false
		clearPreloadFlow()
		d.Phase, d.Stage = DownloadIdle, StageIdle
		d.Note = "cancelled"
		if d.Kind == DownloadPreload {
			d.Preload = PreloadStateUnavailable
		}
	case ApplyStarted:
		// A preload reaches apply before its transaction starts (StageApply) and
		// rebuilds staged patches exactly like an update, so its progress and
		// fallback notices must be visible until DownloadCommitStart.
		preloadRebuild := d.Kind == DownloadPreload && d.Phase == DownloadCommitting && d.Stage != StageCommit
		if !current || d.pausePending || d.cancelPending || d.Phase != DownloadRunning && d.Phase != DownloadVerifying && !preloadRebuild {
			break
		}
		d.Phase, d.Stage = DownloadRebuilding, StageApply
		d.Downloaded, d.Total, d.Rate, d.ETA = 0, 0, 0, 0
		d.FilesDone, d.FilesTotal = 0, e.FilesTotal
		d.stageReported = false
	case ApplyFinished:
		if !current || d.Phase != DownloadRebuilding {
			break
		}
		d.FallbackNote = d.FallbackNote || e.Enabled
		d.FilesDone = e.FilesDone
		d.Phase, d.Stage = DownloadVerifying, StageVerify
		d.Downloaded, d.Total, d.Rate, d.ETA = 0, 0, 0, 0
		d.stageReported = false
		return effect(EffectDownloadVerify)
	case DownloadFallbackNotice:
		if !current || d.Phase != DownloadRunning && d.Phase != DownloadRebuilding && d.Phase != DownloadVerifying {
			break
		}
		d.FallbackNote = true
		d.Note = "fallback-full"
		if e.Body == "patch-source-mismatch" {
			d.Note = e.Body
		}
	case DownloadVerified:
		if !current || d.pausePending || d.cancelPending || d.Phase != DownloadRunning && d.Phase != DownloadVerifying {
			break
		}
		d.Phase = DownloadVerifying
		if d.Stage != StageComponents {
			d.Stage = StageVerify
		}
		d.Rate, d.ETA = 0, 0
		if d.Kind == DownloadPreload {
			d.Preload = PreloadStateVerifying
			break
		}
		return beginCommit()
	case DownloadCommitStart:
		// The backend reports this only after it has started the transaction, so a
		// game or login race observed asynchronously cannot drop the stage receipt.
		if !current || d.Phase != DownloadCommitting && !(d.Kind == DownloadPreload && d.Phase == DownloadRebuilding) {
			break
		}
		d.Phase, d.Stage = DownloadCommitting, StageCommit
		d.stageReported = false
	case DownloadCommitted, PreloadApplied:
		if !current || d.Phase != DownloadCommitting || e.Message == "" {
			break
		}
		if e.Kind == PreloadApplied && d.Preload != PreloadStateApplying || e.Kind == DownloadCommitted && d.Kind == DownloadPreload {
			break
		}
		d.Current, d.Verified, d.Phase, d.Stage = e.Message, true, DownloadDone, StageIdle
		d.operation = downloadOperationNone
		clearPreloadFlow()
		d.Note, d.Error = "", ""
		d.Rate, d.ETA = 0, 0
		if e.Kind == PreloadApplied {
			d.Preload = PreloadStateCompleted
		}
	case DownloadRecoveryScanRequest, DownloadRecoverRequest:
		if m.Managed.Root == "" {
			break
		}
		if e.Kind == DownloadRecoveryScanRequest && (d.Request != 0 || downloadUnfinished(*d)) || e.Kind == DownloadRecoverRequest && d.Phase != DownloadRecoveryRequired || d.operation == downloadOperationRecover {
			break
		}
		d.Request++
		d.operation = downloadOperationRecover
		return m, []Effect{{Kind: EffectRecoverScan, Token: d.Request, Path: m.Managed.Root, Enabled: e.Kind == DownloadRecoverRequest}}, true
	case DownloadRecoveryNeeded:
		if !current || d.operation != downloadOperationRecover && d.Phase != DownloadCommitting && d.Phase != DownloadRebuilding && d.operation != downloadOperationReconcile {
			break
		}
		d.Phase, d.Stage = DownloadRecoveryRequired, StageIdle
		d.operation = downloadOperationNone
		clearPreloadFlow()
		d.Note = "recovery-required"
		d.Error = downloadErrorText(e.Message)
		d.Rate, d.ETA = 0, 0
		if d.Kind == DownloadPreload {
			d.Preload = PreloadStateRecoveryRequired
		}
	case DownloadRestored:
		if !current || d.operation != downloadOperationRecover || e.Path == "" || !SameManagedRoot(e.Path, m.Managed.Root) || e.Index < uint8(DownloadInstall) || e.Index > uint8(DownloadPreload) || e.Stage != StageIdle && e.Stage != StageNetwork || e.Message == "" {
			break
		}
		restored := PreloadStateUnavailable
		if DownloadKind(e.Index) == DownloadPreload && e.Stage == StageIdle {
			restored = preloadStateNote(e.Body)
			if restored == PreloadStateUnavailable {
				break
			}
		}
		d.operation = downloadOperationNone
		d.Kind = DownloadKind(e.Index)
		d.Version, d.Current, d.Verified = e.Message, e.CurrentVersion, e.Enabled
		d.Stage = e.Stage
		d.Phase = DownloadIdle
		d.Error, d.Note = "", ""
		d.Rate, d.ETA = 0, 0
		d.stageReported = false
		if e.Stage == StageNetwork {
			d.Phase = DownloadSuspended
		}
		if d.Kind == DownloadPreload {
			d.PreloadVersion = e.Message
			d.Preload = restored
			if e.Stage == StageNetwork {
				d.Preload = PreloadStatePaused
			}
		} else if e.Body != "" {
			// A scan that resumed an ordinary task can still report the preload it
			// found beside it. Only the preload fields are restored, so the resumed
			// hotfix keeps its own kind, phase, stage and target version.
			if note, preloadVersion, ok := strings.Cut(e.Body, ":"); ok && preloadVersion != "" {
				if preload := preloadStateNote(note); preload != PreloadStateUnavailable {
					d.Preload, d.PreloadVersion = preload, preloadVersion
				}
			}
		}
		if e.Stage == StageNetwork && e.Focused {
			return reduceDownload(m, Event{Kind: DownloadResume})
		}
	case DownloadRecoveryCleared:
		if !current || d.operation != downloadOperationRecover {
			break
		}
		d.operation = downloadOperationNone
		d.Phase, d.Stage = DownloadIdle, StageIdle
		d.Note, d.Error = "", ""
		if e.Enabled && e.Message != "" {
			d.Current, d.Verified = e.Message, true
		}
		if e.CurrentVersion != "" {
			d.Current, d.Verified = e.CurrentVersion, e.Enabled
		}
		// Recovery clears the persisted preload record, so the UI must leave the
		// recovery state the same way the backend record did.
		if d.Preload == PreloadStateRecoveryRequired {
			switch e.Body {
			case "completed":
				d.Preload = PreloadStateCompleted
			case "stale":
				d.Preload = PreloadStateStale
			default:
				d.Preload = PreloadStateUnavailable
			}
		}
	case InstallLocateRequest, InstallAdoptRequest, InstallVerifyRequest:
		choosingDraft := m.Overlay == OverlayInstallPath && e.Kind == InstallLocateRequest && e.Enabled
		if m.Overlay == OverlayInstallPath && (!choosingDraft || !CanSelectInstallPath(m)) ||
			!choosingDraft && downloadUnfinished(*d) ||
			m.NormalMode && (ManagedBusy(m.Managed.Phase) || ManagedActive(m.Managed.Phase)) {
			break
		}
		if e.Kind != InstallLocateRequest && m.Managed.Root == "" {
			break
		}
		if e.Kind == InstallLocateRequest {
			d.Request++
			d.Error, d.Note = "", ""
			d.operation = downloadOperationLocate
			d.locateInstall = e.Enabled
			return m, []Effect{{Kind: EffectInstallLocate, Token: d.Request, Path: m.Managed.Root, Enabled: e.Enabled}}, true
		}
		return startBaselineCheck(e.Kind == InstallAdoptRequest, false)
	case InstallLocated:
		if !current || d.operation != downloadOperationLocate {
			break
		}
		d.operation = downloadOperationNone
		root := e.Message
		if e.Path != "" {
			root = e.Path
		}
		fresh := d.locateInstall
		d.locateInstall = false
		if root == "" {
			break
		}
		if !SameManagedRoot(m.Managed.Root, root) {
			m.Managed.RiskAccepted = false
			d.Current, d.offerResultRoot = "", ""
		}
		m.Managed.Root, m.Managed.Phase, m.Managed.Error = root, ManagedReady, ""
		m.Managed.restoredConsentRoot = ""
		d.Located, d.Verified = !fresh, e.Enabled && !fresh
		if fresh {
			d.Kind = DownloadInstall
			d.Current = ""
			if m.Overlay == OverlayInstallPath {
				d.AwaitingConfirmation = false
				if reason := DownloadNetworkBlocked(m); reason != "" {
					d.Phase, d.Stage = DownloadIdle, StageIdle
					d.Note = reason
					break
				}
				return startPlan(DownloadInstall)
			}
		}
		return m, []Effect{{Kind: SaveManagedSettings, Token: m.Managed.Request, Path: root}}, true
	case InstallAdopted, InstallVerified:
		expected := downloadOperationAdopt
		if e.Kind == InstallVerified {
			expected = downloadOperationVerify
		}
		if !current || d.operation != expected {
			break
		}
		d.operation = downloadOperationNone
		d.Current = e.Message
		d.Located = true
		d.Verified = e.Kind == InstallVerified || e.Enabled
		d.Phase, d.Stage = DownloadIdle, StageIdle
		d.Rate, d.ETA = 0, 0
		if SameManagedRoot(d.offerResultRoot, m.Managed.Root) {
			d.offerNeedsAdoption = false
			if e.Kind == InstallVerified || d.preparingUpdateBaseline {
				d.offerNeedsVerification = false
			} else {
				d.offerNeedsVerification = !d.Verified
			}
		}
		if d.preparingUpdateBaseline {
			d.preparingUpdateBaseline = false
			if hasFormalUpdateOffer(m) {
				return reduceDownload(m, Event{Kind: DownloadRequest})
			}
		}
	case PreloadStaged:
		if !current || d.Kind != DownloadPreload || d.Phase != DownloadRunning && d.Phase != DownloadVerifying {
			break
		}
		d.Preload = PreloadStateWaitingRelease
		d.PreloadVersion = d.Version
		d.Phase, d.Stage = DownloadDone, StageIdle
		d.Rate, d.ETA = 0, 0
		if d.preloadReconcileAfterStage {
			d.preloadReconcileAfterStage = false
			if d.preloadReconcileLoops < 2 {
				d.preloadReconcileLoops++
				return startPreloadReconcile(d.preloadAutoApply, d.preloadFallbackUpdate)
			}
			clearPreloadFlow()
		}
	case PreloadReconcileRequest:
		if downloadUnfinished(*d) || d.Preload != PreloadStateWaitingRelease && d.Preload != PreloadStateStaged && d.Preload != PreloadStateStale || m.Managed.Root == "" {
			break
		}
		d.preloadReconcileLoops = 0
		return startPreloadReconcile(false, false)
	case PreloadDiscardRequest:
		if downloadUnfinished(*d) || d.Preload == PreloadStateUnavailable || d.Preload == PreloadStateApplying || d.Preload == PreloadStateRecoveryRequired {
			break
		}
		d.Request++
		d.operation = downloadOperationDiscard
		return effect(EffectPreloadDiscard)
	case PreloadDiscarded:
		if !current || d.operation != downloadOperationDiscard {
			break
		}
		d.operation = downloadOperationNone
		clearPreloadFlow()
		d.Preload = PreloadStateUnavailable
		d.PreloadVersion = ""
		d.Note, d.Error = "", ""
		// Discarding touches only the preload record; a paused or finished hotfix
		// task keeps its own phase and version.
		if d.Kind == DownloadPreload {
			d.Phase, d.Stage = DownloadIdle, StageIdle
			d.Version = ""
		}
	case PreloadAvailableToApply:
		if !current || d.Kind != DownloadPreload || d.Preload != PreloadStateWaitingRelease && d.Preload != PreloadStateReconcile {
			break
		}
		d.Preload = PreloadStateApplicable
		d.operation = downloadOperationNone
		autoApply := d.preloadAutoApply
		clearPreloadFlow()
		if autoApply {
			if reason := DownloadWriteBlocked(m); reason != "" {
				d.Note = reason
				break
			}
			d.Request++
			d.Preload = PreloadStateApplying
			// The transaction has not started yet: the backend still rebuilds
			// staged patches, and only DownloadCommitStart enters StageCommit.
			d.Phase, d.Stage = DownloadCommitting, StageApply
			d.Error, d.Note = "", ""
			return effect(EffectPreloadApply)
		}
	case PreloadApplyRequest:
		if d.Preload != PreloadStateApplicable || downloadUnfinished(*d) {
			break
		}
		if reason := DownloadWriteBlocked(m); reason != "" {
			d.Note = reason
			break
		}
		d.Request++
		d.Preload = PreloadStateApplying
		d.Phase, d.Stage = DownloadCommitting, StageApply
		d.Error, d.Note = "", ""
		return effect(EffectPreloadApply)
	case PreloadStale:
		// Applying only owns the record once the transaction has started. Before
		// StageCommit a stale plan or baseline still leaves the installation
		// untouched, so the record must be released instead of sticking the UI in
		// "applying" forever.
		if !current || d.Kind != DownloadPreload || d.Preload == PreloadStateApplying && d.Stage == StageCommit || d.Preload == PreloadStateUnavailable {
			break
		}
		fallback := d.preloadFallbackUpdate
		clearPreloadFlow()
		d.Preload = PreloadStateStale
		d.Phase, d.Stage = DownloadStale, StageIdle
		d.operation = downloadOperationNone
		d.Note = "stale"
		d.Rate, d.ETA = 0, 0
		if fallback && DownloadNetworkBlocked(m) == "" {
			return startPlan(DownloadUpdate)
		}
	case DownloadPlanFailed, DownloadFailed, InstallAdoptFailed, InstallVerifyFailed, InstallLocateFailed, PreloadFailed:
		if !current {
			break
		}
		if e.Kind == DownloadPlanFailed && d.operation != downloadOperationPlan && d.operation != downloadOperationReconcile || e.Kind == InstallAdoptFailed && d.operation != downloadOperationAdopt || e.Kind == InstallVerifyFailed && d.operation != downloadOperationVerify || e.Kind == InstallLocateFailed && d.operation != downloadOperationLocate {
			break
		}
		if e.Kind == DownloadFailed || e.Kind == PreloadFailed {
			if !downloadBusy(*d) && d.Phase != DownloadPreparing {
				break
			}
		}
		// A preload that fails before its transaction started (rebuilding staged
		// patches, or committing without StageCommit) leaves the installation
		// untouched, so it must stay recoverable instead of demanding a recovery.
		if e.Kind == PreloadFailed && d.Kind == DownloadPreload && (d.Phase == DownloadRebuilding || d.Phase == DownloadCommitting && d.Stage != StageCommit) {
			d.operation = downloadOperationNone
			d.pausePending, d.cancelPending = false, false
			d.AwaitingConfirmation = false
			clearPreloadFlow()
			d.Phase, d.Stage = DownloadError, StageIdle
			d.Preload = PreloadStateWaitingRelease
			d.Error = downloadErrorText(e.Message)
			d.Rate, d.ETA = 0, 0
			break
		}
		wasReconcile := d.operation == downloadOperationReconcile
		d.operation = downloadOperationNone
		d.preparingUpdateBaseline = false
		d.pausePending, d.cancelPending = false, false
		d.AwaitingConfirmation = false
		clearPreloadFlow()
		if d.Phase == DownloadCommitting {
			d.Phase = DownloadRecoveryRequired
			d.Note = "recovery-required"
		} else {
			d.Phase = DownloadError
		}
		d.Stage = StageIdle
		d.Error = downloadErrorText(e.Message)
		d.Rate, d.ETA = 0, 0
		if d.Kind == DownloadPreload {
			switch {
			case d.Phase == DownloadRecoveryRequired:
				d.Preload = PreloadStateRecoveryRequired
			case wasReconcile:
				d.Preload = PreloadStateWaitingRelease
			default:
				d.Preload = PreloadStateFailed
			}
		}
	}
	return m, nil, true
}

func downloadStageHasThroughput(stage DownloadStage) bool {
	return stage == StageNetwork || stage == StageVerify || stage == StageApply || stage == StageCommit || stage == StageComponents
}

func downloadAcceptProgress(d *DownloadState, e Event) bool {
	if e.Stage < StageNetwork || e.Stage > StageComponents || e.Downloaded < 0 || e.Total < 0 || e.Downloaded > e.Total || e.Rate < 0 || e.Reused < 0 || e.FilesDone > e.FilesTotal {
		return false
	}
	switch d.Phase {
	case DownloadRunning:
		if e.Stage != StageNetwork && e.Stage != StageVerify && e.Stage != StageComponents {
			return false
		}
	case DownloadVerifying:
		if e.Stage != StageVerify {
			return false
		}
	case DownloadRebuilding:
		if e.Stage != StageApply {
			return false
		}
	case DownloadCommitting:
		if e.Stage != StageCommit && e.Stage != StageFinalize {
			return false
		}
	default:
		return false
	}
	if e.Stage < d.Stage {
		return false
	}
	if e.Stage == d.Stage && d.stageReported && (e.Downloaded < d.Downloaded || e.Total < d.Total || e.FilesDone < d.FilesDone || e.FilesTotal < d.FilesTotal) {
		return false
	}
	d.Stage = e.Stage
	d.stageReported = true
	d.Downloaded, d.Total, d.FilesDone, d.FilesTotal = e.Downloaded, e.Total, e.FilesDone, e.FilesTotal
	d.Reused = e.Reused
	d.Rate, d.ETA = 0, 0
	if downloadStageHasThroughput(e.Stage) {
		d.Rate = e.Rate
		if e.ETASecs > 0 {
			d.ETA = int64(e.ETASecs)
		}
	}
	if e.Stage == StageVerify {
		d.Phase = DownloadVerifying
		if d.Kind == DownloadPreload {
			d.Preload = PreloadStateVerifying
		}
	}
	return true
}
func downloadErrorText(message string) string {
	// Backend owns localization. Reject raw ASCII diagnostics and bound UI text.
	chars := []rune(message)
	chinese := false
	for _, r := range chars {
		if r >= 0x3400 && r <= 0x9fff {
			chinese = true
			break
		}
	}
	if !chinese {
		return "操作未完成，请重试；若持续失败请联系维护者"
	}
	if len(chars) > 120 {
		chars = chars[:120]
	}
	return string(chars)
}
