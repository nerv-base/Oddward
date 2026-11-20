// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"fmt"
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
		ui.Box(c).FillWidth().Height(1).Background(hairline).Margin(12, 0, 4, 0)
		s.downloadMaintenance(c)
	})
}

// downloadMaintenance keeps the rare transfer actions out of the home page.
// The home capsule and its preload chip only carry the current state; planning,
// preload checks and clean-up live here, where they cannot compete with play.
func (s *Shell) downloadMaintenance(c *ui.Context) {
	p := state.PresentDownload(s.Model)
	preload := state.PresentPreload(s.Model)
	d := s.Model.Download
	ui.Text(c, "更新与预下载").FontSize(15).FontWeight(500)
	ui.Text(c, p.StageText).FontSize(downloadBodySize).SingleLine()
	if p.PreloadOfferVersion != "" {
		ui.Text(c, "可预下载版本："+p.PreloadOfferVersion).FontSize(downloadCaptionSize).TextColor(s.tone())
	}
	if p.OfferVersion != "" {
		ui.Text(c, "可用正式版本："+p.OfferVersion).FontSize(downloadCaptionSize).TextColor(muted)
	}
	if p.NextAction != "" && p.NextAction != p.StageText {
		ui.Text(c, p.NextAction).FontSize(downloadCaptionSize).TextColor(muted).LineHeight(1.5)
	}
	// Budgets belong here: a scratch volume that cannot hold the transfer is
	// the one thing the home capsule can only say "no" about.
	if p.CacheRoot != "" {
		ui.Text(c, "下载暂存目录："+p.CacheRoot).FontSize(downloadCaptionSize).TextColor(muted).SingleLine()
	}
	if p.StorageNote != "" {
		ui.Text(c, p.StorageNote).FontSize(downloadCaptionSize).TextColor(muted).LineHeight(1.5)
	}
	if p.SpaceShort && p.Note != "" {
		ui.Text(c, p.Note).FontSize(downloadCaptionSize).TextColor(warning).LineHeight(1.5)
	}
	if p.AwaitingConfirmation || p.SpaceShort {
		ui.Text(c, fmt.Sprintf("%s最低所需：%.1f MB", p.BudgetVolume, float64(p.Needed)/(1<<20))).FontSize(downloadCaptionSize).TextColor(warning)
		ui.Text(c, fmt.Sprintf("%s可用空间：%.1f MB", p.BudgetVolume, float64(p.Free)/(1<<20))).FontSize(downloadCaptionSize).TextColor(muted)
	}
	action := func(key, label string, enabled bool, event state.EventKind) {
		s.quietButton(c, key, label, !enabled).FillWidth().Height(downloadButtonHeight).OnClick(func() {
			s.Dispatch(state.Event{Kind: event})
		})
	}
	if p.CanPause {
		action("settings-download-pause", "暂停下载", true, state.DownloadPause)
	}
	if p.CanResume {
		action("settings-download-resume", "继续下载", true, state.DownloadResume)
	}
	if p.CanCancel {
		s.quietButton(c, "settings-download-cancel", "取消任务", false).FillWidth().Height(downloadButtonHeight).OnClick(s.confirmDownloadCancel)
	}
	install := d.Kind == state.DownloadInstall && !d.Located && !d.Verified
	if s.Model.Managed.Root != "" && (d.Phase == state.DownloadIdle || d.Phase == state.DownloadDone) {
		label := "检查游戏更新"
		if install {
			label = "规划安装"
		}
		s.quietButton(c, "settings-download-plan", label, false).FillWidth().Height(downloadButtonHeight).OnClick(func() {
			s.Dispatch(state.Event{Kind: state.DownloadRequest, Enabled: install})
		})
	}
	if p.CanCheckPreload {
		action("settings-preload-check", "检查预下载", true, state.PreloadCheckRequest)
	}
	if preload.Visible && preload.Action == state.PreloadRequest && preload.Enabled {
		action("settings-preload-start", "开始预下载", true, state.PreloadRequest)
	}
	if p.CanApply {
		action("settings-preload-apply", "应用预下载", true, state.PreloadApplyRequest)
	}
	if p.CanReconcile {
		action("settings-preload-reconcile", "核对正式版本", true, state.PreloadReconcileRequest)
	}
	if p.CanDiscard {
		action("settings-preload-discard", "清理预下载", true, state.PreloadDiscardRequest)
	}
	if p.CanSelectCache {
		action("settings-cache-select", "选择下载暂存目录", true, state.DownloadCacheSelect)
	}
	if p.CanRecover {
		action("settings-download-recover", "完成上次更新", true, state.DownloadRecoverRequest)
	}
}
