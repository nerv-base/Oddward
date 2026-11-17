// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func TestOfficialNewsOpenReadsOnlyItsOwnAnnouncement(t *testing.T) {
	for item := uint8(0); item <= 2; item++ {
		m := settled(state.Ready)
		m.NewsTab, m.NewsIndex = 2, 1
		n, fx := state.Reduce(m, state.Event{Kind: state.OpenOfficial, Index: item})
		if n.Overlay != state.NewsDetails || n.DetailItem != item {
			t.Fatalf("news must open the reading panel: model=%+v", n)
		}
		if len(fx) != 1 || fx[0].Kind != state.FetchDetail || fx[0].Token != n.DetailRequest {
			t.Fatalf("opening must read that announcement and nothing else: %+v", fx)
		}
		if n.Flow != m.Flow || n.Token != m.Token || n.Media != m.Media || n.NewsIndex != 1 || n.NewsTab != 2 {
			t.Fatal("opening news changed another region")
		}
	}
}

func TestMockNewsSelectionIsBoundedAndModalGuarded(t *testing.T) {
	m := settled(state.Downloading)
	m.NewsBanners.Count = 3
	m.NewsIndex = 2
	m, fx := state.Reduce(m, state.Event{Kind: state.SelectNewsTab, Index: 1})
	if m.NewsTab != 1 || m.NewsIndex != 2 || len(fx) != 0 {
		t.Fatalf("category must preserve independent official carousel without effects: %+v %v", m, fx)
	}
	m, _ = state.Reduce(m, state.Event{Kind: state.SelectNewsStory, Index: 2})
	if m.NewsIndex != 2 || m.NewsTab != 1 || m.Flow != state.Downloading {
		t.Fatal("story selection crossed region or failed")
	}
	for _, event := range []state.Event{{Kind: state.SelectNewsTab, Index: 3}, {Kind: state.SelectNewsStory, Index: 255}, {Kind: state.OpenOfficial, Index: 3}} {
		n, effects := state.Reduce(m, event)
		if n != m || len(effects) != 0 {
			t.Fatalf("unbounded news event accepted: %+v", event)
		}
	}
	for _, overlay := range []state.Overlay{state.Settings, state.Scenarios, state.About, state.ConfirmQuit, state.NewsDetails} {
		blocked := m
		blocked.Overlay = overlay
		for _, event := range []state.Event{{Kind: state.SelectNewsTab, Index: 2}, {Kind: state.SelectNewsStory, Index: 1}, {Kind: state.OpenOfficial, Index: 1}} {
			n, effects := state.Reduce(blocked, event)
			if n != blocked || len(effects) != 0 {
				t.Fatalf("news leaked through overlay %v", overlay)
			}
		}
	}
	for _, page := range []state.Page{state.WishAnalysis, state.CheckIn} {
		blocked := m
		blocked.Page = page
		for _, event := range []state.Event{{Kind: state.SelectNewsTab, Index: 2}, {Kind: state.SelectNewsStory, Index: 1}, {Kind: state.OpenOfficial}} {
			n, fx := state.Reduce(blocked, event)
			if n != blocked || len(fx) != 0 {
				t.Fatal("hidden home news accepted input")
			}
		}
	}
	if n, _ := state.Reduce(m, state.Event{Kind: state.OpenOverlay, Overlay: state.NewsDetails}); n != m {
		t.Fatal("generic overlay event bypassed article selection")
	}
}

func TestMockDemoClockIsIndependentAndSurvivesAllScenarioSelections(t *testing.T) {
	m := settled(state.Downloading)
	m.Overlay = state.Scenarios
	m = send(m, state.ToggleDemoClock)
	if !m.DemoPaused || m.Flow != state.Downloading || m.Token != 1 || m.Overlay != state.Scenarios {
		t.Fatalf("clock toggle must freeze independently: %+v", m)
	}
	for flow := state.Checking; flow <= state.Failed; flow++ {
		m.Overlay = state.Scenarios
		n, fx := state.Reduce(m, state.Event{Kind: state.SelectScenario, Flow: flow})
		if n.Flow != flow || !n.DemoPaused || n.Overlay != state.NoOverlay || n.Page != state.Home || n.Token <= m.Token {
			t.Fatalf("cannot inspect frozen state %v: %+v", flow, n)
		}
		if len(fx) == 0 || fx[0].Kind != state.CancelOperation {
			t.Fatal("scenario must fence previous work")
		}
		if state.Active(flow) && (len(fx) != 2 || fx[1].Kind != state.Simulate || fx[1].Token != n.Token) {
			t.Fatal("frozen scenario must retain its declared resumable simulation")
		}
		m = n
	}
	m.Overlay = state.Scenarios
	m = send(m, state.ToggleDemoClock)
	if m.DemoPaused {
		t.Fatal("clock toggle cannot resume")
	}
}

