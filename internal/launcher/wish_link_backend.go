// SPDX-License-Identifier: GPL-3.0-or-later
package launcher

import (
	"context"
	"errors"
	"github.com/nerv-base/Oddward/internal/launcher/state"
)

// WishLinkBackend keeps all credentials behind the system boundary. Login and
// Roles stage a link in memory; Complete is the sole local commit point.
type WishLinkBackend interface {
	Status(context.Context) (WishLinkStatus, error)
	SendSMS(context.Context, string, string) error
	Login(context.Context, string, string, string) error
	LoginSaved(context.Context, string) error
	Roles(context.Context, string) ([]WishLinkRole, error)
	Bind(context.Context, string, string) error
	Complete(context.Context, string, string) error
	Cancel(context.Context, string) error
	Unlink(context.Context, string) error
	Fetch(context.Context, string, string) (WishImportReport, error)
}
type WishLinkStatus struct {
	AccountID, RoleID   string
	Linked, NeedsRelink bool
}
type WishLinkRole struct {
	ID, Name string
	Bound    bool
}

// WishLinkError exposes a fixed category, never a server response or credential.
type WishLinkError struct{ Kind state.WishLinkFailure }

func (e WishLinkError) Error() string { return wishLinkFailureText(e.Kind) }
func wishLinkErrorKind(err error) state.WishLinkFailure {
	var failure WishLinkError
	if errors.As(err, &failure) {
		return failure.Kind
	}
	return state.WishLinkStorage
}
func wishLinkFailureText(kind state.WishLinkFailure) string {
	switch kind {
	case state.WishLinkNetwork:
		return "网络连接失败，请稍后再试"
	case state.WishLinkAuth:
		return "塔吉多关联已失效，请重新关联"
	case state.WishLinkProtocol:
		return "塔吉多接口已变化，请更新 Oddward"
	case state.WishLinkChallenge:
		return "塔吉多要求人机验证，Oddward 不支持，请稍后再试或改用导入"
	case state.WishLinkMismatch:
		return "塔吉多登录的账号与已保存的游戏账号不一致"
	case state.WishLinkStorage:
		return "无法保存塔吉多关联，请重新关联"
	case state.WishLinkNoRole:
		return "塔吉多未绑定角色，请选择角色"
	case state.WishLinkInvalid:
		return "请检查手机号和验证码"
	case state.WishLinkRoleMismatch:
		return "当前档案与塔吉多绑定的角色不同，请先选择绑定的角色"
	case state.WishLinkGameCredential:
		return "无法使用已保存的游戏账号关联，请改用短信登录"
	}
	return ""
}
