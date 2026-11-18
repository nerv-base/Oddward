// SPDX-License-Identifier: GPL-3.0-or-later

package state

import "strconv"

// DownloadPresentation is the panel's pure aggregate projection. Zero rates
// and unknown times remain absent; it never estimates numbers from file counts.
type DownloadPresentation struct {
	CacheRoot, StorageNote, BudgetVolume                                        string
	CanSelectCache                                                              bool
	StageText, RouteText, Note, Error, NextAction                               string
	OfferVersion, PreloadOfferVersion                                           string
	Version, Current                                                            string
	Downloaded, Total, PatchBytes, Reused, Rate, ETA                            int64
	FilesDone, FilesTotal                                                       uint16
	ShowProgress, ShowRate, ShowETA                                             bool
	CanCheckPreload, CanReconcile, CanDiscard                                   bool
	CanConfirm, CanPause, CanResume, CanCancel, CanVerify, CanApply, CanRecover bool
	AwaitingConfirmation, SpaceShort, FallbackNote                              bool
	Needed, Free, Suggested                                                     int64
}

func PresentDownload(m Model) DownloadPresentation {
	d := m.Download
	cacheRoot := DownloadCacheRoot(m)
	p := DownloadPresentation{Version: d.Version, Current: d.Current, Downloaded: d.Downloaded, Total: d.Total, PatchBytes: d.PatchBytes, Reused: d.Reused, FilesDone: d.FilesDone, FilesTotal: d.FilesTotal, Note: DownloadNoteText(d.Note), Error: d.Error, AwaitingConfirmation: d.AwaitingConfirmation, SpaceShort: d.SpaceShort, FallbackNote: d.FallbackNote, Needed: d.Needed, Free: d.Free, Suggested: d.Suggested}
	p.CacheRoot, p.CanSelectCache, p.BudgetVolume = cacheRoot, canSelectDownloadCache(m), "安装卷"
	if d.CrossVolume {
		p.StorageNote = "跨卷安装：下载暂存位于另一磁盘；校验后复制到安装卷，仍需预留完整安装空间"
	}
	if d.CacheBudget {
		p.BudgetVolume = "下载暂存卷"
	}
	if downloadOfferIdle(m) && d.offerResultRoot != "" && SameManagedRoot(d.offerResultRoot, m.Managed.Root) {
		p.OfferVersion, p.PreloadOfferVersion = d.OfferVersion, d.PreloadOfferVersion
	}
	switch d.Stage {
	case StagePlanning:
		p.StageText = "正在获取版本信息 / 规划更新"
	case StageNetwork:
		p.StageText = "正在下载"
	case StageVerify:
		p.StageText = "正在校验"
	case StageApply:
		p.StageText = "正在重建文件"
	case StageCommit:
		p.StageText = "正在提交更新"
	case StageFinalize:
		p.StageText = "正在完成"
	default:
		p.StageText = "尚未开始"
	}
	switch d.Phase {
	case DownloadSuspended:
		p.StageText = "下载已暂停"
	case DownloadDone:
		p.StageText = "任务已完成"
	case DownloadError:
		p.StageText = "操作未完成"
	case DownloadRecoveryRequired:
		p.StageText = "上次更新未完成"
	case DownloadStale:
		p.StageText = "目标版本信息已变化"
	}
	switch d.Route {
	case RouteAllReuse:
		p.RouteText = "文件已是最新，无需下载"
	case RoutePatch:
		p.RouteText = "使用官方差分补丁"
	case RouteFull:
		p.RouteText = "当前没有可用补丁，将下载完整资源"
	case RouteMixed:
		p.RouteText = "使用官方差分补丁与完整资源"
	default:
		p.RouteText = "正在确认下载路线"
	}
	if d.FallbackNote && p.Note == "" {
		p.Note = DownloadNoteText("fallback-full")
	}
	if d.Kind == DownloadPreload && d.Phase == DownloadRunning && d.Stage == StageNetwork && d.GameRunning && !d.OfficialRunning && !d.pausePending && !d.cancelPending && p.Note == "" && p.Error == "" {
		limit := 512
		if user := d.Settings.RateLimitKiB; user > 0 && user < limit {
			limit = user
		}
		p.Note = "游戏运行中，预下载限速上限为 " + strconv.Itoa(limit) + " KiB/s"
	}
	p.ShowProgress = d.Total > 0 && (d.Stage == StageNetwork || d.Stage == StageApply) && d.Phase != DownloadPreparing
	if d.Stage == StageNetwork && d.Phase == DownloadRunning && !d.pausePending && !d.cancelPending {
		p.Rate, p.ETA = d.Rate, d.ETA
		p.ShowRate = d.Rate > 0
		p.ShowETA = d.ETA > 0
	}
	networkOK := DownloadNetworkBlocked(m) == ""
	waiting := d.pausePending || d.cancelPending
	p.CanConfirm = d.AwaitingConfirmation && !d.SpaceShort && !waiting && d.operation == downloadOperationNone && networkOK
	if d.Phase == DownloadVerifying {
		p.CanConfirm = p.CanConfirm && DownloadWriteBlocked(m) == ""
	}
	p.CanPause = (d.Phase == DownloadRunning || d.Phase == DownloadVerifying) && !waiting
	p.CanResume = d.Phase == DownloadSuspended && !waiting && networkOK
	p.CanCancel = downloadUnfinished(d) && !downloadMutation(d) && d.Phase != DownloadRecoveryRequired && !d.cancelPending
	p.CanVerify = m.Managed.Root != "" && !downloadUnfinished(d) && (!m.NormalMode || !ManagedBusy(m.Managed.Phase) && !ManagedActive(m.Managed.Phase))
	p.CanCheckPreload = m.Managed.Root != "" && !downloadUnfinished(d) && networkOK && downloadCanDiscoverPreload(d)
	p.CanReconcile = m.Managed.Root != "" && !downloadBusy(d) && (d.Preload == PreloadStateWaitingRelease || d.Preload == PreloadStateStaged || d.Preload == PreloadStateStale)
	p.CanDiscard = !downloadBusy(d) && d.Preload != PreloadStateUnavailable && d.Preload != PreloadStateApplying && d.Preload != PreloadStateRecoveryRequired
	p.CanApply = d.Preload == PreloadStateApplicable && !downloadBusy(d) && DownloadWriteBlocked(m) == ""
	p.CanRecover = d.Phase == DownloadRecoveryRequired && d.operation != downloadOperationRecover
	if d.Note != "" {
		p.NextAction = DownloadNoteText(d.Note)
	}
	if d.Phase == DownloadError {
		p.NextAction = "请重试；若持续失败请联系维护者"
	}
	if d.SpaceShort {
		p.NextAction = DownloadNoteText("disk-short")
	}
	if d.Phase == DownloadRecoveryRequired {
		p.NextAction = "请完成上次更新；无法恢复时请联系维护者"
		p.CanConfirm, p.CanPause, p.CanResume, p.CanCancel, p.CanVerify, p.CanApply = false, false, false, false, false, false
	}
	if d.Preload == PreloadStateWaitingRelease {
		p.StageText = "预下载已完成，等待正式发布"
	}
	if d.Preload == PreloadStateApplicable {
		p.StageText = "预下载已就绪，可以应用"
	}
	return p
}
