// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package session

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nerv-base/Oddward/internal/cnlogin/accounts"
	"github.com/nerv-base/Oddward/internal/cnlogin/laohu"
	"github.com/nerv-base/Oddward/internal/cnlogin/loginui"
)

const (
	smsQueryPath = "/m/newApi/query/whetherSetPassword"
	smsSendPath  = "/m/newApi/sendPhoneCaptchaWithOutLogin"
)

// smsService intercepts only synthetic SMS traffic; other endpoints use the
// existing offline fixture. Its gate models a request already sent but pending.
type smsService struct {
	base     *testService
	failAt   string
	failBody string
	gate     chan struct{}
	started  chan struct{}
	once     sync.Once
	queries  atomic.Int32
	sends    atomic.Int32
}

func (s *smsService) RoundTrip(req *http.Request) (*http.Response, error) {
	switch req.URL.Path {
	case smsQueryPath:
		s.queries.Add(1)
	case smsSendPath:
		s.sends.Add(1)
		if s.gate != nil {
			s.once.Do(func() { close(s.started) })
			select {
			case <-s.gate:
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
		}
	}
	if req.URL.Path == s.failAt {
		if s.failBody == "" {
			return nil, errors.New("synthetic transport failure")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(s.failBody))}, nil
	}
	return s.base.RoundTrip(req)
}

func newSMSRig(t *testing.T, store *fakeAccounts) (*testRig, *smsService) {
	t.Helper()
	rig := newRig(t, store, testPreLoginOK)
	service := &smsService{base: rig.service}
	// Configure the existing dependency seam before the first registration
	// event; no client has been constructed or requested yet.
	rig.session.deps.newClient = func(ctx context.Context, cfg laohu.Config, key laohu.AppKey, deviceID string) (*laohu.Client, error) {
		client, err := laohu.NewClient(cfg, laohu.Options{
			HTTPClient: &http.Client{Transport: service}, InstalledVersion: "1.2.3.4",
			DeviceID: deviceID, OSVersion: "Microsoft Windows NT 10.0.22631.0", AppKey: key, Now: rig.clock.Now,
		})
		if err != nil {
			return nil, err
		}
		if err := client.Init(ctx); err != nil {
			client.Close()
			return nil, err
		}
		return client, nil
	}
	return rig, service
}

func requireSMSModel(t *testing.T, m loginui.Model, label string, canSend bool) {
	t.Helper()
	if m.SMSLabel != label || m.CanSendSMS != canSend {
		t.Fatalf("SMS model: label=%q canSend=%v, want %q/%v", m.SMSLabel, m.CanSendSMS, label, canSend)
	}
}

func TestSMSFailureKeepsConsumedWindow(t *testing.T) {
	for _, tc := range []struct {
		name, path, body, status string
	}{
		{"password query failure", smsQueryPath, "", "登录请求失败，请稍后重试。"},
		{"SMS transport failure", smsSendPath, "", "登录请求失败，请稍后重试。"},
		{"SMS service rejection", smsSendPath, `{"code":17}`, "服务端拒绝了此次请求。"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newFakeAccounts()
			store.ack = true
			rig, service := newSMSRig(t, store)
			service.failAt, service.failBody = tc.path, tc.body
			rig.register(t)
			ui := rig.openLogin(t)
			ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "phone" })
			ui.events <- loginui.Event{Kind: "send_sms", Phone: testPhone}
			failed := ui.waitFor(t, func(m loginui.Model) bool { return m.Status == tc.status })
			requireSMSModel(t, failed, "60 秒后重发", false)
			if failed.Busy {
				t.Fatal("failed request left login controls busy")
			}
			ui.events <- loginui.Event{Kind: "send_sms", Phone: "13900000000"}
			blocked := ui.waitFor(t, func(m loginui.Model) bool { return m.Status == "请稍后再发送验证码。" })
			requireSMSModel(t, blocked, "60 秒后重发", false)
			if service.queries.Load() != 1 {
				t.Fatalf("cooldown was bypassed with another phone: queries=%d", service.queries.Load())
			}
		})
	}
}

