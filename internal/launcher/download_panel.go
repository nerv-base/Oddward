// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"fmt"
	"github.com/egoist/mygo/ui"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func (s *Shell) confirmDownloadCancel() {
	s.Dispatch(state.Event{Kind: state.OpenOverlay, Overlay: state.ConfirmDownloadCancel})
}

func (s *Shell) downloadCancelPanel(c *ui.Context) {
	ui.Text(c, "取消后，当前任务的未完成进度无法继续。想稍后接着下载，可以返回并暂停任务。").
		FontSize(downloadBodySize).TextColor(muted).LineHeight(1.5)
	ui.Box(c).Grow(1)
	ui.Row(c).FillWidth().Gap(downloadGap).Justify(ui.End).Children(func() {
		s.quietButton(c, "keep-download", "返回任务", false).Height(downloadButtonHeight).
			OnClick(func() { s.Dispatch(state.Event{Kind: state.Dismiss}) })
		s.quietButton(c, "confirm-cancel-download", "确认取消", !state.PresentDownload(s.Model).CanCancel).
			Height(downloadButtonHeight).TextColor(warning).
			OnClick(func() { s.Dispatch(state.Event{Kind: state.DownloadCancel}) })
	})
}

func (s *Shell) homeDownloads(c *ui.Context) {
	p := state.PresentDownload(s.Model)
	bottom := homeBottom + homeDockHeight + homeDownloadGap
	ui.Column(c.Key("home-downloads")).Role(ui.RoleGroup).Label("游戏下载与更新").Absolute().Right(dockRight).Bottom(bottom).
		Size(homeDownloadWidth, homeDownloadHeight).Padding(homeDownloadInset).Gap(downloadGap).Radius(sheetRadius).Clip().
		Border(1, hairline).Material(frostedSurface{Tint: ui.RGBA(18, 19, 24, .84)}).Children(func() {
		ui.Row(c).FillWidth().AlignItems(ui.Center).Height(30).Children(func() {
			ui.Text(c, "游戏下载与更新").FontSize(downloadTitleSize).FontWeight(700).Grow(1)
			iconButton(c, "home-download-collapse", "收起游戏下载与更新", "\uE8BB", 28, func() {
				s.Dispatch(state.Event{Kind: state.CloseHomeDownloads})
			})
		})
		s.downloadControls(c, p)
		if p.CanConfirm {
			s.accentButton(c, "download-confirm", "确认并开始").FillWidth().Height(downloadButtonHeight).Shrink(0).OnClick(func() {
				s.Dispatch(state.Event{Kind: state.DownloadConfirm})
			})
		}
	})
}