func TestMockStepCompletesOnlyCurrentActiveStageInsideScenarios(t *testing.T) {
	for _, tc := range []struct{ from, to state.Flow }{{state.Checking, state.Ready}, {state.Downloading, state.Verifying}, {state.Verifying, state.Ready}, {state.Launching, state.Running}} {
		m := settled(tc.from)
		m.Overlay, m.DemoPaused, m.Progress = state.Scenarios, true, .42
		n, fx := state.Reduce(m, state.Event{Kind: state.StepDemo})
		if n.Flow != tc.to || n.Token <= m.Token || !n.DemoPaused || n.Overlay != state.Scenarios || len(fx) == 0 {
			t.Fatalf("one stage %v -> %v failed: %+v %v", tc.from, tc.to, n, fx)
		}
		stale, effects := state.Reduce(n, state.Event{Kind: state.Completed, Token: m.Token})
		if stale != n || len(effects) != 0 {
			t.Fatal("step failed to fence completed stage")
		}
	}
	for _, flow := range []state.Flow{state.NotInstalled, state.Ready, state.UpdateAvailable, state.Paused, state.Running, state.Failed} {
		m := settled(flow)
		m.Overlay = state.Scenarios
		if n, fx := state.Reduce(m, state.Event{Kind: state.StepDemo}); n != m || len(fx) != 0 {
			t.Fatalf("step invented work for idle state %v", flow)
		}
	}
}

func TestMockFailureUsesCurrentTokenAndExplicitDemoMessage(t *testing.T) {
	for _, flow := range []state.Flow{state.Checking, state.Downloading, state.Verifying, state.Launching} {
		m := settled(flow)
		m.Overlay, m.DemoPaused = state.Scenarios, true
		n, fx := state.Reduce(m, state.Event{Kind: state.FailDemo, Message: "https://not-used.invalid"})
		if n.Flow != state.Failed || n.Token <= m.Token || n.Overlay != state.Scenarios || !n.DemoPaused || !strings.Contains(n.Error, "演示") || strings.Contains(n.Error, "https://") {
			t.Fatalf("failure must be controlled mock-only: %+v", n)
		}
		if len(fx) != 1 || fx[0].Kind != state.CancelOperation {
			t.Fatalf("failure must only cancel in-memory operation: %+v", fx)
		}
	}
}

func TestMockScenarioControlsCannotLeakThroughOtherOverlays(t *testing.T) {
	for _, overlay := range []state.Overlay{state.NoOverlay, state.Settings, state.About, state.ConfirmQuit, state.NewsDetails} {
		m := settled(state.Downloading)
		m.Overlay = overlay
		for _, event := range []state.Event{{Kind: state.ToggleDemoClock}, {Kind: state.StepDemo}, {Kind: state.FailDemo}, {Kind: state.SelectMediaState, Media: state.MediaUnavailable}, {Kind: state.ResetMediaPreview}} {
			n, fx := state.Reduce(m, event)
			if n != m || len(fx) != 0 {
				t.Fatalf("scenario control leaked through overlay %v: event=%v", overlay, event.Kind)
			}
		}
	}
	m := settled(state.Checking)
	m.Closed, m.Overlay = true, state.Scenarios
	for _, kind := range []state.EventKind{state.ToggleDemoClock, state.StepDemo, state.FailDemo, state.SelectMediaState, state.ResetMediaPreview, state.SelectNewsTab, state.SelectNewsStory, state.OpenOfficial} {
		if n, fx := state.Reduce(m, state.Event{Kind: kind}); n != m || len(fx) != 0 {
			t.Fatal("closed model accepted mock interaction")
		}
	}
}