func TestSMSInvalidPhoneDoesNotConsumeWindow(t *testing.T) {
	store := newFakeAccounts()
	store.ack = true
	rig, service := newSMSRig(t, store)
	rig.register(t)
	ui := rig.openLogin(t)
	ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "phone" })
	for _, invalid := range []string{"", "138bad", "23800138000", "+8613800138000"} {
		ui.events <- loginui.Event{Kind: "send_sms", Phone: invalid}
		model := ui.waitFor(t, func(m loginui.Model) bool { return m.Status == "请输入有效的中国大陆手机号。" })
		requireSMSModel(t, model, "发送验证码", true)
	}
	if service.queries.Load() != 0 || service.sends.Load() != 0 {
		t.Fatal("invalid phone reached a request endpoint")
	}
	ui.events <- loginui.Event{Kind: "send_sms", Phone: testPhone}
	sent := ui.waitFor(t, func(m loginui.Model) bool { return m.Status == "验证码已发送，请查收短信。" })
	requireSMSModel(t, sent, "60 秒后重发", false)
	if service.sends.Load() != 1 {
		t.Fatalf("valid SMS sends=%d, want 1", service.sends.Load())
	}
}

func TestSMSPendingRequestKeepsOriginalWindowAcrossReopen(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeAccounts()
		store.ack = true
		rig, service := newSMSRig(t, store)
		service.gate, service.started = make(chan struct{}), make(chan struct{})
		var release sync.Once
		defer release.Do(func() { close(service.gate) })
		rig.register(t)
		ui := rig.openLogin(t)
		ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "phone" })
		ui.events <- loginui.Event{Kind: "send_sms", Phone: testPhone}
		<-service.started
		// A forged duplicate while the HTTP call is pending cannot queue a send.
		ui.events <- loginui.Event{Kind: "send_sms", Phone: testPhone}
		rig.clock.Advance(10 * time.Second)
		pending := ui.waitFor(t, func(m loginui.Model) bool { return m.SMSLabel == "50 秒后重发" })
		if !pending.Busy || pending.CanSendSMS {
			t.Fatal("in-flight SMS did not keep the controls disabled")
		}
		ui.events <- loginui.Event{Kind: "cancel"}
		<-ui.closed
		reopened := rig.openLogin(t)
		model := reopened.waitFor(t, func(m loginui.Model) bool { return m.Screen == "phone" })
		requireSMSModel(t, model, "50 秒后重发", false)
		reopened.events <- loginui.Event{Kind: "send_sms", Phone: testPhone}
		reopened.waitFor(t, func(m loginui.Model) bool { return m.Status == "请稍后再发送验证码。" })
		release.Do(func() { close(service.gate) })
		finished := reopened.waitFor(t, func(m loginui.Model) bool { return m.Status == "验证码已发送，请查收短信。" })
		requireSMSModel(t, finished, "50 秒后重发", false)
		if service.sends.Load() != 1 {
			t.Fatalf("pending/reopened clicks sent %d SMS requests", service.sends.Load())
		}
		rig.clock.Advance(50 * time.Second)
		reopened.waitFor(t, func(m loginui.Model) bool { return m.CanSendSMS })
		reopened.events <- loginui.Event{Kind: "send_sms", Phone: testPhone}
		reopened.waitFor(t, func(m loginui.Model) bool { return m.Status == "验证码已发送，请查收短信。" && !m.Busy })
		if service.sends.Load() != 2 {
			t.Fatalf("60-second boundary did not permit the next request: sends=%d", service.sends.Load())
		}
	})
}

