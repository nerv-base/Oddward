// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package session

import (
	"context"
	"errors"

	"github.com/nerv-base/Oddward/internal/cnlogin/accounts"
	"github.com/nerv-base/Oddward/internal/cnlogin/cnproto"
	"github.com/nerv-base/Oddward/internal/cnlogin/laohu"
	"github.com/nerv-base/Oddward/internal/cnlogin/loginui"
	"github.com/nerv-base/Oddward/internal/cnlogin/sdkhost"
)

// loginState is the whole mutable state of one login attempt. Only the loop
// goroutine reads or writes it.
type loginState struct {
	cfg       laohu.Config
	key       laohu.AppKey
	client    *laohu.Client
	admission *laohu.Admission

	accounts []accounts.Account
	lastID   string
	ack      bool

	screen   string
	status   string
	busy     bool
	phone    string
	remember bool
	selected string

	// shownSMSLabel records the last projection, not another resend deadline.
	shownSMSLabel string

	preLogin  *laohu.PreLoginResult
	delivered bool

	// pendingQuery remembers a pre-login query that arrived while the identity
	// was still being handed to the game. The op 49 reply is sent as soon as
	// delivery completes.
	pendingQuery *cnproto.Query

	// action cancels the in-flight login when the window is closed, so a
	// cancelled attempt cannot deliver an identity to the game afterwards.
	action context.CancelFunc

	setupErr error
}

// registered decrypts the boot configuration and builds the login client once
// the game registered its application key.
func (s *Session) registered(event HostEvent) {
	s.endAction()
	s.st.busy = false
	if s.st.client != nil {
		s.st.client.Close()
		s.st.client = nil
	}
	s.st.cfg = laohu.Config{}
	s.st.key.Zero()
	cfg, err := s.deps.loadConfig(s.ctx, event.ConfigKey)
	if err != nil {
		s.st.setupErr = err
		if s.ui != nil {
			s.st.status = setupMessage(err)
			s.st.busy = true
			s.applyModel()
		}
		return
	}
	client, err := s.deps.newClient(s.ctx, cfg, event.Key, "")
	if err != nil {
		s.st.setupErr = err
		if s.ui != nil {
			s.st.status = setupMessage(err)
			s.st.busy = true
			s.applyModel()
		}
		return
	}
	admission, err := laohu.NewAdmission(client, s.deps.now)
	if err != nil {
		client.Close()
		s.st.setupErr = err
		return
	}
	s.st.cfg = cfg
	s.st.key.Zero()
	s.st.key = event.Key
	if s.st.client != nil {
		s.st.client.Close()
	}
	s.st.client = client
	s.st.admission = admission
	s.st.setupErr = nil
	s.st.ack = false
	if s.st.preLogin != nil {
		s.st.preLogin.Body = nil
	}
	s.st.preLogin = nil
	s.st.pendingQuery = nil
	s.st.delivered = false
	if acked, err := s.deps.accounts.NoticeAcknowledged(NoticeRevision); err == nil {
		s.st.ack = acked
	}
	s.reloadAccounts()
	if !s.st.ack {
		s.st.screen = "notice"
	} else if s.st.screen == "" {
		s.st.screen = s.initialScreen()
	}
	if s.ui != nil {
		s.applyModel()
	}
}

func (s *Session) initialScreen() string {
	if !s.st.ack {
		return "notice"
	}
	if len(s.st.accounts) > 0 {
		return "accounts"
	}
	return "phone"
}

func (s *Session) reloadAccounts() {
	list, _, err := s.deps.accounts.List()
	if err != nil {
		s.st.status = "无法读取已保存的账号。"
		return
	}
	s.st.accounts = list
	if last, err := s.deps.accounts.Last(); err == nil {
		s.st.lastID = last
	}
	if !hasAccount(list, s.st.selected) {
		s.st.selected = ""
		switch {
		case hasAccount(list, s.st.lastID):
			s.st.selected = s.st.lastID
		case len(list) > 0:
			s.st.selected = list[0].ID
		}
	}
}

