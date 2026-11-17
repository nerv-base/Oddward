// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func featureStateFor(m state.Model, _ state.Page) state.FeatureState { return m.CheckIn }

func TestFeatureMockManualLifecyclePreservesGame(t *testing.T) {
	for _, page := range []state.Page{state.CheckIn} {
		m := settled(state.Downloading)
		m.Page, m.Progress = page, .42
		for _, step := range []struct {
			kind state.EventKind
			want state.FeatureState
		}{
			{state.StartFeature, state.FeatureLoading},
			{state.FailFeature, state.FeatureFailed},
			{state.StartFeature, state.FeatureLoading},
			{state.CompleteFeature, state.FeatureReady},
			{state.ResetFeature, state.FeatureEmpty},
			{state.StartFeature, state.FeatureLoading},
			{state.ResetFeature, state.FeatureEmpty},
		} {
			next, fx := state.Reduce(m, state.Event{Kind: step.kind, Page: page})
			if got := featureStateFor(next, page); got != step.want {
				t.Fatalf("page %v event %v: got state %v, want %v", page, step.kind, got, step.want)
			}
			if next.Flow != m.Flow || next.Token != m.Token || next.Progress != m.Progress || next.Error != m.Error || len(fx) != 0 {
				t.Fatalf("feature event changed the game region or emitted effects: %+v %v", next, fx)
			}
			m = next
		}
	}
}

func TestFeatureMockRejectsInvalidAndHiddenActions(t *testing.T) {
	for _, page := range []state.Page{state.CheckIn} {
		for _, current := range []state.FeatureState{state.FeatureEmpty, state.FeatureLoading, state.FeatureReady, state.FeatureFailed} {
			for _, kind := range []state.EventKind{state.StartFeature, state.CompleteFeature, state.FailFeature, state.ResetFeature} {
				m := settled(state.Paused)
				m.Page, m.Progress = page, .71
				m.CheckIn = current
				for _, blocked := range []struct {
					name  string
					apply func(*state.Model, *state.Event)
				}{
					{"home", func(m *state.Model, e *state.Event) { m.Page = state.Home }},
					{"wrong page", func(m *state.Model, e *state.Event) { e.Page = state.Home }},
					{"other feature", func(m *state.Model, e *state.Event) {
						if e.Page == state.CheckIn {
							e.Page = state.WishAnalysis
						} else {
							e.Page = state.CheckIn
						}
					}},
					{"unknown page", func(m *state.Model, e *state.Event) { m.Page, e.Page = state.Page(255), state.Page(255) }},
					{"closed", func(m *state.Model, e *state.Event) { m.Closed = true }},
					{"settings", func(m *state.Model, e *state.Event) { m.Overlay = state.Settings }},
					{"scenarios", func(m *state.Model, e *state.Event) { m.Overlay = state.Scenarios }},
					{"about", func(m *state.Model, e *state.Event) { m.Overlay = state.About }},
					{"quit", func(m *state.Model, e *state.Event) { m.Overlay = state.ConfirmQuit }},
				} {
					before, e := m, state.Event{Kind: kind, Page: page}
					blocked.apply(&before, &e)
					next, handled := state.ReduceFeature(before, e)
					if !handled || next != before {
						t.Fatalf("%s must consume/reject event without mutation: %+v %+v", blocked.name, before, next)
					}
				}
			}
		}
	}
	m := settled(state.Ready)
	if next, handled := state.ReduceFeature(m, state.Event{Kind: state.Primary}); handled || next != m {
		t.Fatal("non-feature event must be left to the game reducer")
	}
}

func TestFeatureMockRejectsOutOfOrderTransitions(t *testing.T) {
	for _, page := range []state.Page{state.CheckIn} {
		for _, tc := range []struct {
			current state.FeatureState
			kind    state.EventKind
		}{
			{state.FeatureEmpty, state.CompleteFeature}, {state.FeatureEmpty, state.FailFeature},
			{state.FeatureReady, state.CompleteFeature}, {state.FeatureReady, state.FailFeature},
			{state.FeatureFailed, state.CompleteFeature}, {state.FeatureFailed, state.FailFeature},
			{state.FeatureLoading, state.StartFeature}, {state.FeatureReady, state.StartFeature},
			{state.FeatureState(255), state.StartFeature}, {state.FeatureState(255), state.CompleteFeature},
		} {
			m := settled(state.Ready)
			m.Page = page
			m.CheckIn = tc.current
			next, fx := state.Reduce(m, state.Event{Kind: tc.kind, Page: page})
			if next != m || len(fx) != 0 {
				t.Fatalf("page %v state %v must reject event %v", page, tc.current, tc.kind)
			}
		}
	}
}

