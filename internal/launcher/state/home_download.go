// SPDX-License-Identifier: GPL-3.0-or-later

package state

// homeDownloadPrimary shares presentation and dispatch so the main button
// cannot say one thing and start a different operation.
func homeDownloadPrimary(m Model) (Presentation, EventKind, bool) {
	if !m.NormalMode || downloadLaunchBlocked(m) || ManagedBusy(m.Managed.Phase) || ManagedActive(m.Managed.Phase) {
		return Presentation{}, 0, false
	}
	d, p := m.Download, PresentDownload(m)
	if p.OfferVersion != "" && p.OfferVersion != p.Current {
		return Presentation{Primary: "更新游戏", Detail: "可用正式版本 " + p.OfferVersion, Enabled: true}, DownloadRequest, true
	}
	// Preloading alone must leave the ordinary launch button reachable, unless
	// the user started a formal update backed by a staged preload.
	if d.Kind != DownloadUpdate && d.Kind != DownloadInstall && !(d.Kind == DownloadPreload && d.preloadAutoApply) {
		return Presentation{}, 0, false
	}
	out := Presentation{Task: true, Status: p.StageText, Detail: p.NextAction}
	if p.Error != "" {
		out.Detail = p.Error
	}
	status := func(label string) (Presentation, EventKind, bool) {
		out.Primary = label
		return out, 0, true
	}
	action := func(label string, event EventKind) (Presentation, EventKind, bool) {
		out.Primary, out.Enabled = label, true
		return out, event, true
	}
	switch {
	case d.cancelPending:
		return status("正在取消")
	case d.pausePending:
		return status("正在暂停")
	case d.Kind == DownloadPreload && d.Preload == PreloadStateReconcile:
		return status("正在核对预下载")
	case d.AwaitingConfirmation && m.Overlay != OverlayInstallPath:
		if d.SpaceShort {
			out.Detail = DownloadNoteText("disk-short")
			out.Task = false
			return action("空间不足", DownloadRequest)
		}
		return status("准备开始下载")
	case d.Phase == DownloadRunning && p.CanPause:
		return action("暂停下载", DownloadPause)
	case d.Phase == DownloadSuspended:
		if d.operation == downloadOperationRun {
			return status("正在继续")
		}
		if p.CanResume {
			return action("继续下载", DownloadResume)
		}
		return status("下载已暂停")
	case d.Phase == DownloadPreparing:
		if d.Stage == StageNetwork {
			return status("正在准备下载")
		}
		return status("正在检查更新")
	case d.Phase == DownloadRunning && d.Stage == StageComponents:
		return status("正在准备运行组件")
	case d.Phase == DownloadVerifying:
		return status("检查游戏文件")
	case d.Phase == DownloadError || d.Phase == DownloadStale:
		out.Task = false
		if d.Kind == DownloadUpdate {
			if hasFormalUpdateOffer(m) {
				return action("重试更新", DownloadRequest)
			}
			if d.Located || d.Verified {
				return Presentation{}, 0, false
			}
		}
		if d.Kind == DownloadUpdate || d.Kind == DownloadInstall {
			return action("重试下载", DownloadRequest)
		}
		return Presentation{}, 0, false
	default:
		return Presentation{}, 0, false
	}
}