// downloadControls keeps the running task readable at a glance: stage, size,
// speed, remaining time and the immediate actions. Install paths, patching
// routes and maintenance actions live behind 详细信息 so they do not compete
// with the one question a player has.
func (s *Shell) downloadControls(c *ui.Context, p state.DownloadPresentation) {
	freshInstall := s.Model.Download.Kind == state.DownloadInstall
	formatBytes := megabytes
	if freshInstall {
		formatBytes = installSize
	}
	scroll := ui.Scroll(c.Key("download-content")).Grow(1).MinHeight(0).FillWidth().Scrollbars(true)
	advanced := ui.Local(scroll, "advanced", func() bool { return false })
	scroll.Children(func() {
		ui.Column(c).FillWidth().Gap(downloadGap).Children(func() {
			ui.Text(c, p.StageText).FontSize(downloadBodySize).FontWeight(600).TextColor(s.tone())
			switch s.Model.Download.Stage {
			case state.StageNetwork:
				ui.Text(c, formatBytes(p.Downloaded)+" / "+formatBytes(p.Total)).FontSize(downloadBodySize)
				if p.ShowRate {
					ui.Text(c, formatBytes(p.Rate)+"/s").FontSize(downloadCaptionSize).TextColor(muted)
				} else if s.Model.Download.Phase == state.DownloadRunning {
					ui.Text(c, "速度与剩余时间：计算中").FontSize(downloadCaptionSize).TextColor(muted)
				}
				if p.ShowETA {
					ui.Text(c, "剩余时间："+formatDuration(p.ETA)).FontSize(downloadCaptionSize).TextColor(muted)
				} else if p.ShowRate && s.Model.Download.Phase == state.DownloadRunning {
					ui.Text(c, "剩余时间：计算中").FontSize(downloadCaptionSize).TextColor(muted)
				}
			case state.StageVerify:
				ui.Text(c, fmt.Sprintf("已校验文件：%d / %d", p.FilesDone, p.FilesTotal)).FontSize(downloadBodySize)
			case state.StageApply:
				ui.Text(c, fmt.Sprintf("重建文件：%d / %d", p.FilesDone, p.FilesTotal)).FontSize(downloadBodySize)
			case state.StageComponents:
				ui.Text(c, fmt.Sprintf("运行组件：%s / %s", formatBytes(p.Downloaded), formatBytes(p.Total))).FontSize(downloadBodySize)
			case state.StageCommit, state.StageFinalize:
				ui.Text(c, "请等待提交完成后再启动游戏。").FontSize(downloadCaptionSize).TextColor(s.tone())
			}
			if p.ShowProgress {
				ui.Progress(c.Key("download-progress"), float64(p.Downloaded)/float64(p.Total)).Label("游戏下载进度").FillWidth().Height(downloadMeterHeight).MinHeight(downloadMeterHeight)
			}
			if p.SpaceShort {
				ui.Text(c, "可用空间不足").FontSize(downloadBodySize).TextColor(s.tone())
			}
			if p.SpaceShort || p.AwaitingConfirmation && (p.Needed > 0 || p.Free > 0) {
				ui.Text(c, p.BudgetVolume+"最低所需："+megabytes(p.Needed)).FontSize(downloadCaptionSize)
				ui.Text(c, p.BudgetVolume+"可用空间："+megabytes(p.Free)).FontSize(downloadCaptionSize).TextColor(muted)
				if p.Suggested > 0 {
					ui.Text(c, "建议释放："+megabytes(p.Suggested)).FontSize(downloadCaptionSize).TextColor(muted)
				}
			}
			if p.CacheRoot != "" {
				ui.Text(c, "下载暂存目录："+p.CacheRoot).Tooltip(p.CacheRoot).FontSize(downloadCaptionSize).TextColor(muted).SingleLine()
			}
			if p.StorageNote != "" {
				ui.Text(c, p.StorageNote).FontSize(downloadCaptionSize).TextColor(s.tone()).LineHeight(1.5)
			}
			if !freshInstall {
				ui.Text(c, p.RouteText).FontSize(downloadCaptionSize).TextColor(muted).LineHeight(1.5)
			}
			if p.FallbackNote {
				ui.Text(c, "部分文件改为完整下载").FontSize(downloadCaptionSize).TextColor(s.tone())
			}
			if p.Note != "" && (!p.FallbackNote || p.Note != "部分文件改为完整下载") {
				ui.Text(c, p.Note).FontSize(downloadCaptionSize).TextColor(s.tone()).LineHeight(1.5)
			}
			if p.OfferVersion != "" {
				ui.Text(c, "可用正式版本："+p.OfferVersion).FontSize(downloadCaptionSize).TextColor(muted)
			}
			if p.PreloadOfferVersion != "" {
				ui.Text(c, "可预下载版本："+p.PreloadOfferVersion).FontSize(downloadCaptionSize).TextColor(s.tone())
			}
			if p.Error != "" {
				ui.Text(c, p.Error).FontSize(downloadCaptionSize).TextColor(s.tone()).LineHeight(1.5)
			}
			if p.NextAction != "" && p.NextAction != p.Note {
				ui.Text(c, p.NextAction).FontSize(downloadCaptionSize).TextColor(muted).LineHeight(1.5)
			}
			ui.Box(c).FillWidth().Height(1).Background(hairline)
			button := func(key, label string, event state.EventKind) {
				s.quietButton(c, key, label, false).FillWidth().Height(downloadButtonHeight).OnClick(func() { s.Dispatch(state.Event{Kind: event}) })
			}
			if p.CanPause {
				button("download-pause", "暂停下载", state.DownloadPause)
			}
			if p.CanResume {
				button("download-resume", "继续下载", state.DownloadResume)
			}
			if p.CanCancel {
				s.quietButton(c, "download-cancel", "取消任务", false).FillWidth().Height(downloadButtonHeight).OnClick(s.confirmDownloadCancel)
			}
			needsRecovery := s.Model.Download.Phase == state.DownloadError || s.Model.Download.Phase == state.DownloadStale || p.SpaceShort
			if needsRecovery {
				s.quietButton(c, "download-replan", "重新规划", false).FillWidth().Height(downloadButtonHeight).OnClick(func() {
					kind := state.DownloadRequest
					if s.Model.Download.Kind == state.DownloadPreload {
						kind = state.PreloadRequest
					}
					s.Dispatch(state.Event{Kind: kind, Enabled: s.Model.Download.Kind == state.DownloadInstall})
				})
			}
			if s.Model.Download.Phase == state.DownloadError && p.CanVerify {
				button("download-verify", "重新校验", state.InstallVerifyRequest)
			}
			if p.Error != "" {
				diagnostics := fmt.Sprintf("Oddward 下载诊断\n阶段：%s\n任务：%s\n版本：%s\n错误：%s\n提示：%s\n目录：%s",
					p.StageText, fmt.Sprint(s.Model.Download.Phase), p.Version, p.Error, p.Note, s.Model.Managed.Root)
				s.quietButton(c, "download-copy-diagnostics", "复制诊断信息", false).FillWidth().Height(downloadButtonHeight).OnClick(func() {
					c.WriteClipboard(diagnostics)
				})
			}
			if p.CanRecover {
				button("download-recover", "完成上次更新", state.DownloadRecoverRequest)
			}
			if p.CanSelectCache {
				button("download-cache-select", "选择下载暂存目录", state.DownloadCacheSelect)
			}
			if s.Model.Download.Phase == state.DownloadIdle && p.CanCheckPreload {
				button("preload-check", "检查预下载", state.PreloadCheckRequest)
			}
			label := "详细信息"
			if *advanced {
				label = "收起详细信息"
			}
			s.quietButton(c, "download-advanced-toggle", label, false).FillWidth().Height(downloadButtonHeight).OnClick(func() {
				*advanced = !*advanced
				c.Invalidate()
			})
			if !*advanced {
				return
			}
			ui.Box(c).FillWidth().Height(1).Background(hairline)
			if root := s.Model.Managed.Root; root != "" {
				ui.Text(c, "安装目录："+root).Tooltip(root).FontSize(downloadCaptionSize).TextColor(muted).SingleLine()
			}

			if p.Version != "" {
				label := "本次更新：" + p.Current + " → " + p.Version
				if s.Model.Download.Kind == state.DownloadPreload {
					label = "预下载版本：" + p.Version
				}
				if s.Model.Download.Kind == state.DownloadInstall {
					label = "安装版本：" + p.Version
				}
				ui.Text(c, label).FontSize(downloadBodySize).SingleLine()
			}

			if !freshInstall && p.Version != "" && (s.Model.Download.Stage == state.StagePlanning || s.Model.Download.Stage == state.StageNetwork) {
				ui.Text(c, "下载总量："+megabytes(p.Total)+" · 其中补丁 "+megabytes(p.PatchBytes)).FontSize(downloadCaptionSize).TextColor(muted).LineHeight(1.5)
			}
			if p.Version != "" && (!freshInstall || p.Reused > 0) {
				ui.Text(c, "本地复用："+formatBytes(p.Reused)).FontSize(downloadCaptionSize).TextColor(muted)
			}
			if p.CanApply {
				button("download-apply", "应用预下载", state.PreloadApplyRequest)
			}

			if p.CanReconcile {
				button("preload-reconcile", "核对正式版本", state.PreloadReconcileRequest)
			}
			if p.CanDiscard {
				button("preload-discard", "清理预下载", state.PreloadDiscardRequest)
			}
			if s.Model.Download.Phase == state.DownloadIdle || s.Model.Download.Phase == state.DownloadDone {
				label := "检查游戏更新"
				install := s.Model.Download.Kind == state.DownloadInstall && !s.Model.Download.Located && !s.Model.Download.Verified
				if install {
					label = "规划安装"
				}
				s.quietButton(c, "download-plan", label, false).FillWidth().Height(downloadButtonHeight).OnClick(func() {
					s.Dispatch(state.Event{Kind: state.DownloadRequest, Enabled: install})
				})
				if s.Model.Download.Preload == state.PreloadStateAvailable {
					button("preload-plan", "开始预下载", state.PreloadRequest)
				}
			}
		})
	})
}
