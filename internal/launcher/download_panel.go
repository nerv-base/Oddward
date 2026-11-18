// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func (s *Shell) homeDownloads(c *ui.Context) {
	p := state.PresentDownload(s.Model)
	bottom := homeBottom + actionHeight + homeDownloadGap + homeDownloadDockClearance
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

func (s *Shell) downloadControls(c *ui.Context, p state.DownloadPresentation) {
	ui.Scroll(c.Key("download-content")).Grow(1).MinHeight(0).FillWidth().Scrollbars(true).Children(func() {
		ui.Column(c).FillWidth().Gap(downloadGap).Children(func() {
			ui.Text(c, p.StageText).FontSize(downloadBodySize).FontWeight(600).TextColor(s.tone())
			freshInstall := s.Model.Download.Kind == state.DownloadInstall
			formatBytes := megabytes
			if freshInstall {
				formatBytes = installSize
			}
			if root := s.Model.Managed.Root; freshInstall {
				ui.Text(c, "安装目录："+root).Tooltip(root).FontSize(downloadCaptionSize).TextColor(muted).SingleLine()
			}
			if p.OfferVersion != "" {
				ui.Text(c, "可用正式版本："+p.OfferVersion).FontSize(downloadCaptionSize).TextColor(muted)
			}
			if p.PreloadOfferVersion != "" {
				ui.Text(c, "可预下载版本："+p.PreloadOfferVersion).FontSize(downloadCaptionSize).TextColor(s.tone())
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
			if !freshInstall {
				ui.Text(c, p.RouteText).FontSize(downloadBodySize).LineHeight(1.5)
			}
			if p.CacheRoot != "" {
				ui.Text(c, "下载暂存目录："+p.CacheRoot).FontSize(downloadCaptionSize).TextColor(muted).LineHeight(1.5)
			}
			if p.StorageNote != "" {
				ui.Text(c, p.StorageNote).FontSize(downloadCaptionSize).TextColor(s.tone()).LineHeight(1.5)
			}
			if !freshInstall && p.Version != "" && (s.Model.Download.Stage == state.StagePlanning || s.Model.Download.Stage == state.StageNetwork) {
				ui.Text(c, "下载总量："+megabytes(p.Total)+" · 其中补丁 "+megabytes(p.PatchBytes)).FontSize(downloadCaptionSize).TextColor(muted).LineHeight(1.5)
			}
			if p.Version != "" && (!freshInstall || p.Reused > 0) {
				ui.Text(c, "本地复用："+formatBytes(p.Reused)).FontSize(downloadCaptionSize).TextColor(muted)
			}
			if p.FallbackNote {
				ui.Text(c, "部分文件改为完整下载").FontSize(downloadCaptionSize).TextColor(s.tone())
			}
			if p.Note != "" && (!p.FallbackNote || p.Note != "部分文件改为完整下载") {
				ui.Text(c, p.Note).FontSize(downloadCaptionSize).TextColor(s.tone()).LineHeight(1.5)
			}
			switch s.Model.Download.Stage {
			case state.StageNetwork:
				ui.Text(c, formatBytes(p.Downloaded)+" / "+formatBytes(p.Total)).FontSize(downloadBodySize)
				if p.ShowRate {
					ui.Text(c, formatBytes(p.Rate)+"/s").FontSize(downloadCaptionSize).TextColor(muted)
				} else if s.Model.Download.Phase == state.DownloadRunning {
					ui.Text(c, "速度与剩余时间：计算中").FontSize(downloadCaptionSize).TextColor(muted)
				}
				if p.ShowETA {
					ui.Text(c, fmt.Sprintf("剩余时间：%d 秒", p.ETA)).FontSize(downloadCaptionSize).TextColor(muted)
				} else if p.ShowRate && s.Model.Download.Phase == state.DownloadRunning {
					ui.Text(c, "剩余时间：计算中").FontSize(downloadCaptionSize).TextColor(muted)
				}
			case state.StageVerify:
				ui.Text(c, fmt.Sprintf("已校验文件：%d / %d", p.FilesDone, p.FilesTotal)).FontSize(downloadBodySize)
			case state.StageApply:
				ui.Text(c, fmt.Sprintf("重建文件：%d / %d", p.FilesDone, p.FilesTotal)).FontSize(downloadBodySize)
				ui.Text(c, "本次重建写入："+megabytes(p.Downloaded)+" / "+megabytes(p.Total)).FontSize(downloadCaptionSize).TextColor(muted)
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
			if p.Error != "" {
				ui.Text(c, p.Error).FontSize(downloadCaptionSize).TextColor(s.tone()).LineHeight(1.5)
			}
			if p.NextAction != "" && p.NextAction != p.Note {
				ui.Text(c, p.NextAction).FontSize(downloadCaptionSize).TextColor(muted).LineHeight(1.5)
			}
			ui.Box(c).FillWidth().Height(1).Background(hairline)
			button := func(key, label string, enabled bool, event state.EventKind) {
				s.quietButton(c, key, label, !enabled).FillWidth().Height(downloadButtonHeight).OnClick(func() { s.Dispatch(state.Event{Kind: event}) })
			}
			if p.CanPause {
				button("download-pause", "暂停下载", true, state.DownloadPause)
			}
			if p.CanResume {
				button("download-resume", "继续下载", true, state.DownloadResume)
			}
			if p.CanCancel {
				button("download-cancel", "取消任务", true, state.DownloadCancel)
			}
			if p.CanRecover {
				button("download-recover", "完成上次更新", true, state.DownloadRecoverRequest)
			}
			if p.CanApply {
				button("download-apply", "应用预下载", true, state.PreloadApplyRequest)
			}
			if s.Model.Download.Phase == state.DownloadError || s.Model.Download.Phase == state.DownloadStale || p.SpaceShort {
				s.quietButton(c, "download-replan", "重新规划", false).FillWidth().Height(downloadButtonHeight).OnClick(func() {
					kind := state.DownloadRequest
					if s.Model.Download.Kind == state.DownloadPreload {
						kind = state.PreloadRequest
					}
					s.Dispatch(state.Event{Kind: kind, Enabled: s.Model.Download.Kind == state.DownloadInstall})
				})
			}
			if p.CanSelectCache {
				button("download-cache-select", "选择下载暂存目录", true, state.DownloadCacheSelect)
			}
			if p.CanVerify {
				button("download-verify", "重新校验", true, state.InstallVerifyRequest)
			}
			if p.CanCheckPreload {
				button("preload-check", "检查预下载", true, state.PreloadCheckRequest)
			}
			if p.CanReconcile {
				button("preload-reconcile", "核对正式版本", true, state.PreloadReconcileRequest)
			}
			if p.CanDiscard {
				button("preload-discard", "清理预下载", true, state.PreloadDiscardRequest)
			}
			if s.Model.Download.Phase == state.DownloadIdle || s.Model.Download.Phase == state.DownloadDone {
				label := "检查游戏更新"
				install := s.Model.Download.Kind == state.DownloadInstall && !s.Model.Download.Located && !s.Model.Download.Verified
				if install {
					label = "规划安装"
				}
				s.quietButton(c, "download-plan", label, false).FillWidth().Height(downloadButtonHeight).OnClick(func() { s.Dispatch(state.Event{Kind: state.DownloadRequest, Enabled: install}) })
				if s.Model.Download.Preload == state.PreloadStateAvailable {
					button("preload-plan", "开始预下载", true, state.PreloadRequest)
				}
			}
		})
	})
}

func (s *Shell) downloadStatus(c *ui.Context, locateExisting bool) {
	if !s.Model.NormalMode {
		return
	}
	bottom := downloadStatusBottom
	label := "游戏下载与更新"
	if s.Model.Download.Phase != state.DownloadIdle || s.Model.Download.Preload != state.PreloadStateUnavailable {
		label = state.PresentDownload(s.Model).StageText
		if locateExisting {
			bottom -= downloadStatusHeight + locateActionGap
		}
	} else if locateExisting {
		return
	}
	s.quietButton(c, "download-status", label, false).Absolute().Right(dockRight + actionHeight + menuGap).Bottom(bottom).Width(actionWidth).Height(downloadStatusHeight).OnClick(func() {
		s.Dispatch(state.Event{Kind: state.OpenHomeDownloads})
	})
}
