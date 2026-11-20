// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
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
