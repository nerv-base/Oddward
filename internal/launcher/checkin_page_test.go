// SPDX-License-Identifier: GPL-3.0-or-later

package launcher

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// loadCheckInPage walks the same path the shell uses: one state request, then
// its answer. Tests never inject a snapshot with a guessed token.
func loadCheckInPage(t *testing.T, s *Shell, payload state.CheckInPayload) {
	t.Helper()
	s.Dispatch(state.Event{Kind: state.CheckInStateRequested})
	token := s.Model.CheckIn.LoadToken
	s.Dispatch(state.Event{Kind: state.CheckInStateLoaded, Token: token, Check: payload})
	if !s.Model.CheckIn.Loaded {
		t.Fatal("state load was dropped")
	}
}

func linkedCheckInPayload(accountID string) state.CheckInPayload {
	return state.CheckInPayload{
		AccountID: accountID, RoleID: "100012345", Linked: true,
		Known: [state.CheckInKinds]bool{true, true},
	}
}

func TestCheckInPageUnlinkedAccountOffersNoWrite(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1280, 720)
	if clickFailed(tt, "自动签到") {
		t.Fatal("check-in page unreachable")
	}
	loadCheckInPage(t, s, state.CheckInPayload{AccountID: "preview-account"})
	tt.Frame()
	if _, ok := tt.Find("去关联塔吉多"); !ok {
		t.Fatal("an unlinked account must point at the link flow")
	}
	if !tt.HasText("未关联塔吉多") {
		t.Fatal("the page must say the account is not linked")
	}
	if !tt.HasText("关联塔吉多后才能签到") {
		t.Fatal("the page must explain why the write is unavailable")
	}
	// The action stays on screen but a click can never start a write.
	_ = tt.Click("签到")
	if s.Model.CheckIn.Busy[state.CheckGame] || s.Model.CheckIn.Phase[state.CheckGame] != state.CheckInUnlinked {
		t.Fatal("an unlinked account must never start a sign-in write")
	}
	if tt.HasText("本性像素") {
		t.Fatal("there is no game data to show before a link exists")
	}
}

func TestCheckInPageShowsOnlyServerNumbers(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1280, 720)
	if clickFailed(tt, "自动签到") {
		t.Fatal("check-in page unreachable")
	}
	payload := linkedCheckInPayload("preview-account")
	loadCheckInPage(t, s, payload)
	s.Dispatch(state.Event{Kind: state.CheckInStatusRequested})
	s.Dispatch(state.Event{Kind: state.CheckInStatusLoaded, Token: s.Model.CheckIn.StatusToken, Check: state.CheckInPayload{
		Role: state.CheckInRole{
			RoleID: "100012345", Name: "合成测试角色", Server: "天空之城",
			Level: 61, WorldLevel: 12,
			Stamina: 187, StaminaMax: 240,
			CityStamina: 92, CityStaminaMax: 120,
			DayValue: 100, WeekRemain: 2,
			LoginDays: 128, CharCount: 24, Achievement: 512, AchievementTotal: 900,
			FetchedAt: time.Date(2026, 11, 17, 15, 27, 0, 0, time.Local),
		},
	}})
	tt.Frame()
	for _, want := range []string{"本性像素", "187", "240", "都市活力", "92", "120", "今日活跃度", "周本剩余", "合成测试角色"} {
		if !tt.HasText(want) {
			t.Fatalf("status panel must show %q from the server payload", want)
		}
	}
	if !tt.HasText("没有体力恢复时间") {
		t.Fatal("the panel must say which data the server does not provide")
	}
	if tt.HasText("MOCK · 仅内存演示") {
		t.Fatal("the real page must never claim to be a mock")
	}
}

func TestCheckInPageAccessibleLabelsAcrossPreferences(t *testing.T) {
	for _, prefs := range []ui.Preferences{{}, {HighContrast: true}, {ReduceTransparency: true}} {
		s := NewShell(settled(state.Ready))
		tt := ui.NewTester(s.View, 1280, 720)
		tt.SetPreferences(prefs)
		if clickFailed(tt, "自动签到") {
			t.Fatal("check-in page unreachable")
		}
		loadCheckInPage(t, s, linkedCheckInPayload("preview-account"))
		tt.Frame()
		for _, label := range []string{"游戏签到", "社区签到", "签到", "刷新", "刷新状态", "游戏信息"} {
			if !tt.HasText(label) {
				t.Fatalf("prefs %+v: missing %q", prefs, label)
			}
		}
		for _, role := range []struct {
			role ui.Role
			name string
		}{
			{ui.RoleButton, "签到"},
			{ui.RoleButton, "刷新"},
			{ui.RoleSwitch, "游戏签到 · 自动"},
			{ui.RoleSwitch, "社区签到 · 自动"},
		} {
			if _, ok := tt.AccessByRole(role.role, role.name); !ok {
				t.Fatalf("prefs %+v: role %d %q must be reachable by role and name", prefs, role.role, role.name)
			}
		}
		s.Dispose()
	}
}

func TestCheckInPageUnknownResultNeverLooksFinished(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1280, 720)
	if clickFailed(tt, "自动签到") {
		t.Fatal("check-in page unreachable")
	}
	payload := linkedCheckInPayload("preview-account")
	payload.Result[state.CheckCommunity] = state.CheckResultUnverified
	payload.Detail[state.CheckCommunity] = "服务器称已签到，但状态未确认"
	loadCheckInPage(t, s, payload)
	tt.Frame()
	if !tt.HasText("待核实") {
		t.Fatal("an unverified answer must be labelled as unverified")
	}
	if !tt.HasText("不会自动重试") {
		t.Fatal("the page must promise no automatic retry after a lost answer")
	}
	if _, ok := tt.Find("今日已签到"); ok {
		t.Fatal("an unverified answer must not read as done")
	}
}

