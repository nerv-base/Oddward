// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package session

import (
	"fmt"
	"strconv"
	"time"

	"github.com/nerv-base/Oddward/internal/cnlogin/loginui"
)

// model renders the current login state for the authored WPF window.
func (s *Session) model() loginui.Model {
	m := loginui.Model{
		Screen:      s.st.screen,
		Status:      s.st.status,
		Busy:        s.st.busy,
		CanOfficial: true,
		Remember:    s.st.remember,
	}
	if s.deps.game != nil {
		identity := s.deps.game.Identity()
		m.GameProcessID = identity.PID
		m.GameCreationTime = strconv.FormatUint(identity.CreationTime, 10)
	}
	switch s.st.screen {
	case "notice":
		m.Title = "登录前请阅读"
		m.Subtitle = "国服账号登录说明"
		m.PrimaryLabel = "我已了解并同意继续"
		m.Notice = noticeSections()
	case "accounts":
		m.Title = "选择保存的账号"
		m.Subtitle = "国服账号"
		m.PrimaryLabel = "登录"
		m.CanBack = true
	default:
		m.Title = "手机号登录"
		m.Subtitle = "国服账号"
		m.PrimaryLabel = "登录"
		m.SMSLabel = "发送验证码"
		m.CanSendSMS = !s.st.busy && s.st.client != nil
		if s.st.client != nil {
			if remaining := s.st.client.SMSCooldownRemaining(); remaining > 0 {
				seconds := (remaining + time.Second - 1) / time.Second
				m.SMSLabel = fmt.Sprintf("%d 秒后重发", seconds)
				m.CanSendSMS = false
			}
		}
		m.CanBack = len(s.st.accounts) > 0
	}
	for _, account := range s.st.accounts {
		a := loginui.Account{ID: account.ID, Label: account.Label}
		// The authored window renders MaskedPhone as the secondary text; reuse
		// it for the link badge so the list still reads as one account.
		switch {
		case account.TajiduoState == "needs-relink":
			a.MaskedPhone = "已关联塔吉多（需重新关联）"
		case account.TajiduoLinked:
			a.MaskedPhone = "已关联塔吉多"
		}
		m.Accounts = append(m.Accounts, a)
	}
	m.SelectedAccountID = s.st.selected
	return m
}
