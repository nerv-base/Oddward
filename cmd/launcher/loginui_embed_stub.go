// SPDX-License-Identifier: GPL-3.0-or-later

//go:build !loginui_embed

package main

import "errors"

func embeddedLoginUI() (string, error) {
	return "", errors.New("此构建未包含 WPF 登录窗口。请移除 -login-ui=wpf 改用内置窗口，或使用 -EmbedWPF 构建的版本")
}
