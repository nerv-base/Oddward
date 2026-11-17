// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package session

import "github.com/nerv-base/Oddward/internal/cnlogin/loginui"

// NoticeRevision identifies the authored login notice. Bump it whenever the
// text below changes so every user has to acknowledge the new text.
const NoticeRevision = 1

// noticeSections is the privacy notice shown before the first CN login. It
// names what is sent to the login service and what is stored locally; nothing
// is pre-selected or accepted implicitly.
func noticeSections() []loginui.Section {
	return []loginui.Section{
		{
			Title: "这是什么",
			Body:  "本窗口用于向《异环》国服登录服务提交手机号验证码或已保存的账号，登录成功后把游戏身份交给游戏进程。Oddward 是第三方启动器，与游戏发行商没有隶属关系。",
		},
		{
			Title: "会发送什么",
			Body:  "手机号、短信验证码、设备标识和启动器版本会发送到游戏官方的登录接口。是否接收以及如何使用这些数据由官方服务决定。",
		},
		{
			Title: "本地保存",
			Body:  "只有勾选“在此设备保存账号”时才会保存账号：保存的是设备标识和官方返回的登录令牌，使用当前 Windows 用户的 DPAPI 加密。不保存手机号明文、短信验证码或密码。",
		},
		{
			Title: "不会做什么",
			Body:  "Oddward 不收集遥测，不把登录信息发送给官方接口以外的任何服务器，也不向游戏进程注入代码。",
		},
		{
			Title: "游戏文件",
			Body:  "只读取安装目录中的版本文件、登录组件和登录配置，用来选择匹配的登录方式。不会修改或替换游戏文件。",
		},
		{
			Title: "实名与防沉迷",
			Body:  "官方服务要求实名认证或处于防沉迷限制时，登录会被阻止并显示原因。Oddward 不能也不应绕过这些限制。",
		},
		{
			Title: "删除账号",
			Body:  "可以在账号列表中随时重命名或删除保存的账号。删除后需要重新使用短信验证码登录。",
		},
		{
			Title: "封号风险",
			Body:  "自实现的登录流程可能违反游戏服务条款，或触发风控与反作弊检测，导致登录受限、账号封禁，甚至永久封号。无法保证账号安全或持续兼容；此前成功登录也不代表没有风险。请在理解并愿意承担这些风险后继续。",
		},
	}
}
