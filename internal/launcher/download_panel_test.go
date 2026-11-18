// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"fmt"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func downloadFixture(phase state.DownloadPhase, stage state.DownloadStage) state.Model {
	m := normalFixture(state.ManagedReady)
	m.ReducedMotion = true
	m.Overlay = state.DownloadDetails
	m.Download = state.DownloadState{Phase: phase, Stage: stage, Kind: state.DownloadUpdate, Route: state.RoutePatch, Request: 9, Current: "1.0.0", Version: "1.1.0", Downloaded: 2 << 20, Total: 8 << 20, PatchBytes: 6 << 20, Reused: 20 << 20, FilesDone: 2, FilesTotal: 8, Located: true}
	return m
}

func TestDownloadPanelRendersSyntheticStates(t *testing.T) {
	for _, tc := range []struct {
		name  string
		phase state.DownloadPhase
		stage state.DownloadStage
		label string
	}{
		{"running", state.DownloadRunning, state.StageNetwork, "暂停下载"},
		{"paused", state.DownloadSuspended, state.StageNetwork, "继续下载"},
		{"failed", state.DownloadError, state.StageIdle, "重新规划"},
		{"recovery", state.DownloadRecoveryRequired, state.StageIdle, "完成上次更新"},
		{"planning", state.DownloadPreparing, state.StagePlanning, "正在获取版本信息 / 规划更新"},
		{"verifying", state.DownloadVerifying, state.StageVerify, "正在校验"},
		{"rebuilding", state.DownloadRebuilding, state.StageApply, "正在重建文件"},
		{"committing", state.DownloadCommitting, state.StageCommit, "正在提交更新"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := downloadFixture(tc.phase, tc.stage)
			m.Download.Error = "合成失败，请重试"
			s := NewShell(m)
			defer s.Dispose()
			tt := ui.NewTester(s.View, 1280, 720)
			panel, ok := tt.Find("游戏下载与更新")
			if !ok {
				t.Fatal("download dialog has no accessible title")
			}
			action, ok := tt.Find(tc.label)
			if !ok {
				t.Fatalf("missing state action/label %q", tc.label)
			}
			if action.X < panel.X || action.Y < panel.Y || action.X+action.W > panel.X+panel.W || action.Y+action.H > panel.Y+panel.H {
				t.Fatalf("%q lies outside dialog: %+v panel %+v", tc.label, action, panel)
			}
			snapshot := strings.Join(tt.Texts(), "\n")
			if _, ok := tt.AccessByRole(ui.RoleDialog, "游戏下载与更新"); !ok {
				t.Fatal("download panel is not an accessible dialog")
			}
			if tc.stage == state.StageNetwork && tc.phase == state.DownloadRunning {
				if !strings.Contains(snapshot, "计算中") {
					t.Fatal("unknown speed must remain unknown")
				}
				if strings.Contains(snapshot, "MB/s") || strings.Contains(snapshot, "0 B/s") {
					t.Fatal("unknown speed displayed as numeric speed")
				}
			}
		})
	}
}

func TestDownloadProgressUsesPosterTone(t *testing.T) {
	s := NewShell(downloadFixture(state.DownloadRunning, state.StageNetwork))
	defer s.Dispose()
	s.Accent = ui.Hex("#42A5D9")
	tt := ui.NewTester(s.View, 1280, 720)
	node, ok := tt.AccessByRole(ui.RoleProgress, "游戏下载进度")
	if !ok {
		t.Fatal("missing accessible download progress")
	}
	r := node.Bounds
	got := tt.Image().RGBAAt(int(r.X+r.W*.1), int(r.Y+r.H/2))
	tone := s.tone()
	if !sameColor(got, color.RGBA{tone.R, tone.G, tone.B, 255}, 3) {
		t.Fatalf("download meter ignores poster tone: got %+v want %+v", got, tone)
	}
}

func TestDownloadKnownRateKeepsUnknownETACalculating(t *testing.T) {
	m := downloadFixture(state.DownloadRunning, state.StageNetwork)
	m.Download.Rate = 3 << 20
	s := NewShell(m)
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1280, 720)
	text := strings.Join(tt.Texts(), "\n")
	if !strings.Contains(text, "3.0 MB/s") || !strings.Contains(text, "剩余时间：计算中") {
		t.Fatalf("reported speed and unknown ETA must be independent: %s", text)
	}
}

