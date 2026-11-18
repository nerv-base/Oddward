// SPDX-License-Identifier: GPL-3.0-or-later
package launcher

import (
	"context"
	"errors"
	"time"

	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// CheckInBackend owns every Tajiduo check-in action. It resolves the saved
// account itself, so a stale page can never sign for another account. The
// game write and the community write are separate calls on purpose: one
// answer is never used for the other.
type CheckInBackend interface {
	// CheckInState reports the linked account, the persisted automatic
	// switches and the ledger's answer for today. refresh asks the server to
	// confirm today's state instead of trusting the ledger alone.
	CheckInState(ctx context.Context, refresh bool) (CheckInSnapshot, error)
	// Sign runs one daily task for the account the page last showed. The
	// ledger is read first: a recorded answer for today is reported as it is,
	// and an earlier attempt whose answer never arrived is only repeated when
	// confirm is set. A different current account is refused, so a stale page
	// cannot sign for someone else.
	Sign(ctx context.Context, accountID string, kind state.CheckInKind, confirm bool) (CheckInOutcome, error)
	// RoleStatus reads the role summary shown by the status panel.
	RoleStatus(ctx context.Context) (state.CheckInRole, error)
	// SetAuto persists one automatic switch for the account the page showed.
	SetAuto(ctx context.Context, accountID string, kind state.CheckInKind, enabled bool) error
}

// CheckInSnapshot is the account-level state the page starts from. Known says
// the sign path can ask the server before writing; Signed is the ledger's or
// the server's answer that today is already complete.
type CheckInSnapshot struct {
	AccountID   string
	RoleID      string
	Linked      bool
	NeedsRelink bool
	// Day is the local calendar day the ledger answers below belong to.
	Day string

	Auto    [state.CheckInKinds]bool
	Known   [state.CheckInKinds]bool
	Signed  [state.CheckInKinds]bool
	Result  [state.CheckInKinds]state.CheckInResult
	Detail  [state.CheckInKinds]string
	Failure [state.CheckInKinds]state.CheckInFailure
	At      [state.CheckInKinds]time.Time
	// Fail reports a recoverable refresh failure: the snapshot's ledger data
	// is still valid, only the server confirmation is missing.
	Fail state.CheckInFailure
}

// CheckInOutcome is one finished attempt. Result Unverified means the request
// may have reached the server; the caller must not retry it automatically.
type CheckInOutcome struct {
	Kind    state.CheckInKind
	Result  state.CheckInResult
	Detail  string
	Failure state.CheckInFailure
	At      time.Time
	Known   bool
	Signed  bool
}

// CheckInError exposes a fixed category, never a server response or a token.
type CheckInError struct{ Kind state.CheckInFailure }

func (e CheckInError) Error() string { return checkInFailureText(e.Kind) }

func checkInErrorKind(err error) state.CheckInFailure {
	var failure CheckInError
	if errors.As(err, &failure) {
		return failure.Kind
	}
	return state.CheckInFailureStorage
}

func checkInFailureText(kind state.CheckInFailure) string {
	switch kind {
	case state.CheckInFailureOffline:
		return "网络连接失败，请稍后再试"
	case state.CheckInFailureAuth:
		return "塔吉多关联已失效，请重新关联"
	case state.CheckInFailureChallenge:
		return "塔吉多要求人机验证，请先在官方客户端完成签到"
	case state.CheckInFailureProtocol:
		return "塔吉多接口已变化，请更新 Oddward"
	case state.CheckInFailureMismatch:
		return "塔吉多登录的账号与已保存的游戏账号不一致"
	case state.CheckInFailureNoRole:
		return "塔吉多未绑定角色，请先完成关联"
	case state.CheckInFailureStorage:
		return "无法读取或保存本地签到记录"
	case state.CheckInFailureBusy:
		return "另一个 Oddward 正在操作签到记录，请稍后再试"
	}
	return ""
}
