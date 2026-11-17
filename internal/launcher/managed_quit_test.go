// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

func TestNormalActiveQuitRemainsExplicitAndDoesNotClaimMock(t *testing.T) {
	for _, phase := range []state.ManagedPhase{state.ManagedPreparing, state.ManagedStarting, state.ManagedRunning, state.ManagedUnobserved} {
		m := normalFixture(phase)
		m.Flow = state.Ready
		next, _ := state.Reduce(m, state.Event{Kind: state.RequestQuit})
		if next.Closed || next.Overlay != state.ConfirmQuit {
			t.Fatalf("phase %v silently quit ordinary work", phase)
		}
		s := NewShell(next)
		tt := ui.NewTester(s.View, 1280, 720)
		if tt.HasText("后台演示操作正在进行。退出会取消当前演示任务。") {
			t.Fatal("ordinary close dialog claims fake work")
		}
		if !tt.HasText("退出只取消准备或停止观察，不终止已经创建的登录组件与游戏。") {
			t.Fatal("missing explicit non-termination boundary")
		}
		s.Dispose()
	}
}