func TestMockMediaPreviewShowsFourStatesWithoutChangingGame(t *testing.T) {
	for media := state.MediaLoading; media <= state.MediaUnavailable; media++ {
		m := settled(state.Downloading)
		m.Overlay, m.Progress = state.Scenarios, .42
		n, fx := state.Reduce(m, state.Event{Kind: state.SelectMediaState, Media: media})
		if n.Media != media || !n.MediaPreview || n.Flow != m.Flow || n.Token != m.Token || n.Progress != m.Progress || len(fx) != 0 {
			t.Fatalf("media state %v not independently previewed: %+v %v", media, n, fx)
		}
		if n.AnimateBackground() != (media == state.MediaPlaying) {
			t.Fatalf("media state %v projects wrong playback", media)
		}
		for _, kind := range []state.EventKind{state.MediaReady, state.MediaFailed} {
			if actual, _ := state.Reduce(n, state.Event{Kind: kind}); actual != n {
				t.Fatal("real local decoder callback overwrote explicit media preview")
			}
		}
		n, _ = state.Reduce(n, state.Event{Kind: state.ResetMediaPreview})
		if n.MediaPreview || n.Media != state.MediaLoading || n.Flow != m.Flow {
			t.Fatal("media preview cannot return to real local source observation")
		}
	}
	m := settled(state.Ready)
	m.Overlay = state.Scenarios
	if n, fx := state.Reduce(m, state.Event{Kind: state.SelectMediaState, Media: state.Media(255)}); n != m || len(fx) != 0 {
		t.Fatal("unknown media preview state accepted")
	}
}

func TestMockScenarioPanelExposesClockControls(t *testing.T) {
	s := NewShell(settled(state.Downloading))
	t.Cleanup(s.Dispose)
	s.Model.Overlay = state.Scenarios
	s.Model.ReducedMotion = true
	tt := ui.NewTester(s.View, 1024, 631)
	if !tt.HasText("冻结演示时钟") {
		t.Fatal("preview must offer a frozen clock for short-lived states")
	}
	for _, label := range []string{"单步完成", "模拟失败", "媒体加载", "媒体播放", "媒体暂停", "媒体不可用"} {
		if _, ok := tt.AccessByRole(ui.RoleButton, label); !ok {
			t.Fatalf("missing in-memory scenario action %s", label)
		}
	}
}

func TestMockNativeScenarioControlsFitMinimumWindow(t *testing.T) {
	s := NewShell(settled(state.Ready))
	t.Cleanup(s.Dispose)
	s.Model.Overlay, s.Model.ReducedMotion = state.Scenarios, true
	tt := ui.NewTester(s.View, 1024, 631)
	for _, label := range []string{"准备就绪", "尚未安装", "有可用更新", "正在下载", "下载已暂停", "正在校验", "正在启动", "游戏运行中", "连接失败", "重新检查", "媒体加载", "媒体播放", "媒体暂停", "媒体不可用", "退出媒体预览"} {
		node, ok := tt.AccessByRole(ui.RoleButton, label)
		if !ok || node.Bounds.X < 604 || node.Bounds.Y < 0 || node.Bounds.X+node.Bounds.W > 1024 || node.Bounds.Y+node.Bounds.H > 600 || node.Bounds.H < 28 {
			t.Fatalf("all states must be readable and reachable without clipping at minimum size: %s %+v", label, node)
		}
	}
}

func TestMockNativeFrozenStageCanBeInspectedAndStepped(t *testing.T) {
	s := NewShell(settled(state.Ready))
	t.Cleanup(s.Dispose)
	s.Model.ReducedMotion = true
	s.Dispatch(state.Event{Kind: state.OpenOverlay, Overlay: state.Scenarios})
	tt := ui.NewTester(s.View, 1024, 631)
	if clickFailed(tt, "冻结演示时钟") || !s.Model.DemoPaused || clickFailed(tt, "重新检查") {
		t.Fatal("cannot select a frozen transient state")
	}
	tt.Advance(30 * time.Second)
	if s.Model.Flow != state.Checking || !s.Model.DemoPaused {
		t.Fatal("frozen clock advanced the short-lived checking state")
	}
	s.Dispatch(state.Event{Kind: state.OpenOverlay, Overlay: state.Scenarios})
	tt.Frame()
	if clickFailed(tt, "单步完成") || s.Model.Flow != state.Ready || s.Model.Overlay != state.Scenarios {
		t.Fatal("single step must complete exactly one ordinary stage inside preview")
	}
	if clickFailed(tt, "正在下载") || !s.Model.DemoPaused || s.Model.Overlay != state.NoOverlay {
		t.Fatal("scenario selection lost clock state")
	}
	s.Dispatch(state.Event{Kind: state.OpenOverlay, Overlay: state.Scenarios})
	tt.Frame()
	if clickFailed(tt, "模拟失败") || s.Model.Flow != state.Failed || !strings.Contains(s.Model.Error, "演示") {
		t.Fatal("mock failure did not use the ordinary reducer transition")
	}
}