func hasAccount(list []accounts.Account, id string) bool {
	for _, a := range list {
		if a.ID == id {
			return true
		}
	}
	return false
}

func (s *Session) applyModel() {
	if s.ui == nil {
		return
	}
	model := s.model()
	s.st.shownSMSLabel = model.SMSLabel
	_ = s.ui.Update(s.ctx, model)
}

// refreshSMS updates only the visible countdown and its expiry. Other screens
// may contain local edits, so they must not receive unsolicited snapshots.
func (s *Session) refreshSMS() {
	if s.ui != nil && s.st.screen == "phone" && s.model().SMSLabel != s.st.shownSMSLabel {
		s.applyModel()
	}
}

// handleUI applies one event from the authored login window.
func (s *Session) handleUI(event loginui.Event) {
	switch event.Kind {
	case "ready":
		// The window is visible; the snapshot it received is current.
	case "acknowledge":
		s.acknowledge()
	case "official":
		s.st.busy = true
		s.st.status = "请使用官方启动器完成登录。本窗口不会再向游戏发送登录信息。"
		s.applyModel()
	case "help":
		s.st.status = "遇到登录问题时，可以改用官方启动器登录。Oddward 不保存密码，也不修改游戏文件。"
		s.applyModel()
	case "cancel":
		s.closeUI()
	case "back":
		switch {
		case s.st.screen == "accounts":
			s.st.screen = "phone"
		case len(s.st.accounts) > 0:
			s.st.screen = "accounts"
		default:
			return
		}
		s.st.status = ""
		s.applyModel()
	case "other_account":
		s.st.screen = "phone"
		s.st.status = ""
		s.applyModel()
	case "select_account":
		if hasAccount(s.st.accounts, event.AccountID) {
			s.st.selected = event.AccountID
			s.applyModel()
		}
	case "rename_account":
		s.renameAccount(event)
	case "delete_account":
		s.deleteAccount(event)
	case "send_sms":
		s.sendSMS(event.Phone)
	case "login_phone":
		s.loginPhone(event)
	case "login_saved":
		s.loginSaved(event.AccountID)
	}
}

func (s *Session) acknowledge() {
	if err := s.deps.accounts.AcceptNotice(NoticeRevision); err != nil {
		s.st.status = "无法保存确认，请重试。"
		s.applyModel()
		return
	}
	s.st.ack = true
	s.st.status = ""
	s.st.screen = s.initialScreen()
	s.applyModel()
}

func (s *Session) sendSMS(phone string) {
	if s.st.busy {
		return
	}
	if s.st.client == nil {
		s.st.status = "登录服务尚未就绪，请稍后重试。"
		s.applyModel()
		return
	}
	if s.st.client.SMSCooldownRemaining() > 0 {
		s.st.status = "请稍后再发送验证码。"
		s.applyModel()
		return
	}
	client := s.st.client
	s.st.phone = phone
	s.st.busy = true
	s.st.status = ""
	s.applyModel()
	s.async(func(ctx context.Context) func() {
		err := client.SendSMS(ctx, phone)
		return func() {
			s.st.busy = false
			if err != nil {
				s.st.status = message(err)
			} else {
				s.st.status = "验证码已发送，请查收短信。"
			}
			s.applyModel()
		}
	})
}

