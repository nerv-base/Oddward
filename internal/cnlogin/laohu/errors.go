// SPDX-License-Identifier: GPL-3.0-or-later
package laohu

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode"
)

// Phase is a stable local error category. It never contains response text.
type Phase string

const (
	PhaseNotInitialized                Phase = "not-initialized"
	PhaseInvalidMainlandPhone          Phase = "invalid-mainland-phone"
	PhaseInvalidCaptcha                Phase = "invalid-captcha"
	PhaseSMSCooldown                   Phase = "sms-cooldown"
	PhaseServerRejected                Phase = "server-rejected"
	PhaseInvalidResponse               Phase = "invalid-response"
	PhaseRequestFailed                 Phase = "request-failed"
	PhaseCancelled                     Phase = "cancelled"
	PhaseHTTPFailure                   Phase = "http-failure"
	PhasePhoneLoginUnavailable         Phase = "server-phone-login-unavailable"
	PhasePasswordStateUnavailable      Phase = "password-state-unavailable"
	PhaseInvalidPasswordState          Phase = "invalid-password-state"
	PhaseChannelLoginRejected          Phase = "channel-login-rejected"
	PhaseGameLoginRejected             Phase = "game-login-rejected"
	PhaseGameKeyUnavailable            Phase = "game-key-unavailable"
	PhaseChannelIdentityUnavailable    Phase = "channel-identity-unavailable"
	PhaseSessionSaveUnavailable        Phase = "session-save-unavailable"
	PhaseSessionSaveFailed             Phase = "session-save-failed"
	PhaseValidationRejected            Phase = "channel-validation-rejected"
	PhaseValidationNetworkFailure      Phase = "channel-validation-network-failure"
	PhaseValidationOfficialInteraction Phase = "channel-validation-official-interaction"
	PhaseValidationRequestRejected     Phase = "channel-validation-request-rejected"
	PhaseValidationUnknownResponse     Phase = "channel-validation-unknown-response"
	PhasePreLoginServiceRejected       Phase = "prelogin-service-rejected"
	PhaseRealNameRequired              Phase = "real-name-required"
	PhaseRealNameRestricted            Phase = "real-name-restricted"
	PhasePreLoginStateUnavailable      Phase = "prelogin-state-unavailable"
)

var phaseMessages = map[Phase]string{
	PhaseNotInitialized: "请先初始化登录环境。", PhaseInvalidMainlandPhone: "请输入有效的中国大陆手机号。", PhaseInvalidCaptcha: "请输入 4 至 8 位短信验证码。", PhaseSMSCooldown: "发送过于频繁，请稍后重试。",
	PhaseServerRejected: "服务端拒绝了此次请求。", PhaseInvalidResponse: "服务端返回了无法识别的响应。", PhaseRequestFailed: "登录请求失败，请稍后重试。", PhaseCancelled: "登录已取消。", PhaseHTTPFailure: "登录服务请求未成功。", PhasePhoneLoginUnavailable: "服务端暂不支持手机号登录。",
	PhasePasswordStateUnavailable: "无法获取账号的登录方式。", PhaseInvalidPasswordState: "服务端返回的登录方式无效。", PhaseChannelLoginRejected: "渠道账号登录未通过。", PhaseGameLoginRejected: "未能取得本次游戏身份。", PhaseGameKeyUnavailable: "游戏登录密钥不可用。", PhaseChannelIdentityUnavailable: "渠道身份不可用，请重新登录。", PhaseSessionSaveUnavailable: "无法保存此账号。", PhaseSessionSaveFailed: "账号保存失败。",
	PhaseValidationRejected: "保存的渠道凭据已失效，请使用短信重新登录。", PhaseValidationNetworkFailure: "校验渠道凭据时网络连接失败。", PhaseValidationOfficialInteraction: "该账号需要通过官方渠道完成交互后再登录。", PhaseValidationRequestRejected: "渠道凭据校验请求未成功。", PhaseValidationUnknownResponse: "无法确认保存的渠道凭据状态。",
	PhasePreLoginServiceRejected:  "服务端未通过登录前检查，本次登录已阻止。请稍后重试或使用官方启动器处理。",
	PhaseRealNameRequired:         "该账号尚未完成实名认证，本次登录已阻止。请先通过官方渠道完成实名认证后再登录。",
	PhaseRealNameRestricted:       "该账号的实名认证或防沉迷状态限制登录，本次登录已阻止。请通过官方渠道查看限制原因。",
	PhasePreLoginStateUnavailable: "无法确认该账号的实名及防沉迷状态，本次登录已阻止。请稍后重试或使用官方启动器处理。",
}

// Error deliberately does not wrap transport errors: those may embed a phone
// number in a URL or a complete response. Message is for transient local UI only.
type Error struct {
	Phase       Phase
	StatusCode  int
	APICode     int
	HasAPICode  bool
	explanation string
}

func (e *Error) Error() string {
	p := e.Phase
	if _, ok := phaseMessages[p]; !ok {
		p = PhaseRequestFailed
	}
	return "laohu: " + string(p)
}
func (e *Error) String() string                { return e.Error() }
func (e *Error) GoString() string              { return e.Error() }
func (e *Error) Format(f fmt.State, verb rune) { io.WriteString(f, e.Error()) }
func (e *Error) Message() string {
	s, ok := phaseMessages[e.Phase]
	if !ok {
		s = phaseMessages[PhaseRequestFailed]
	}
	if e.Phase == PhaseHTTPFailure {
		if text := SafeExplanation(e.explanation); text != "" {
			s += " " + text
		}
	}
	return s
}

var explanationSecrets = regexp.MustCompile(`https?://\S+|\S+@\S+|[0-9]{4,}|[A-Za-z0-9_+/=\-]{24,}`)

// SafeExplanation removes common secret forms from a short display-only hint.
// Callers must never persist this text or use it as an error/log message.
func SafeExplanation(s string) string {
	if strings.TrimSpace(s) == "" || utf16Len(s) > 256 {
		return ""
	}
	s = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.Cc, r) || unicode.Is(unicode.Cf, r) {
			return ' '
		}
		return r
	}, s)
	return explanationSecrets.ReplaceAllString(s, "[omitted]")
}
