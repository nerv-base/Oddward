// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

const (
	installDialogWidth     float32 = 510
	installDialogHeight    float32 = 318
	installDialogEmpty     float32 = 274
	installDialogInset     float32 = 24
	installDialogGap       float32 = 16
	installPathHeight      float32 = 40
	installCloseSize       float32 = 32
	installDialogTitleSize float32 = 22
)

func (s *Shell) installPathDialog(c *ui.Context) {
	const title = "选择安装路径"
	p := state.PresentDownload(s.Model)
	root := s.Model.Managed.Root
	path := root
	chooseLabel := "更改目录"
	if path == "" {
		path, chooseLabel = "尚未选择目录", "选择目录"
	}
	height := installDialogEmpty
	if p.AwaitingConfirmation || root != "" && p.Error != "" {
		height = installDialogHeight
	}
	ui.Overlay(c, func() {
		ui.Row(c.Key("install-backdrop")).Absolute().Left(0).Top(0).Right(0).Bottom(0).
			Justify(ui.Center).AlignItems(ui.Center).Background(ui.RGBA(6, 7, 10, .42)).Modal().Children(func() {
			w, h := c.Size()
			ui.Column(c.Key("install-dialog")).Role(ui.RoleDialog).Label(title).
				Size(min(installDialogWidth, w-2*sheetInset), min(height, h-2*sheetInset)).Padding(installDialogInset).Gap(installDialogGap).
				Radius(sheetRadius).Clip().Border(1, hairline).Material(frostedSurface{Tint: ui.RGBA(18, 19, 24, .80)}).Children(func() {
				ui.Row(c).Height(installCloseSize).FillWidth().AlignItems(ui.Center).Children(func() {
					ui.Text(c, title).FontSize(installDialogTitleSize).FontWeight(700).Grow(1)
					iconButton(c, "install-close", "关闭安装路径", "\uE8BB", installCloseSize, func() { s.Dispatch(state.Event{Kind: state.Dismiss}) })
				})
				ui.Column(c).Gap(6).FillWidth().Children(func() {
					ui.Text(c, "安装目录").FontSize(downloadCaptionSize).TextColor(muted)
					ui.Row(c).Gap(8).FillWidth().AlignItems(ui.Center).Children(func() {
						ui.Row(c).Grow(1).MinWidth(0).Height(installPathHeight).Padding(0, 12).Radius(10).Border(1, hairline).
							AlignItems(ui.Center).Clip().Tooltip(root).Children(func() {
							ui.Text(c, path).SingleLine().TextColor(paper).FontSize(downloadBodySize)
						})
						s.quietButton(c, "install-choose", chooseLabel, !state.CanSelectInstallPath(s.Model)).Height(installPathHeight).Shrink(0).OnClick(func() {
							s.Dispatch(state.Event{Kind: state.InstallLocateRequest, Enabled: true})
						})
					})
				})
				if p.AwaitingConfirmation {
					ui.Row(c).FillWidth().Gap(installDialogGap).Children(func() {
						installMetric(c, "下载量", p.Total)
						installMetric(c, p.BudgetVolume+"可用空间", p.Free)
						installMetric(c, p.BudgetVolume+"所需空间", p.Needed)
					})
					ui.Text(c, "下载时会一并安装运行组件（约 30 MB），不需要官方启动器。").FontSize(downloadCaptionSize).TextColor(muted).FillWidth()
				}
				note := p.Error
				if note == "" {
					switch {
					case p.SpaceShort:
						note = p.Note
					case p.StorageNote != "":
						note = p.StorageNote
					case root == "":
						note = "请选择空目录。确认前会检查下载量和磁盘空间。"
					case !p.AwaitingConfirmation:
						note = p.Note
						if note == "" {
							note = "正在检查下载量和磁盘空间…"
						}
					case !p.CanConfirm && state.DownloadNetworkBlocked(s.Model) != "":
						note = state.DownloadNoteText(state.DownloadNetworkBlocked(s.Model))
					}
				}
				if note != "" {
					color := muted
					if p.Error != "" || p.SpaceShort {
						color = s.tone()
					}
					ui.Text(c, note).FontSize(downloadCaptionSize).TextColor(color).FillWidth()
				}
				ui.Box(c).Grow(1)
				ui.Row(c).FillWidth().Gap(8).AlignItems(ui.Center).Children(func() {
					if p.CanSelectCache {
						s.quietButton(c, "install-cache", "选择下载暂存目录", false).OnClick(func() {
							s.Dispatch(state.Event{Kind: state.DownloadCacheSelect})
						})
					}
					ui.Box(c).Grow(1)
					if root != "" && state.CanSelectInstallPath(s.Model) && (p.SpaceShort || !p.AwaitingConfirmation) {
						s.quietButton(c, "install-retry", "重新规划", state.DownloadNetworkBlocked(s.Model) != "").OnClick(func() {
							s.Dispatch(state.Event{Kind: state.DownloadRequest, Enabled: true})
						})
					}
					if p.CanConfirm {
						s.accentButton(c, "install-start", "开始下载").OnClick(func() { s.Dispatch(state.Event{Kind: state.DownloadConfirm}) })
					} else {
						s.quietButton(c, "install-start", "开始下载", true)
					}
				})
			})
		})
	})
}

func installMetric(c *ui.Context, label string, bytes int64) {
	ui.Column(c).Grow(1).Basis(0).Gap(4).Children(func() {
		ui.Text(c, label).FontSize(downloadCaptionSize).TextColor(muted)
		ui.Text(c, installSize(bytes)).FontSize(downloadTitleSize).FontWeight(600).SingleLine()
	})
}

func installSize(bytes int64) string {
	switch {
	case bytes >= 1<<30:
		return fmt.Sprintf("%.1f GiB", float64(bytes)/(1<<30))
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(bytes)/(1<<20))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