func (s *Session) loginPhone(event loginui.Event) {
	if s.st.busy {
		return
	}
	if s.st.client == nil {
		s.st.status = "登录服务尚未就绪，请稍后重试。"
		s.applyModel()
		return
	}
	client := s.st.client
	admission := s.st.admission
	s.st.phone = event.Phone
	s.st.remember = event.Remember
	s.st.busy = true
	s.st.status = ""
	s.st.pendingQuery = nil
	s.applyModel()
	actionCtx := s.beginAction()
	s.asyncContext(actionCtx, func(ctx context.Context) func() {
		channel, err := client.LoginSMS(ctx, event.Phone, event.Captcha)
		if err != nil {
			return s.failed(err)
		}
		defer channel.Zero()
		var pending accounts.Pending
		if event.Remember {
			label, err := laohu.MaskPhone(event.Phone)
			if err != nil {
				return s.failed(err)
			}
			secret := accounts.Secret{DeviceID: client.DeviceID(), ChannelUID: string(channel.UID), ChannelToken: string(channel.Token)}
			pending, err = s.deps.accounts.Prepare(accounts.Account{Label: label}, secret)
			secret.Wipe()
			if err != nil {
				return s.failed(err)
			}
		}
		result, err := deliver(ctx, s.deps.host, admission, client, channel)
		if err != nil {
			if pending != nil {
				_ = pending.Rollback()
			}
			return s.failed(err)
		}
		saved := false
		if pending != nil {
			if err := pending.Commit(); err == nil {
				saved = true
				_ = s.deps.accounts.SetLast(pending.Account().ID)
			} else {
				_ = pending.Rollback()
			}
		}
		return s.delivered(result, saved)
	})
}

func (s *Session) loginSaved(id string) {
	if s.st.busy || id == "" {
		return
	}
	if s.st.client == nil {
		s.st.status = "登录服务尚未就绪，请稍后重试。"
		s.applyModel()
		return
	}
	s.st.selected = id
	s.st.busy = true
	s.st.status = ""
	s.st.pendingQuery = nil
	s.applyModel()
	cfg, key, admission := s.st.cfg, s.st.key, s.st.admission
	actionCtx := s.beginAction()
	s.asyncContext(actionCtx, func(ctx context.Context) func() {
		secret, err := s.deps.accounts.Unlock(id)
		if err != nil {
			return s.failed(err)
		}
		defer secret.Wipe()
		client, err := s.deps.newClient(ctx, cfg, key, secret.DeviceID)
		if err != nil {
			return s.failed(err)
		}
		defer client.Close()
		identity := laohu.ChannelIdentity{UID: []byte(secret.ChannelUID), Token: []byte(secret.ChannelToken)}
		defer identity.Zero()
		validation, err := client.Validate(ctx, identity)
		// The upstream client tolerates an isolated HTTP 400 with API code 400
		// from sdkValidate; the saved channel credential is still usable.
		tolerated := err != nil && validation.Status == laohu.RequestRejected && validation.HasAPICode && validation.APICode == 400
		if err != nil && !tolerated {
			if validation.Status == laohu.Unknown {
				return s.failed(err)
			}
			status, toPhone := validationMessage(validation.Status)
			return func() {
				s.st.busy = false
				s.st.status = status
				if toPhone {
					s.st.screen = "phone"
					s.st.selected = ""
				}
				s.applyModel()
			}
		}
		if validation.Status != laohu.Validated && !tolerated {
			status, toPhone := validationMessage(validation.Status)
			return func() {
				s.st.busy = false
				s.st.status = status
				if toPhone {
					s.st.screen = "phone"
					s.st.selected = ""
				}
				s.applyModel()
			}
		}
		result, err := deliver(ctx, s.deps.host, admission, client, identity)
		if err != nil {
			return s.failed(err)
		}
		_ = s.deps.accounts.SetLast(id)
		return s.delivered(result, false)
	})
}

func (s *Session) renameAccount(event loginui.Event) {
	if event.AccountID == "" {
		return
	}
	if err := s.deps.accounts.Rename(event.AccountID, event.Label); err != nil {
		s.st.status = message(err)
		s.applyModel()
		return
	}
	s.reloadAccounts()
	s.st.status = ""
	s.applyModel()
}

func (s *Session) deleteAccount(event loginui.Event) {
	if event.AccountID == "" {
		return
	}
	if err := s.deps.accounts.Remove(event.AccountID); err != nil {
		s.st.status = message(err)
		s.applyModel()
		return
	}
	if s.st.selected == event.AccountID {
		s.st.selected = ""
	}
	s.reloadAccounts()
	if len(s.st.accounts) == 0 {
		s.st.screen = "phone"
	}
	s.st.status = "已删除保存的账号。"
	s.applyModel()
}

