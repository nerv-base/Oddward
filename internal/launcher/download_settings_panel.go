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
	Files, Connections, Rate, Cache string
	AutoResume, PatchApply          bool
	Error                           string
}

func (s *Shell) openDownloadSettings() {
	p := s.Model.Download.Settings
	if p == (state.DownloadPreferences{}) {
		p = state.DefaultDownloadPreferences()
	}
	s.DownloadForm = DownloadForm{Files: strconv.Itoa(p.FilesInFlight), Connections: strconv.Itoa(p.ConnectionsPerFile), Rate: strconv.Itoa(p.RateLimitKiB), Cache: strconv.Itoa(p.CacheLimitGiB), AutoResume: p.AutoResume, PatchApply: p.PatchApply}
	s.openDownloads()
	s.DownloadSettingsOpen = true
}

func (s *Shell) downloadSettingsPanel(c *ui.Context) {
	form := &s.DownloadForm
	d := s.Model.Download
	ui.Scroll(c.Key("download-settings-content")).Grow(1).MinHeight(0).FillWidth().Scrollbars(true).Children(func() {
		ui.Column(c).FillWidth().Gap(downloadGap).Children(func() {
			ui.Text(c, "下载设置").FontSize(downloadTitleSize).FontWeight(600)
			input := func(key, label string, value *string) {
				caption := ui.Text(c, label).FontSize(downloadCaptionSize).TextColor(muted)
				ui.TextInput(c.Key(key), value).LabelledBy(caption).FillWidth().Height(downloadInputHeight).Padding(6, 10).Radius(8).Border(1, hairline).Background(ui.RGBA(255, 255, 255, .05)).TextColor(paper).FontSize(downloadBodySize).Disabled(d.SettingsSaving)
			}
			input("download-files", "并发文件数", &form.Files)
			input("download-connections", "每文件连接数", &form.Connections)
			input("download-rate", "限速（KiB/s，0 为不限）", &form.Rate)
			input("download-cache", "缓存上限（GiB，0 为不限）", &form.Cache)
			ui.Row(c).FillWidth().Height(downloadInputHeight).Children(func() {
				caption := ui.Text(c, "自动恢复").FontSize(downloadBodySize).Grow(1)
				s.formSwitch(c, "download-auto-resume", caption, &form.AutoResume, d.SettingsSaving)
			})
			ui.Row(c).FillWidth().Height(downloadInputHeight).Children(func() {
				caption := ui.Text(c, "差分更新").FontSize(downloadBodySize).Grow(1)
				s.formSwitch(c, "download-patch-apply", caption, &form.PatchApply, d.SettingsSaving)
			})
			ui.Text(c, "并发文件数 1–8，每文件连接数 1–16。差分不可用时会下载完整资源。").FontSize(downloadCaptionSize).TextColor(muted).LineHeight(1.5)
			message := form.Error
			if message == "" {
				message = d.SettingsError
			}
			if message != "" {
				ui.Text(c, message).FontSize(downloadCaptionSize).TextColor(s.tone()).LineHeight(1.5)
			} else if d.SettingsSaved {
				ui.Text(c, "下载设置已保存").FontSize(downloadCaptionSize).TextColor(s.tone())
			}
			s.accentButton(c, "download-save-settings", "保存下载设置").FillWidth().Height(downloadButtonHeight).Disabled(d.SettingsSaving).OnClick(func() {
				values := [4]int{}
				for i, text := range []string{form.Files, form.Connections, form.Rate, form.Cache} {
					value, err := strconv.Atoi(text)
					if err != nil {
						form.Error = "下载设置无效，请填写非负整数"
						return
					}
					values[i] = value
				}
				form.Error = ""
				s.Dispatch(state.Event{Kind: state.DownloadSettingsApply, DownloadSettings: state.DownloadPreferences{FilesInFlight: values[0], ConnectionsPerFile: values[1], RateLimitKiB: values[2], CacheLimitGiB: values[3], AutoResume: form.AutoResume, PatchApply: form.PatchApply}})
			})
			s.quietButton(c, "download-settings-back", "返回下载任务", false).FillWidth().Height(downloadButtonHeight).OnClick(func() { s.DownloadSettingsOpen = false })
		})
	})
}
