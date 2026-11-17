// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

type fakeUpdates struct {
	mu        sync.Mutex
	offer     UpdateOffer
	checks    int
	stages    int
	restarts  int
	skipped   []string
	stageErr  error
	restartOK bool
}

func (f *fakeUpdates) Check(context.Context) (UpdateOffer, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checks++
	return f.offer, nil
}

func (f *fakeUpdates) Stage(_ context.Context, progress func(done, total int64)) error {
	f.mu.Lock()
	f.stages++
	err, size := f.stageErr, f.offer.Size
	f.mu.Unlock()
	progress(size/2, size)
	progress(size, size)
	return err
}

func (f *fakeUpdates) Restart() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.restarts++
	if !f.restartOK {
		return errors.New("stub missing")
	}
	return nil
}

func (f *fakeUpdates) SaveSkipped(_ context.Context, v string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.skipped = append(f.skipped, v)
	return nil
}

func (f *fakeUpdates) count(n *int) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return *n
}

// waitFor renders frames until cond holds; worker results arrive between frames.
func waitFor(t *testing.T, tt *ui.Tester, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
		tt.Frame()
	}
}

func updatableShell(t *testing.T, f *fakeUpdates, w, h int) (*Shell, *ui.Tester) {
	m := settled(state.Ready)
	m.Update = state.UpdateState{Phase: state.SelfUpdateIdle, Current: "0.1.0"}
	s := NewShell(m)
	t.Cleanup(s.Dispose)
	s.UpdateBackend = f
	tt := ui.NewTester(s.View, w, h)
	tt.Frame()
	return s, tt
}

func TestUpdatePillWalksTheReleaseToARestart(t *testing.T) {
	f := &fakeUpdates{offer: UpdateOffer{Version: "0.2.0", Notes: "修复了若干问题", Size: 8 << 20, Newer: true}, restartOK: true}
	s, tt := updatableShell(t, f, 1280, 720)
	closed := false
	s.CloseWindow = func() { closed = true }
	waitFor(t, tt, "automatic check", func() bool { return s.Model.Update.Phase == state.SelfUpdateAvailable })
	if f.count(&f.checks) != 1 {
		t.Fatalf("start-up must check exactly once, got %d", f.checks)
	}
	pill, ok := tt.Find("新版本 v0.2.0")
	minimize, _ := tt.Find("最小化")
	if !ok || pill.X+pill.W > minimize.X || pill.Y != minimize.Y || pill.H != minimize.H {
		t.Fatalf("the pill must sit left of the caption buttons on their line: %+v vs %+v", pill, minimize)
	}
	if err := tt.Click("新版本 v0.2.0"); err != nil {
		t.Fatal(err)
	}
	if s.Model.Overlay != state.UpdateDetails || !tt.HasText("修复了若干问题") || !tt.HasText("v0.2.0") {
		t.Fatal("the pill must open the release notes")
	}
	if err := tt.Click("立即更新"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, "staged", func() bool { return s.Model.Update.Phase == state.SelfUpdateReady })
	if s.Model.Update.Downloaded != 8<<20 {
		t.Fatalf("progress must report real bytes: %+v", s.Model.Update)
	}
	if _, ok := tt.Find("重启以更新"); !ok {
		t.Fatal("pill must say the update waits for a restart")
	}
	if err := tt.Click("重启并更新"); err != nil {
		t.Fatal(err)
	}
	if f.count(&f.restarts) != 1 || !closed {
		t.Fatalf("restart must start the stub and close: restarts %d closed %v", f.restarts, closed)
	}
}

func TestUpdateRestartWaitsForTheGameAndReportsAFailedStub(t *testing.T) {
	f := &fakeUpdates{offer: UpdateOffer{Version: "0.2.0", Size: 1 << 20, Newer: true}}
	s, tt := updatableShell(t, f, 1280, 720)
	waitFor(t, tt, "check", func() bool { return s.Model.Update.Phase == state.SelfUpdateAvailable })
	s.Dispatch(state.Event{Kind: state.UpdateDownload})
	waitFor(t, tt, "staged", func() bool { return s.Model.Update.Phase == state.SelfUpdateReady })
	s.Model.Flow = state.Running
	s.Dispatch(state.Event{Kind: state.OpenOverlay, Overlay: state.UpdateDetails})
	tt.Frame()
	if !tt.HasText("游戏或登录组件仍在运行，结束后再重启更新。") {
		t.Fatal("blocked restart must say why")
	}
	_ = tt.Click("重启并更新")
	if f.count(&f.restarts) != 0 {
		t.Fatal("restart must wait while the game runs")
	}
	s.Model.Flow = state.Ready
	tt.Frame()
	if err := tt.Click("重启并更新"); err != nil {
		t.Fatal(err)
	}
	if f.count(&f.restarts) != 1 || s.Model.Closed || !tt.HasText("无法启动新版本：stub missing") {
		t.Fatalf("a failed stub must keep the launcher open with the reason: %+v", s.Model.Update)
	}
}

func TestSkippingAVersionPersistsAndHidesThePill(t *testing.T) {
	f := &fakeUpdates{offer: UpdateOffer{Version: "0.2.0", Size: 1 << 20, Newer: true}}
	s, tt := updatableShell(t, f, 1280, 720)
	waitFor(t, tt, "check", func() bool { return s.Model.Update.Phase == state.SelfUpdateAvailable })
	_ = tt.Click("新版本 v0.2.0")
	if err := tt.Click("跳过此版本"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, "skip saved", func() bool { f.mu.Lock(); defer f.mu.Unlock(); return len(f.skipped) == 1 })
	if f.skipped[0] != "0.2.0" || s.Model.Overlay != state.NoOverlay {
		t.Fatalf("skip: %v overlay %v", f.skipped, s.Model.Overlay)
	}
	if _, ok := tt.Find("新版本 v0.2.0"); ok {
		t.Fatal("a skipped version must not keep nagging")
	}
}