func TestMockNativeMediaPreviewButtonsDispatchIndependentState(t *testing.T) {
	s := NewShell(settled(state.Ready))
	t.Cleanup(s.Dispose)
	s.Model.Overlay, s.Model.ReducedMotion = state.Scenarios, true
	tt := ui.NewTester(s.View, 1024, 631)
	for _, item := range []struct {
		label string
		media state.Media
	}{{"媒体加载", state.MediaLoading}, {"媒体播放", state.MediaPlaying}, {"媒体暂停", state.MediaPaused}, {"媒体不可用", state.MediaUnavailable}} {
		if clickFailed(tt, item.label) || s.Model.Media != item.media || !s.Model.MediaPreview || s.Model.Flow != state.Ready {
			t.Fatalf("native media preview did not dispatch %s", item.label)
		}
	}
	if clickFailed(tt, "退出媒体预览") || s.Model.MediaPreview {
		t.Fatal("cannot leave mock media observation")
	}
}

func TestMockNativeF8DoesNotDismissUnrelatedModals(t *testing.T) {
	for _, overlay := range []state.Overlay{state.Settings, state.About, state.ConfirmQuit, state.NewsDetails} {
		m := settled(state.Ready)
		m.Overlay, m.ReducedMotion = overlay, true
		s := NewShell(m)
		tt := ui.NewTester(s.View, 1024, 631)
		tt.Key(0, ui.KeyF8)
		if s.Model.Overlay != overlay || s.Model.Flow != state.Ready {
			s.Dispose()
			t.Fatalf("F8 leaked from preview into unrelated modal %v: %+v", overlay, s.Model)
		}
		tt.Key(0, ui.KeyEscape)
		if s.Model.Overlay != state.NoOverlay {
			s.Dispose()
			t.Fatalf("Escape no longer dismisses modal %v", overlay)
		}
		s.Dispose()
	}
}

func TestMockNativeSettingsMarksMediaPreview(t *testing.T) {
	m := settled(state.Ready)
	m.Media, m.MediaPreview, m.Overlay, m.ReducedMotion = state.MediaPlaying, true, state.Settings, true
	s := NewShell(m)
	t.Cleanup(s.Dispose)
	tt := ui.NewTester(s.View, 1024, 631)
	if !tt.HasText("MOCK · 媒体状态预览") {
		t.Fatal("settings must distinguish selected mock media state from a real local decoder")
	}
}

func TestNativeNewsDetailsIsModalAndReturnsLocally(t *testing.T) {
	for tab := uint8(0); tab < 3; tab++ {
		m := settled(state.Ready)
		m.NewsTab, m.NewsIndex = tab, 2
		s := NewShell(m)
		s.Model.ReducedMotion = true
		s.Dispatch(state.Event{Kind: state.OpenOfficial, Index: 2})
		tt := ui.NewTester(s.View, 1024, 631)
		if !tt.HasText("OFFICIAL · 官网公告") || !tt.HasText("正文来自官网公开公告页面，仅用于本窗口阅读；不会打开外部网站。") {
			s.Dispose()
			t.Fatal("the panel must say where the text comes from and that it stays in this window")
		}
		if _, ok := tt.AccessByRole(ui.RoleButton, "启动游戏"); ok {
			s.Dispose()
			t.Fatal("news modal leaked game action")
		}
		back, ok := tt.AccessByRole(ui.RoleButton, "返回主页")
		if !ok || back.Bounds.Y < 0 || back.Bounds.Y+back.Bounds.H > 600 {
			s.Dispose()
			t.Fatalf("local article must keep its return action reachable at minimum height: %+v", back)
		}
		if clickFailed(tt, "返回主页") || s.Model.Overlay != state.NoOverlay || s.Model.NewsTab != tab || s.Model.NewsIndex != 2 {
			s.Dispose()
			t.Fatal("closing news reset selection or left the local app")
		}
		s.Dispose()
	}
}