func TestNativeFeatureMockManualPreview(t *testing.T) {
	for _, size := range [][2]int{{1024, 631}, {1280, 720}, {1600, 900}} {
		for _, tc := range []struct {
			page                state.Page
			nav, start, success string
		}{
			{state.CheckIn, "自动签到", "模拟签到", "示例签到结果"},
		} {
			s := NewShell(settled(state.Ready))
			t.Cleanup(s.Dispose)
			tt := ui.NewTester(s.View, size[0], size[1])
			if clickFailed(tt, tc.nav) || !tt.HasText("MOCK · 仅内存演示") || clickFailed(tt, tc.start) {
				t.Fatalf("missing explicit mock empty/start UI for %s at %v", tc.nav, size)
			}
			if featureStateFor(s.Model, tc.page) != state.FeatureLoading || !tt.HasText("模拟处理中") {
				t.Fatal("start must expose Loading instead of completing instantly")
			}
			tt.Advance(time.Hour)
			if featureStateFor(s.Model, tc.page) != state.FeatureLoading {
				t.Fatal("feature mock must wait for a manual outcome, not a timer")
			}
			for _, label := range []string{"模拟成功", "模拟失败", "重置预览", "返回游戏"} {
				node, ok := tt.AccessByRole(ui.RoleButton, label)
				if !ok || node.Bounds.H < 44 || node.Bounds.Y+node.Bounds.H > float32(size[1])-80 {
					t.Fatalf("mock outcome action must remain visible and reachable: %s %+v", label, node)
				}
			}
			if clickFailed(tt, "模拟失败") || featureStateFor(s.Model, tc.page) != state.FeatureFailed || !tt.HasText("模拟失败，未连接任何服务") {
				t.Fatal("manual failure must expose honest retryable state")
			}
			if clickFailed(tt, "重试模拟") || featureStateFor(s.Model, tc.page) != state.FeatureLoading || clickFailed(tt, "模拟成功") {
				t.Fatal("retry/success controls must complete the manual loop")
			}
			if featureStateFor(s.Model, tc.page) != state.FeatureReady || !tt.HasText(tc.success) || !tt.HasText("示例") {
				t.Fatal("ready must display explicitly marked example results")
			}
			if clickFailed(tt, "返回游戏") || clickFailed(tt, tc.nav) || featureStateFor(s.Model, tc.page) != state.FeatureReady {
				t.Fatal("navigation must preserve independent feature state")
			}
			if clickFailed(tt, "重置预览") || featureStateFor(s.Model, tc.page) != state.FeatureEmpty {
				t.Fatal("reset must return to the empty preview")
			}
			if urls := tt.OpenedURLs(); len(urls) != 0 {
				t.Fatalf("mock must not open an external service: %v", urls)
			}
		}
	}
}

func TestNativeCheckInMockPreservesStateAcrossWishNavigation(t *testing.T) {
	s := NewShell(settled(state.Downloading))
	t.Cleanup(s.Dispose)
	tt := ui.NewTester(s.View, 1024, 631)
	before := s.Model
	for _, label := range []string{"自动签到", "模拟签到", "模拟失败", "抽卡分析", "自动签到"} {
		if clickFailed(tt, label) {
			t.Fatalf("route lost action %s", label)
		}
	}
	if s.Model.CheckIn != state.FeatureFailed {
		t.Fatal("wish navigation erased check-in demo result")
	}
	if s.Model.Flow != before.Flow || s.Model.Token != before.Token || s.Model.Progress != before.Progress {
		t.Fatal("navigation changed running game operation")
	}
}

func TestNativeFeatureMockActionsStayInContentAtEveryState(t *testing.T) {
	actions := map[string]bool{"载入示例记录": true, "模拟签到": true, "模拟成功": true, "模拟失败": true, "重试模拟": true, "重置预览": true, "返回游戏": true}
	for _, size := range [][2]int{{1024, 631}, {1280, 720}, {1600, 900}} {
		for _, page := range []state.Page{state.CheckIn} {
			for _, current := range []state.FeatureState{state.FeatureEmpty, state.FeatureLoading, state.FeatureReady, state.FeatureFailed} {
				m := settled(state.Ready)
				m.Page, m.CheckIn = page, current
				s := NewShell(m)
				t.Cleanup(s.Dispose)
				tt := ui.NewTester(s.View, size[0], size[1])
				var found []ui.AccessInfo
				for _, node := range tt.AccessAll() {
					if node.Role != ui.RoleButton || !actions[node.Name] {
						continue
					}
					r := node.Bounds
					if r.W < 44 || r.H < 44 || r.X < railWidth+44 || r.Y < 218 || r.X+r.W > float32(size[0])-44 || r.Y+r.H > float32(size[1])-80 {
						t.Fatalf("page %v state %v at %v: mock action %s escaped content bounds: %+v", page, current, size, node.Name, r)
					}
					for _, other := range found {
						q := other.Bounds
						if r.X < q.X+q.W && q.X < r.X+r.W && r.Y < q.Y+q.H && q.Y < r.Y+r.H {
							t.Fatalf("mock actions overlap: %s / %s", node.Name, other.Name)
						}
					}
					found = append(found, node)
				}
				if len(found) < 2 {
					t.Fatalf("page %v state %v lost reset/return controls", page, current)
				}
			}
		}
	}
}

func TestNativeFeatureMockModalIsolationAndKeyboard(t *testing.T) {
	s := NewShell(settled(state.Ready))
	t.Cleanup(s.Dispose)
	tt := ui.NewTester(s.View, 1024, 631)
	if clickFailed(tt, "自动签到") || clickFailed(tt, "模拟签到") || clickFailed(tt, "设置") {
		t.Fatal("mock/settings route inaccessible")
	}
	if _, ok := tt.AccessByRole(ui.RoleButton, "模拟成功"); ok {
		t.Fatal("modal leaked background mock actions")
	}
	s.Dispatch(state.Event{Kind: state.CompleteFeature, Page: state.CheckIn})
	if s.Model.CheckIn != state.FeatureLoading {
		t.Fatal("modal must guard feature outcome events")
	}
	tt.Key(0, ui.KeyEscape)
	if clickFailed(tt, "模拟成功") || s.Model.CheckIn != state.FeatureReady {
		t.Fatal("outcome unavailable after modal dismissal")
	}
	if clickFailed(tt, "重置预览") {
		t.Fatal("reset action inaccessible")
	}
	tt.Key(0, ui.KeySpace)
	if s.Model.CheckIn != state.FeatureEmpty {
		t.Fatal("focused reset must remain keyboard-safe and idempotent")
	}
}