// deliver runs the pre-login checks and only then hands the identity to the
// game. An identity that admission blocked never reaches the game.
func deliver(ctx context.Context, host Host, admission *laohu.Admission, client *laohu.Client, channel laohu.ChannelIdentity) (*laohu.PreLoginResult, error) {
	game, err := client.OneLogin(ctx, channel)
	if err != nil {
		return nil, err
	}
	defer game.Zero()
	preLogin, err := admission.Admit(ctx, game)
	if err != nil {
		return nil, err
	}
	result := new(laohu.PreLoginResult)
	*result = preLogin
	identity := cnproto.LoginIdentity{
		GameUID:      game.UID,
		GameToken:    game.Token,
		ChannelUID:   channel.UID,
		ChannelToken: channel.Token,
		Info:         game.ExtraOne,
	}
	defer identity.Zero()
	if err := host.SendLogin(ctx, identity); err != nil {
		return nil, err
	}
	return result, nil
}

// failed reports err in the login window and enables the controls again.
func (s *Session) failed(err error) func() {
	return func() {
		s.st.busy = false
		s.st.pendingQuery = nil
		s.st.status = message(err)
		s.applyModel()
	}
}

// delivered runs after the game acknowledged SendLogin and any account commit
// finished. Close the login window now; the game need not ask a pre-login query.
func (s *Session) delivered(result *laohu.PreLoginResult, saved bool) func() {
	return func() {
		s.st.preLogin = result
		s.st.delivered = true
		if pending := s.st.pendingQuery; pending != nil {
			query := *pending
			s.st.pendingQuery = nil
			s.replyQuery(query)
		}
		s.st.busy = true
		if saved {
			s.st.status = "登录成功，已保存此账号，正在进入游戏。"
		} else {
			s.st.status = "登录成功，正在进入游戏。"
		}
		s.reloadAccounts()
		s.closeUI()
	}
}

func validationMessage(status laohu.ValidationStatus) (string, bool) {
	switch status {
	case laohu.ServiceRejected:
		return "登录服务拒绝了此账号，请重新获取验证码。", true
	case laohu.RequiresOfficialInteraction:
		return "该账号需要在官方环境中完成验证，请改用官方启动器登录。", false
	case laohu.RequestRejected:
		return "登录请求被拒绝，请稍后重试。", false
	case laohu.NetworkFailure:
		return "网络异常，请稍后重试。", false
	default:
		return "保存的账号已失效，请用短信重新登录。", true
	}
}

// message turns an error into a short login-window status. It never includes
// response bodies, phone numbers or tokens.
func message(err error) string {
	var api *laohu.Error
	switch {
	case err == nil:
		return ""
	case errors.As(err, &api):
		return api.Message()
	case errors.Is(err, accounts.ErrTooManyAccounts):
		return "保存的账号已达上限，请先删除一个。"
	case errors.Is(err, accounts.ErrInvalidSecret), errors.Is(err, accounts.ErrCorrupt),
		errors.Is(err, accounts.ErrUnlock), errors.Is(err, accounts.ErrNotFound):
		return "保存的账号凭据不可用，请用短信重新登录。"
	case errors.Is(err, accounts.ErrInvalidLabel):
		return "账号名称不能为空或过长。"
	case errors.Is(err, accounts.ErrInvalidPhone):
		return "请输入有效的中国大陆手机号。"
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "登录已取消。"
	case errors.Is(err, sdkhost.ErrIdentity), errors.Is(err, sdkhost.ErrClosed):
		return "与游戏的连接已断开，请重启游戏。"
	default:
		return "登录失败，请重试。"
	}
}

func setupMessage(err error) string {
	if err == nil {
		return ""
	}
	return "登录组件初始化失败，请重启启动器后重试。"
}