func TestDownloadPanelShowsRouteFallbackAndBudget(t *testing.T) {
	m := downloadFixture(state.DownloadPreparing, state.StagePlanning)
	m.Download.FallbackNote = true
	m.Download.Note = "patch-source-mismatch"
	m.Download.SpaceShort = true
	m.Download.Needed = 64 << 20
	m.Download.Free = 8 << 20
	m.Download.Suggested = 56 << 20
	s := NewShell(m)
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1100, 680)
	text := strings.Join(tt.Texts(), "\n")
	for _, want := range []string{"使用官方差分补丁", "部分文件改为完整下载", "检测到本地文件与官方补丁不匹配", "可用空间不足", "64.0 MB", "8.0 MB", "56.0 MB"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing %q in download panel", want)
		}
	}
	if strings.Contains(text, "patch-source-mismatch") {
		t.Fatal("internal note key leaked into UI")
	}
}

func TestDownloadSettingsExposeSixPreferencesAndRejectInvalidInput(t *testing.T) {
	m := normalFixture(state.ManagedReady)
	m.Download.Settings = state.DefaultDownloadPreferences()
	m.Overlay = state.Settings
	s := NewShell(m)
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1280, 720)
	if err := tt.Click("下载设置"); err != nil {
		t.Fatal(err)
	}
	for _, label := range []string{"并发文件数", "每文件连接数", "限速（KiB/s，0 为不限）", "缓存上限（GiB，0 为不限）"} {
		if _, ok := tt.AccessByRole(ui.RoleTextField, label); !ok {
			t.Errorf("missing accessible setting %q", label)
		}
	}
	for _, label := range []string{"自动恢复", "差分更新"} {
		if _, ok := tt.AccessByRole(ui.RoleSwitch, label); !ok {
			t.Errorf("missing accessible switch %q", label)
		}
	}
	if err := tt.Click("并发文件数"); err != nil {
		t.Fatal(err)
	}
	tt.Key(ui.Ctrl, ui.KeyA)
	tt.Type("0")
	if err := tt.Click("保存下载设置"); err != nil {
		t.Fatal(err)
	}
	if s.Model.Download.Settings != state.DefaultDownloadPreferences() {
		t.Fatal("invalid preferences replaced active settings")
	}
	if !strings.Contains(strings.Join(tt.Texts(), "\n"), "下载设置无效") {
		t.Fatalf("invalid input has no visible validation feedback: model=%+v form=%+v text=%v", s.Model.Download, s.DownloadForm, tt.Texts())
	}
}

func TestDownloadPanelShowsPassiveOfferWithoutStartingPlan(t *testing.T) {
	m := offerFixture()
	m, _ = state.Reduce(m, state.Event{Kind: state.DownloadOfferPollRequest})
	m, _ = state.Reduce(m, state.Event{Kind: state.DownloadOfferPolled, Token: m.Download.OfferRequest, Path: m.Managed.Root, Message: "2.0.0", Body: "3.0.0", Enabled: true})
	s := NewShell(m)
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1280, 720)
	text := strings.Join(tt.Texts(), "\n")
	for _, want := range []string{"可用正式版本：2.0.0", "可预下载版本：3.0.0"} {
		if !strings.Contains(text, want) {
			t.Errorf("missing passive version offer %q", want)
		}
	}
	if _, ok := tt.AccessByRole(ui.RoleButton, "检查预下载"); !ok {
		t.Fatal("idle offer has no explicit user discovery action")
	}
	if _, ok := tt.Find("确认并开始"); ok {
		t.Fatal("passive offer implicitly created a download plan")
	}
}

