// SPDX-License-Identifier: GPL-3.0-or-later

package state

func DefaultDownloadPreferences() DownloadPreferences { return DownloadPreferences{} }

func validDownloadPreferences(p DownloadPreferences) bool {
	return p.RateLimitKiB >= 0 && p.RateLimitKiB <= 1048576
}

func reduceDownloadSettings(m Model, e Event) (Model, []Effect, bool) {
	d := &m.Download
	switch e.Kind {
	case DownloadSettingsLoaded:
		if d.SettingsRequest != 0 || d.SettingsSaving {
			break
		}
		p := e.DownloadSettings
		if !validDownloadPreferences(p) {
			d.SettingsError = "下载限速无效，请检查后重新保存"
			break
		}
		d.Settings = p
	case DownloadSettingsApply:
		if d.SettingsSaving {
			break
		}
		if !validDownloadPreferences(e.DownloadSettings) {
			d.SettingsError = "下载限速无效，请检查后重新保存"
			d.SettingsSaved = false
			break
		}
		d.SettingsRequest++
		d.PendingSettings = e.DownloadSettings
		d.SettingsSaving, d.SettingsSaved = true, false
		d.SettingsError = ""
		return m, []Effect{{Kind: EffectSaveDownloads, Token: d.SettingsRequest}}, true
	case DownloadSettingsSaved, DownloadSettingsFailed:
		if e.Token == 0 || e.Token != d.SettingsRequest || !d.SettingsSaving {
			break
		}
		d.SettingsSaving = false
		if e.Kind == DownloadSettingsSaved {
			d.Settings = d.PendingSettings
			d.SettingsSaved = true
			d.SettingsError = ""
		} else {
			d.SettingsError = downloadErrorText(e.Message)
		}
	default:
		return m, nil, false
	}
	return m, nil, true
}