func TestCheckInPageShowsPartialSuccess(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	tt := ui.NewTester(s.View, 1280, 720)
	if clickFailed(tt, "自动签到") {
		t.Fatal("check-in page unreachable")
	}
	payload := linkedCheckInPayload("preview-account")
	payload.Result[state.CheckGame] = state.CheckResultSuccess
	payload.Signed[state.CheckGame] = true
	payload.Detail[state.CheckGame] = "本性像素 ×60"
	payload.At[state.CheckGame] = time.Date(2026, 11, 17, 15, 28, 0, 0, time.Local)
	payload.Result[state.CheckCommunity] = state.CheckResultFailed
	payload.Failure[state.CheckCommunity] = state.CheckInFailureOffline
	payload.Detail[state.CheckCommunity] = "网络连接失败，请稍后再试"
	loadCheckInPage(t, s, payload)
	tt.Frame()
	if !tt.HasText("已完成") || !tt.HasText("失败") {
		t.Fatal("one finished task and one failed task must both be visible")
	}
	if !tt.HasText("本性像素 ×60") {
		t.Fatal("the game reward text must be shown")
	}
	if !tt.HasText("网络连接失败") {
		t.Fatal("the community failure must be shown next to the game success")
	}
}

// checkInFake is a backend double: it records calls and can hold an answer so
// a test can prove the UI thread never waits for it.
type checkInFake struct {
	mu       sync.Mutex
	loads    []bool
	signs    []state.CheckInKind
	accounts []string
	autos    [][3]any
	role     state.CheckInRole
	snapshot CheckInSnapshot
	outcome  CheckInOutcome
	err      error
	gate     chan struct{}
}

func (f *checkInFake) CheckInState(_ context.Context, refresh bool) (CheckInSnapshot, error) {
	f.mu.Lock()
	f.loads = append(f.loads, refresh)
	f.mu.Unlock()
	return f.snapshot, f.err
}

func (f *checkInFake) Sign(_ context.Context, accountID string, kind state.CheckInKind) (CheckInOutcome, error) {
	f.mu.Lock()
	f.signs = append(f.signs, kind)
	f.accounts = append(f.accounts, accountID)
	gate := f.gate
	f.mu.Unlock()
	if gate != nil {
		<-gate
	}
	return f.outcome, f.err
}

func (f *checkInFake) RoleStatus(context.Context) (state.CheckInRole, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.role, f.err
}

func (f *checkInFake) SetAuto(_ context.Context, accountID string, kind state.CheckInKind, enabled bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.autos = append(f.autos, [3]any{accountID, kind, enabled})
	return f.err
}

func (f *checkInFake) counts() (loads, signs int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.loads), len(f.signs)
}

func (f *checkInFake) signedWith() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.accounts...)
}

// waitFor runs the UI thread until cond holds or the deadline passes. The shell
// posts background answers to a channel and wakes the window, so a test must
// draw frames instead of sleeping.
func waitCheckIn(t *testing.T, tt *ui.Tester, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		tt.Advance(20 * time.Millisecond)
		tt.Frame()
		if cond() {
			return
		}
	}
	t.Fatal("condition never became true")
}

func TestCheckInShellLoadsOnEntryAndSignsWithoutBlockingUI(t *testing.T) {
	s := NewShell(settled(state.Ready))
	defer s.Dispose()
	fake := &checkInFake{
		snapshot: CheckInSnapshot{AccountID: "preview-account", RoleID: "10001", Linked: true, Known: [state.CheckInKinds]bool{true, true}},
		outcome:  CheckInOutcome{Kind: state.CheckGame, Result: state.CheckResultSuccess, Detail: "本性像素 ×60", Known: true, Signed: true},
	}
	s.CheckInBackend = fake
	tt := ui.NewTester(s.View, 1280, 720)
	if clickFailed(tt, "自动签到") {
		t.Fatal("check-in page unreachable")
	}
	waitCheckIn(t, tt, func() bool {
		loads, _ := fake.counts()
		return loads >= 2 && s.Model.CheckIn.Loaded
	})
	if s.Model.CheckIn.AccountID != "preview-account" || !s.Model.CheckIn.Linked {
		t.Fatal("the entry load must fill the page from the backend")
	}
	gate := make(chan struct{})
	fake.mu.Lock()
	fake.gate = gate
	fake.mu.Unlock()
	if clickFailed(tt, "签到") {
		t.Fatal("a linked account must offer the manual write")
	}
	// The write is blocked in the backend; the UI thread still finishes frames.
	tt.Frame()
	if _, signs := fake.counts(); signs != 1 {
		t.Fatalf("expected exactly one write, got %d", signs)
	}
	if got := fake.signedWith(); len(got) != 1 || got[0] != "preview-account" {
		t.Fatalf("the write must carry the page's account, got %v", got)
	}
	close(gate)
	waitCheckIn(t, tt, func() bool { return s.Model.CheckIn.Result[state.CheckGame] == state.CheckResultSuccess })
	if !s.Model.CheckIn.Signed[state.CheckGame] {
		t.Fatal("a success answer must mark the task done")
	}
	if phases := s.Model.CheckIn.Phase[state.CheckCommunity]; phases != state.CheckInReady {
		t.Fatalf("the other task must stay untouched, got %v", phases)
	}
}
