// SPDX-License-Identifier: GPL-3.0-or-later

package state

// PreloadPresentation is the compact dock chip beside the primary action. It
// only covers preloading: an update or install always owns the primary button,
// so the two never describe competing tasks.
type PreloadPresentation struct {
	Visible bool
	Label   string
	Detail  string
	Percent int
	ShowBar bool
	Action  EventKind
	Enabled bool
}

// PresentPreload projects the preload chip. It hides itself whenever a real
// update or install is under way, so the dock keeps one primary story.
func PresentPreload(m Model) PreloadPresentation {
	d := m.Download
	if !m.NormalMode || m.Page != Home || m.Managed.Root == "" || m.Overlay == OverlayInstallPath {
		return PreloadPresentation{}
	}
	if ManagedBusy(m.Managed.Phase) || ManagedActive(m.Managed.Phase) || downloadMutation(d) {
		return PreloadPresentation{}
	}
	if d.Kind == DownloadPreload && d.preloadAutoApply {
		return PreloadPresentation{}
	}
	// A paused or unconfirmed ordinary task owns the dock: offering preload
	// reconciliation next to it would start a second, competing download.
	if d.Kind != DownloadPreload && downloadUnfinished(d) {
		return PreloadPresentation{}
	}
	p := PresentDownload(m)
	networkOK := DownloadNetworkBlocked(m) == ""
	percent := 0
	if d.Total > 0 {
		percent = int(float64(d.Downloaded) / float64(d.Total) * 100)
	}
	showBar := d.Total > 0 && d.Kind == DownloadPreload && (d.Phase == DownloadRunning || d.Phase == DownloadSuspended)
	chip := func(label, detail string, action EventKind, enabled bool) PreloadPresentation {
		return PreloadPresentation{Visible: true, Label: label, Detail: detail, Percent: percent, ShowBar: showBar, Action: action, Enabled: enabled}
	}
	switch {
	case d.Preload == PreloadStateReconcile:
		return chip("预下载核对中", "正在核对正式版本", 0, false)
	case d.Kind == DownloadPreload && d.AwaitingConfirmation && d.SpaceShort:
		return chip("预下载：空间不足", DownloadNoteText("disk-short"), PreloadRequest, true)
	case d.Kind == DownloadPreload && d.Phase == DownloadRunning:
		return chip("预下载中", "点击暂停预下载", DownloadPause, p.CanPause)
	case d.Kind == DownloadPreload && d.Phase == DownloadSuspended:
		return chip("预下载已暂停", "点击继续预下载", DownloadResume, p.CanResume)
	case d.Kind == DownloadPreload && (d.Phase == DownloadPreparing || d.Phase == DownloadVerifying):
		label := "预下载准备中"
		if d.Phase == DownloadVerifying {
			label = "预下载校验中"
		}
		return chip(label, p.StageText, 0, false)
	case d.Kind == DownloadPreload && d.Phase == DownloadError && d.Preload == PreloadStateFailed:
		return chip("预下载失败", p.Error, PreloadRequest, networkOK && !downloadUnfinished(d) && downloadCanDiscoverPreload(d))
	case d.Preload == PreloadStateWaitingRelease:
		if p.OfferVersion != "" && p.OfferVersion != p.Current {
			return chip("预下载待核对", "正式版本已发布，更新时自动使用预下载内容；点击可立即核对", PreloadReconcileRequest, p.CanReconcile)
		}
		detail := "预下载已完成，正式版更新时自动使用；点击可核对版本"
		if p.Error != "" {
			detail = p.Error
		}
		return chip("预下载已就绪", detail, PreloadReconcileRequest, p.CanReconcile)
	case d.Preload == PreloadStateApplicable:
		return chip("预下载已就绪", "点击安装预下载内容", PreloadApplyRequest, p.CanApply)
	case (d.Preload == PreloadStateStaged || d.Preload == PreloadStateStale) && p.CanReconcile:
		return chip("预下载待核对", "正式版本已发布，点击核对", PreloadReconcileRequest, true)
	case d.Kind == DownloadPreload && (d.Phase == DownloadError || d.Phase == DownloadStale):
		return chip("预下载失败", p.Error, PreloadRequest, networkOK && !downloadUnfinished(d) && downloadCanDiscoverPreload(d))
	case d.Preload == PreloadStateAvailable:
		detail := "检测到可预下载的版本"
		if p.PreloadOfferVersion != "" {
			detail = "可预下载版本 " + p.PreloadOfferVersion
		}
		return chip("预下载", detail, PreloadRequest, p.CanCheckPreload)
	case (d.Preload == PreloadStateUnavailable || d.Preload == PreloadStateCompleted) && p.PreloadOfferVersion != "" && d.Phase != DownloadRunning:
		return chip("预下载", "可预下载版本 "+p.PreloadOfferVersion, PreloadRequest, p.CanCheckPreload)
	default:
		return PreloadPresentation{}
	}
}