func TestSMSCooldownSurvivesHelpNavigationAndWindowLifecycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeAccounts()
		store.ack = true
		store.accounts = []accounts.Account{{ID: "fixture", Label: "Synthetic"}}
		rig, service := newSMSRig(t, store)
		rig.register(t)
		ui := rig.openLogin(t)
		ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "accounts" })
		ui.events <- loginui.Event{Kind: "other_account"}
		ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "phone" })
		ui.events <- loginui.Event{Kind: "send_sms", Phone: testPhone}
		ui.waitFor(t, func(m loginui.Model) bool { return m.Status == "验证码已发送，请查收短信。" })
		rig.clock.Advance(20 * time.Second)
		ui.events <- loginui.Event{Kind: "help"}
		helped := ui.waitFor(t, func(m loginui.Model) bool { return strings.Contains(m.Status, "遇到登录问题") })
		requireSMSModel(t, helped, "40 秒后重发", false)
		ui.events <- loginui.Event{Kind: "back"}
		ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "accounts" })
		rig.clock.Advance(time.Second)
		time.Sleep(time.Second)
		select {
		case m := <-ui.models:
			t.Fatalf("countdown refreshed the account editor: screen=%q", m.Screen)
		default:
		}
		ui.events <- loginui.Event{Kind: "back"}
		back := ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "phone" })
		requireSMSModel(t, back, "39 秒后重发", false)

		for _, closeKind := range []string{"cancel", "logout", "helper exit"} {
			switch closeKind {
			case "cancel":
				ui.events <- loginui.Event{Kind: "cancel"}
				<-ui.closed
			case "logout":
				rig.host.events <- HostEvent{Kind: HostLogout}
				<-ui.closed
			case "helper exit":
				close(ui.events)
				synctest.Wait()
			}
			ui = rig.openLogin(t)
			reopened := ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "phone" })
			requireSMSModel(t, reopened, "39 秒后重发", false)
			ui.events <- loginui.Event{Kind: "send_sms", Phone: testPhone}
			ui.waitFor(t, func(m loginui.Model) bool { return m.Status == "请稍后再发送验证码。" })
		}
		if service.queries.Load() != 1 || service.sends.Load() != 1 {
			t.Fatalf("navigation bypassed cooldown: queries=%d sends=%d", service.queries.Load(), service.sends.Load())
		}
	})
}

func TestSMSCountdownUpdatesWithoutUIEvents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		store := newFakeAccounts()
		store.ack = true
		rig := newRig(t, store, testPreLoginOK)
		rig.register(t)
		ui := rig.openLogin(t)
		ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "phone" })
		ui.events <- loginui.Event{Kind: "send_sms", Phone: testPhone}
		ui.waitFor(t, func(m loginui.Model) bool { return m.Status == "验证码已发送，请查收短信。" })

		for _, step := range []struct {
			advance time.Duration
			label   string
			canSend bool
		}{
			{time.Second, "59 秒后重发", false},
			{58*time.Second + 999*time.Millisecond, "1 秒后重发", false},
			{time.Millisecond, "发送验证码", true},
		} {
			rig.clock.Advance(step.advance)
			model := ui.waitFor(t, func(m loginui.Model) bool { return m.SMSLabel == step.label })
			if model.CanSendSMS != step.canSend {
				t.Fatalf("%q: CanSendSMS=%v, want %v", step.label, model.CanSendSMS, step.canSend)
			}
		}
		// An expired countdown must stop sending snapshots every second.
		time.Sleep(2 * time.Second)
		select {
		case m := <-ui.models:
			t.Fatalf("idle countdown sent an unnecessary model: %q", m.SMSLabel)
		default:
		}
	})
}

func TestSMSSentModelShowsConsumedCooldown(t *testing.T) {
	store := newFakeAccounts()
	store.ack = true
	rig := newRig(t, store, testPreLoginOK)
	rig.register(t)
	ui := rig.openLogin(t)
	ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "phone" })

	ui.events <- loginui.Event{Kind: "send_sms", Phone: testPhone}
	sent := ui.waitFor(t, func(m loginui.Model) bool { return m.Status == "验证码已发送，请查收短信。" })
	if sent.CanSendSMS || sent.SMSLabel != "60 秒后重发" {
		t.Fatalf("sent SMS model: CanSendSMS=%v SMSLabel=%q; want false and 60 秒后重发", sent.CanSendSMS, sent.SMSLabel)
	}
}
