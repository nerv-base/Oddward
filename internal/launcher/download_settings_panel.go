// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"strconv"

	"github.com/egoist/mygo/ui"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// DownloadForm holds only unsaved text edits. Validation and active preferences
// remain in the reducer; the backend acknowledges persistence before activation.
type DownloadForm struct {
	Rate  string
	Error string
}

func (s *Shell) resetDownloadForm() {
	p := s.Model.Download.Settings
	s.DownloadForm = DownloadForm{Rate: strconv.Itoa(p.RateLimitKiB)}
}

func (s *Shell) openDownloadSettings() {
	s.Dispatch(state.Event{Kind: state.Navigate, Page: state.LauncherSettings})
	s.Dispatch(state.Event{Kind: state.SelectLauncherSettingsSection, Index: uint8(state.SettingsNetwork)})
}

// downloadSection is the launcher's transfer and network page: the rate limit
// applies to game downloads, the proxy to every request Oddward sends.
func (s *Shell) downloadSection(c *ui.Context) {
	content := ui.Column(c.Key("download-settings-content")).FillWidth().Gap(downloadGap)
	ready := ui.Local(content, "download-form-ready", func() bool { return false })
	if !*ready {
		s.resetDownloadForm()
		*ready = true
	}
	content.Children(func() {
		form, d := &s.DownloadForm, s.Model.Download
		ui.Text(c, "游戏下载").FontSize(15).FontWeight(500)
		caption := ui.Text(c, "限速（KiB/s，0 为不限）").FontSize(downloadCaptionSize).TextColor(muted)
		ui.TextInput(c.Key("download-rate"), &form.Rate).LabelledBy(caption).FillWidth().Height(downloadInputHeight).
			Padding(6, 10).Radius(8).Border(1, hairline).Background(ui.RGBA(255, 255, 255, .05)).
			TextColor(paper).FontSize(downloadBodySize).Disabled(d.SettingsSaving)
		ui.Text(c, "自动恢复和差分更新始终开启；差分不可用时会下载完整资源。").FontSize(downloadCaptionSize).TextColor(muted).LineHeight(1.5)
		message := form.Error
		if message == "" {
			message = d.SettingsError
		}
		if message != "" {
			ui.Text(c, message).FontSize(downloadCaptionSize).TextColor(warning).LineHeight(1.5)
		} else if d.SettingsSaved {
			ui.Text(c, "下载设置已保存").FontSize(downloadCaptionSize).TextColor(s.tone())
		}
		s.quietButton(c, "download-save-settings", "保存限速", false).Height(downloadButtonHeight).
			Disabled(d.SettingsSaving).OnClick(func() {
			rate, err := strconv.Atoi(form.Rate)
			if err != nil || rate < 0 {
				form.Error = "限速请填写非负整数"
				return
			}
			form.Error = ""
			s.Dispatch(state.Event{Kind: state.DownloadSettingsApply, DownloadSettings: state.DownloadPreferences{RateLimitKiB: rate}})
		})
		ui.Box(c).FillWidth().Height(1).Background(hairline).Margin(12, 0, 4, 0)
		s.networkPanel(c)
	})
}