func TestSettingsShowsTheVersionAndChecksOnRequest(t *testing.T) {
	f := &fakeUpdates{offer: UpdateOffer{Version: "0.1.0"}}
	s, tt := updatableShell(t, f, 1280, 720)
	waitFor(t, tt, "auto check", func() bool { return s.Model.Update.Phase == state.SelfUpdateCurrent })
	if clickFailed(tt, "设置") {
		t.Fatal("settings unreachable")
	}
	if !tt.HasText("v0.1.0") || !tt.HasText("已是最新版本") {
		t.Fatal("settings must show the running version and the last result")
	}
	if err := tt.Click("检查更新"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, "manual check", func() bool { return f.count(&f.checks) == 2 && s.Model.Update.Phase == state.SelfUpdateCurrent })
}

func TestDevelopmentBuildsNeverCheck(t *testing.T) {
	s := NewShell(settled(state.Ready))
	t.Cleanup(s.Dispose)
	tt := ui.NewTester(s.View, 1280, 720)
	if clickFailed(tt, "设置") {
		t.Fatal("settings unreachable")
	}
	if !tt.HasText("开发构建") {
		t.Fatal("unversioned builds must say so")
	}
	field, ok := tt.AccessByRole(ui.RoleButton, "检查更新")
	if !ok || !field.Disabled {
		t.Fatal("a development build has nothing to check")
	}
}

type fakeNetwork struct {
	mu    sync.Mutex
	saved []state.NetworkState
	err   error
}

func (f *fakeNetwork) Apply(_ context.Context, n state.NetworkState) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.saved = append(f.saved, n)
	return nil
}

func TestNetworkPanelEditsAFormAndSavesOnce(t *testing.T) {
	m := settled(state.Ready)
	m.Network = state.NetworkState{MirrorPrefix: "https://gh-proxy.com/"}
	s := NewShell(m)
	t.Cleanup(s.Dispose)
	f := &fakeNetwork{}
	s.NetworkBackend = f
	tt := ui.NewTester(s.View, 1024, 631)
	if clickFailed(tt, "设置") || clickFailed(tt, "网络") {
		t.Fatal("network settings unreachable")
	}
	if s.Model.Overlay != state.NetworkSettings || !tt.HasText("跟随系统") {
		t.Fatal("network panel missing")
	}
	if err := tt.Click("自定义"); err != nil {
		t.Fatal(err)
	}
	if err := tt.Click("代理地址"); err != nil {
		t.Fatal(err)
	}
	tt.Type("socks5://127.0.0.1:1080")
	if err := tt.Click("GitHub 加速"); err != nil {
		t.Fatal(err)
	}
	if s.Model.Network.Proxy != state.ProxySystem || s.Model.Network.Mirror {
		t.Fatal("editing must not touch the saved preference")
	}
	save, ok := tt.Find("保存")
	panel, _ := tt.AccessByRole(ui.RoleDialog, "网络")
	if !ok || save.Y+save.H > panel.Bounds.Y+panel.Bounds.H {
		t.Fatalf("save must stay reachable at the minimum window: %+v in %+v", save, panel.Bounds)
	}
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, "saved", func() bool { return s.Model.Network.Saved })
	want := state.NetworkState{Proxy: state.ProxyCustom, ProxyURL: "socks5://127.0.0.1:1080", Mirror: true, MirrorPrefix: "https://gh-proxy.com/"}
	got := f.saved[0]
	if len(f.saved) != 1 || got.Proxy != want.Proxy || got.ProxyURL != want.ProxyURL || got.Mirror != want.Mirror || got.MirrorPrefix != want.MirrorPrefix {
		t.Fatalf("saved %+v", f.saved)
	}
	if !tt.HasText("已保存") {
		t.Fatal("save must confirm")
	}
}

func TestNetworkPanelShowsWhyASaveFailed(t *testing.T) {
	s := NewShell(settled(state.Ready))
	t.Cleanup(s.Dispose)
	s.NetworkBackend = &fakeNetwork{err: errors.New("代理地址必须是 http:// 或 socks5://")}
	tt := ui.NewTester(s.View, 1280, 720)
	s.openNetworkSettings()
	tt.Frame()
	if err := tt.Click("保存"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, tt, "failure", func() bool { return s.Model.Network.Error != "" })
	if !tt.HasText("代理地址必须是 http:// 或 socks5://") {
		t.Fatal("the reason must be shown")
	}
}

func TestSettingsStillFitWithTheVersionRowAtTheMinimumWindow(t *testing.T) {
	s := NewShell(settled(state.Ready))
	t.Cleanup(s.Dispose)
	tt := ui.NewTester(s.View, 1024, 631)
	if clickFailed(tt, "设置") {
		t.Fatal("settings unreachable")
	}
	tt.Advance(time.Second)
	tt.Frame()
	panel, _ := tt.AccessByRole(ui.RoleDialog, "设置")
	for _, label := range []string{"检查更新", "状态预览", "网络", "关于"} {
		r, ok := tt.Find(label)
		if !ok || r.Y+r.H > panel.Bounds.Y+panel.Bounds.H-20 {
			t.Fatalf("%s clipped: %+v in %+v", label, r, panel.Bounds)
		}
	}
}
