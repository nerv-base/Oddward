// SPDX-License-Identifier: GPL-3.0-or-later

package state

import "testing"

func TestDownloadSettingsPersistOnlyAfterSuccessfulTokenAck(t *testing.T) {
	m := downloadModel()
	old := DownloadPreferences{RateLimitKiB: 64}
	m, _ = Reduce(m, Event{Kind: DownloadSettingsLoaded, DownloadSettings: old})
	if m.Download.Settings != old {
		t.Fatal("persisted preferences not loaded")
	}
	candidate := DownloadPreferences{RateLimitKiB: 512}
	m, fx := Reduce(m, Event{Kind: DownloadSettingsApply, DownloadSettings: candidate})
	if m.Download.Settings != old || m.Download.PendingSettings != candidate || !m.Download.SettingsSaving || len(fx) != 1 || fx[0].Kind != EffectSaveDownloads || fx[0].Token != m.Download.SettingsRequest {
		t.Fatalf("candidate changed active settings before save: %+v %+v", m.Download, fx)
	}
	n, _ := Reduce(m, Event{Kind: DownloadSettingsSaved, Token: m.Download.SettingsRequest - 1})
	if n != m {
		t.Fatal("stale settings ack admitted")
	}
	m, _ = Reduce(m, Event{Kind: DownloadSettingsFailed, Token: m.Download.SettingsRequest, Message: "设置无法保存，请重试"})
	if m.Download.Settings != old || m.Download.SettingsSaving || m.Download.SettingsError == "" {
		t.Fatal("failed save replaced settings")
	}
	m, _ = Reduce(m, Event{Kind: DownloadSettingsApply, DownloadSettings: candidate})
	m, _ = Reduce(m, Event{Kind: DownloadSettingsSaved, Token: m.Download.SettingsRequest})
	if m.Download.Settings != candidate || m.Download.SettingsSaving || !m.Download.SettingsSaved {
		t.Fatal("successful save not published")
	}
}
func TestDownloadSettingsRejectInvalidCandidateWithoutEffect(t *testing.T) {
	for _, candidate := range []DownloadPreferences{
		{RateLimitKiB: -1},
		{RateLimitKiB: 1048577},
	} {
		m := downloadModel()
		m.Download.Settings = DownloadPreferences{RateLimitKiB: 64}
		n, fx := Reduce(m, Event{Kind: DownloadSettingsApply, DownloadSettings: candidate})
		if n.Download.Settings != m.Download.Settings || n.Download.SettingsError == "" || len(fx) != 0 {
			t.Fatalf("invalid settings admitted: %+v => %+v %+v", candidate, n.Download, fx)
		}
	}
}
func TestDownloadProjectionPreloadSeparateActions(t *testing.T) {
	m := downloadModel()
	if !PresentDownload(m).CanCheckPreload {
		t.Fatal("idle has no check action")
	}
	m.Download.Preload = PreloadStateWaitingRelease
	p := PresentDownload(m)
	if !p.CanReconcile || !p.CanDiscard || p.CanApply {
		t.Fatalf("waiting release actions: %+v", p)
	}
	m.Download.Preload = PreloadStateApplying
	m.Download.Phase = DownloadCommitting
	p = PresentDownload(m)
	if p.CanDiscard || p.CanReconcile || p.CanCheckPreload {
		t.Fatal("preload mutation can start another operation")
	}
}