func TestDownloadPreloadPanelActions(t *testing.T) {
	for _, tc := range []struct {
		phase state.PreloadState
		label string
	}{
		{state.PreloadStateWaitingRelease, "核对正式版本"}, {state.PreloadStateStaged, "核对正式版本"}, {state.PreloadStateApplicable, "应用预下载"}, {state.PreloadStateStale, "清理预下载"},
	} {
		m := downloadFixture(state.DownloadIdle, state.StageIdle)
		m.Download.Kind = state.DownloadPreload
		m.Download.Preload = tc.phase
		s := NewShell(m)
		tt := ui.NewTester(s.View, 1280, 720)
		if _, ok := tt.AccessByRole(ui.RoleButton, tc.label); !ok {
			t.Errorf("preload phase %v missing accessible %q", tc.phase, tc.label)
		}
		s.Dispose()
	}
}

func TestRenderDownloadPreviewFrames(t *testing.T) {
	out := os.Getenv("ODDWARD_DOWNLOAD_PREVIEW_OUT")
	if out == "" {
		t.Skip("set ODDWARD_DOWNLOAD_PREVIEW_OUT for synthetic download previews")
	}
	if err := os.MkdirAll(out, 0755); err != nil {
		t.Fatal(err)
	}
	for i, phase := range []state.DownloadPhase{state.DownloadRunning, state.DownloadSuspended, state.DownloadError, state.DownloadRecoveryRequired, state.DownloadPreparing, state.DownloadVerifying, state.DownloadRebuilding, state.DownloadCommitting} {
		stage := []state.DownloadStage{state.StageNetwork, state.StageNetwork, state.StageIdle, state.StageIdle, state.StagePlanning, state.StageVerify, state.StageApply, state.StageCommit}[i]
		m := downloadFixture(phase, stage)
		m.Download.FallbackNote = true
		if phase == state.DownloadError || phase == state.DownloadRecoveryRequired {
			m.Download.Error = "合成测试：上次操作中断，请使用下方操作继续。"
		}
		s := NewShell(m)
		tt := ui.NewTester(s.View, 1280, 720)
		tt.Frame()
		f, err := os.Create(filepath.Join(out, fmt.Sprintf("download-%02d.png", i)))
		if err != nil {
			t.Fatal(err)
		}
		err = png.Encode(f, tt.Image())
		closeErr := f.Close()
		if closeErr != nil {
			t.Fatal(closeErr)
		}
		s.Dispose()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name                                        string
		preload                                     state.PreloadState
		settings, home, offer, scratch, sourceShort bool
	}{
		{name: "cross-volume", scratch: true}, {name: "cross-volume-source-short", scratch: true, sourceShort: true}, {name: "offer", offer: true}, {name: "settings", settings: true}, {name: "home", home: true}, {name: "preload-waiting", preload: state.PreloadStateWaitingRelease}, {name: "preload-staged", preload: state.PreloadStateStaged}, {name: "preload-applicable", preload: state.PreloadStateApplicable}, {name: "preload-stale", preload: state.PreloadStateStale},
	} {
		m := downloadFixture(state.DownloadIdle, state.StageIdle)
		m.Download.Settings = state.DefaultDownloadPreferences()
		if tc.offer {
			m.Download.Verified = true
			m, _ = state.Reduce(m, state.Event{Kind: state.DownloadOfferPollRequest})
			m, _ = state.Reduce(m, state.Event{Kind: state.DownloadOfferPolled, Token: m.Download.OfferRequest, Path: m.Managed.Root, Message: "2.0.0", Body: "3.0.0", Enabled: true})
		}
		if tc.scratch {
			m = cachePlanFixture(tc.sourceShort)
		}
		if tc.home {
			m.Overlay = state.NoOverlay
			m.Download.Phase = state.DownloadSuspended
			m.Download.Stage = state.StageNetwork
		}
		if tc.preload != state.PreloadStateUnavailable {
			m.Download.Kind = state.DownloadPreload
			m.Download.Preload = tc.preload
		}
		s := NewShell(m)
		if tc.settings {
			s.openDownloadSettings()
		}
		tt := ui.NewTester(s.View, 1280, 720)
		tt.Frame()
		f, err := os.Create(filepath.Join(out, "download-"+tc.name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		err = png.Encode(f, tt.Image())
		closeErr := f.Close()
		s.Dispose()
		if err != nil {
			t.Fatal(err)
		}
		if closeErr != nil {
			t.Fatal(closeErr)
		}
	}
}
