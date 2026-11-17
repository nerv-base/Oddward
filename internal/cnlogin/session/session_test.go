// SPDX-License-Identifier: GPL-3.0-or-later

//go:build windows

package session

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nerv-base/Oddward/internal/cnlogin/accounts"
	"github.com/nerv-base/Oddward/internal/cnlogin/cnproto"
	"github.com/nerv-base/Oddward/internal/cnlogin/ecb"
	"github.com/nerv-base/Oddward/internal/cnlogin/laohu"
	"github.com/nerv-base/Oddward/internal/cnlogin/loginui"
	"github.com/nerv-base/Oddward/internal/process"
)

const (
	testAppKey      = "ABCDEFGHIJKLMNOPQRSTUVWXYZ012345"
	testDeviceID    = "00112233445566778899aabbccddeeff"
	testPhone       = "13800138000"
	testChannelJSON = `{"code":0,"result":{"userId":42,"token":"synthetic-token"}}`
	testGameJSON    = `{"code":0,"uid":"42","token":"synthetic-game-token","extraOne":{"note":"n","n":1}}`
	testPreLoginOK  = `{"code":0,"message":"ok","result":{"status":"0","realUser":1,"bannedType":null,"appId":1289,"userId":"42"}}`
	testPreLoginNo  = `{"code":0,"message":"ok","result":{"status":"0","realUser":0,"bannedType":null,"appId":1289,"userId":"42"}}`
)

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// testService answers the login HTTP endpoints with synthetic payloads.
type testService struct {
	mu       sync.Mutex
	requests []string
	tail     []byte
	channel  string
	game     string
	preLogin string
	// gate, when non-nil, holds the OneLogin response until it is closed or
	// the request is cancelled. It lets a test cancel a login in flight.
	gate chan struct{}
	// validateStatus, when non-zero, makes sdkValidate return that HTTP status
	// with an API code in the body.
	validateStatus int
}

func (s *testService) saw(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, request := range s.requests {
		if strings.HasSuffix(request, " "+path) {
			return true
		}
	}
	return false
}

func (s *testService) RoundTrip(req *http.Request) (*http.Response, error) {
	s.mu.Lock()
	s.requests = append(s.requests, req.Method+" "+req.URL.Path)
	s.mu.Unlock()
	var body string
	status := http.StatusOK
	switch req.URL.Path {
	case "/m/newApi/initConfig":
		body = `{"code":0,"result":{"loginOptionConfig":[16,92]}}`
	case "/m/newApi/query/whetherSetPassword":
		body = `{"code":0,"result":{"havePwd":false}}`
	case "/m/newApi/sendPhoneCaptchaWithOutLogin":
		body = `{"code":0}`
	case "/openApi/sms/new/login":
		body = s.channel
	case "/openApi/sdkValidate":
		s.mu.Lock()
		validateStatus := s.validateStatus
		s.mu.Unlock()
		if validateStatus != 0 {
			status = validateStatus
			code := "400"
			if status != http.StatusBadRequest {
				code = "500"
			}
			body = `{"code":` + code + `,"message":"synthetic rejection"}`
		} else {
			body = `{"code":0}`
		}
	case "/sdkapi/v2/user/login":
		s.mu.Lock()
		gate := s.gate
		s.mu.Unlock()
		if gate != nil {
			select {
			case <-gate:
			case <-req.Context().Done():
				return nil, req.Context().Err()
			}
		}
		encrypted, err := ecb.Encrypt(s.tail, []byte(s.game))
		if err != nil {
			return nil, err
		}
		body = base64.StdEncoding.EncodeToString(encrypted)
	case "/sdkapi/preLogin":
		body = s.preLogin
	default:
		return nil, fmt.Errorf("unexpected request %s", req.URL)
	}
	return &http.Response{
		StatusCode:    status,
		Header:        http.Header{"Content-Type": []string{"application/json"}},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
	}, nil
}

type fakeHost struct {
	events  chan HostEvent
	logins  chan cnproto.LoginIdentity
	replies chan cnproto.PreLoginReply

	mu        sync.Mutex
	completed int
	boundPID  uint32
	boundTime uint64
	closed    bool
	sendErr   error

	// queryOnSend, when set, makes SendLogin emulate the game asking for the
	// pre-login result before the delivery completion reaches the session loop.
	queryOnSend *cnproto.Query
}

func newFakeHost() *fakeHost {
	return &fakeHost{
		events:  make(chan HostEvent),
		logins:  make(chan cnproto.LoginIdentity, 8),
		replies: make(chan cnproto.PreLoginReply, 8),
	}
}

func (h *fakeHost) PipeName() string { return `\\.\pipe\oddward-test` }

func (h *fakeHost) BindGame(pid uint32, createdAt uint64) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.boundPID, h.boundTime = pid, createdAt
	return nil
}

func (h *fakeHost) Events() <-chan HostEvent { return h.events }

func (h *fakeHost) SendLogin(ctx context.Context, id cnproto.LoginIdentity) error {
	h.mu.Lock()
	err := h.sendErr
	h.mu.Unlock()
	if err != nil {
		return err
	}
	h.logins <- copyIdentity(id)
	if h.queryOnSend != nil {
		query := *h.queryOnSend
		h.events <- HostEvent{Kind: HostQuery, Query: query}
	}
	return nil
}

func copyIdentity(id cnproto.LoginIdentity) cnproto.LoginIdentity {
	return cnproto.LoginIdentity{
		GameUID:      append([]byte(nil), id.GameUID...),
		GameToken:    append([]byte(nil), id.GameToken...),
		ChannelUID:   append([]byte(nil), id.ChannelUID...),
		ChannelToken: append([]byte(nil), id.ChannelToken...),
		Info:         append([]byte(nil), id.Info...),
	}
}

func (h *fakeHost) Reply(ctx context.Context, reply cnproto.PreLoginReply) error {
	h.replies <- reply
	return nil
}

func (h *fakeHost) CompleteLoginRequest() {
	h.mu.Lock()
	h.completed++
	h.mu.Unlock()
}

func (h *fakeHost) Close() error {
	h.mu.Lock()
	h.closed = true
	h.mu.Unlock()
	return nil
}

func (h *fakeHost) completions() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.completed
}

type fakeGame struct {
	identity process.Identity
	wait     chan struct{}
	code     uint32
	err      error

	mu     sync.Mutex
	closed bool
}

func (g *fakeGame) Identity() process.Identity { return g.identity }

func (g *fakeGame) Wait(ctx context.Context) (uint32, error) {
	select {
	case <-g.wait:
		return g.code, g.err
	case <-ctx.Done():
		return 0, ctx.Err()
	}
}

func (g *fakeGame) Close() error {
	g.mu.Lock()
	g.closed = true
	g.mu.Unlock()
	return nil
}

type fakeUI struct {
	models chan loginui.Model
	events chan loginui.Event
	closed chan struct{}
	once   sync.Once
}

func newFakeUI(initial loginui.Model) *fakeUI {
	ui := &fakeUI{
		models: make(chan loginui.Model, 128),
		events: make(chan loginui.Event),
		closed: make(chan struct{}),
	}
	ui.models <- initial
	return ui
}

func (u *fakeUI) Update(ctx context.Context, model loginui.Model) error {
	select {
	case u.models <- model:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (u *fakeUI) Events() <-chan loginui.Event { return u.events }

func (u *fakeUI) Close() error {
	u.once.Do(func() { close(u.closed) })
	return nil
}

func (u *fakeUI) waitFor(t *testing.T, match func(loginui.Model) bool) loginui.Model {
	t.Helper()
	deadline := time.After(3 * time.Second)
	var last loginui.Model
	for {
		select {
		case model := <-u.models:
			last = model
			if match(model) {
				return model
			}
		case <-deadline:
			t.Fatalf("login window did not reach the expected state; last model: screen=%q status=%q accounts=%d selected=%q busy=%v",
				last.Screen, last.Status, len(last.Accounts), last.SelectedAccountID, last.Busy)
			return loginui.Model{}
		}
	}
}

type fakePending struct {
	parent  *fakeAccounts
	account accounts.Account
	done    bool
}

func (p *fakePending) Account() accounts.Account { return p.account }

func (p *fakePending) Commit() error {
	if p.done {
		return nil
	}
	p.done = true
	p.parent.mu.Lock()
	defer p.parent.mu.Unlock()
	p.parent.commits++
	p.parent.accounts = append(p.parent.accounts, p.account)
	return nil
}

func (p *fakePending) Rollback() error {
	if p.done {
		return nil
	}
	p.done = true
	p.parent.mu.Lock()
	defer p.parent.mu.Unlock()
	p.parent.rollbacks++
	return nil
}

type fakeAccounts struct {
	mu        sync.Mutex
	accounts  []accounts.Account
	last      string
	ack       bool
	unlock    map[string]accounts.Secret
	unlockErr error

	prepared  []accounts.Account
	commits   int
	rollbacks int
	accepted  int
	savedLast []string
}

func newFakeAccounts() *fakeAccounts {
	return &fakeAccounts{unlock: map[string]accounts.Secret{}}
}

func (f *fakeAccounts) List() ([]accounts.Account, int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]accounts.Account(nil), f.accounts...), len(f.accounts), nil
}

func (f *fakeAccounts) Unlock(id string) (accounts.Secret, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unlockErr != nil {
		return accounts.Secret{}, f.unlockErr
	}
	secret, ok := f.unlock[id]
	if !ok {
		return accounts.Secret{}, accounts.ErrNotFound
	}
	return secret, nil
}

func (f *fakeAccounts) Last() (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.last, nil
}

func (f *fakeAccounts) SetLast(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.last = id
	f.savedLast = append(f.savedLast, id)
	return nil
}

func (f *fakeAccounts) Rename(id, label string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.accounts {
		if f.accounts[i].ID == id {
			f.accounts[i].Label = label
			return nil
		}
	}
	return accounts.ErrNotFound
}

func (f *fakeAccounts) Remove(id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.accounts {
		if f.accounts[i].ID == id {
			f.accounts = append(f.accounts[:i], f.accounts[i+1:]...)
			return nil
		}
	}
	return accounts.ErrNotFound
}

func (f *fakeAccounts) Prepare(account accounts.Account, secret accounts.Secret) (accounts.Pending, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepared = append(f.prepared, account)
	return &fakePending{parent: f, account: accounts.Account{ID: "new-account", Label: account.Label}}, nil
}

func (f *fakeAccounts) NoticeAcknowledged(revision int) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ack, nil
}

func (f *fakeAccounts) AcceptNotice(revision int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ack = true
	f.accepted = revision
	return nil
}

func (f *fakeAccounts) counts() (commits, rollbacks, prepared int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.commits, f.rollbacks, len(f.prepared)
}

type testRig struct {
	host     *fakeHost
	game     *fakeGame
	accounts *fakeAccounts
	service  *testService
	clock    *testClock
	session  *Session

	uiCreated chan *fakeUI
}

func newRig(t *testing.T, accountStore *fakeAccounts, preLogin string, configure ...func(*deps)) *testRig {
	t.Helper()
	service := &testService{
		tail:     []byte("QRSTUVWXYZ012345"),
		channel:  testChannelJSON,
		game:     testGameJSON,
		preLogin: preLogin,
	}
	host := newFakeHost()
	game := &fakeGame{identity: process.Identity{PID: 4321, CreationTime: 987654321}, wait: make(chan struct{})}
	clock := &testClock{now: time.UnixMilli(1700000000000)}
	rig := &testRig{
		host:      host,
		game:      game,
		accounts:  accountStore,
		service:   service,
		clock:     clock,
		uiCreated: make(chan *fakeUI, 4),
	}
	d := deps{
		host:     host,
		game:     game,
		accounts: accountStore,
		ui: func(ctx context.Context, model loginui.Model) (UI, error) {
			ui := newFakeUI(model)
			rig.uiCreated <- ui
			return ui, nil
		},
		loadConfig: func(ctx context.Context, key [16]byte) (laohu.Config, error) {
			return laohu.Config{
				AppVersion:    "9.8.7",
				PackageName:   "com.example.nte",
				ChannelID:     9,
				Platform:      4,
				ChannelAppID:  "10001",
				ChannelAppKey: "0123456789abcdefghijklmnopqrstuv",
			}, nil
		},
		newClient: func(ctx context.Context, cfg laohu.Config, key laohu.AppKey, deviceID string) (*laohu.Client, error) {
			client, err := laohu.NewClient(cfg, laohu.Options{
				HTTPClient:       &http.Client{Transport: service},
				InstalledVersion: "1.2.3.4",
				DeviceID:         deviceID,
				OSVersion:        "Microsoft Windows NT 10.0.22631.0",
				AppKey:           key,
				Now:              clock.Now,
			})
			if err != nil {
				return nil, err
			}
			if err := client.Init(ctx); err != nil {
				client.Close()
				return nil, err
			}
			return client, nil
		},
		now: clock.Now,
	}
	for _, setup := range configure {
		setup(&d)
	}
	rig.session = newSession(d)
	go rig.session.loop()
	t.Cleanup(func() { _ = rig.session.Close() })
	return rig
}

func (r *testRig) register(t *testing.T) {
	t.Helper()
	key, err := laohu.NewAppKey([]byte(testAppKey))
	if err != nil {
		t.Fatalf("app key: %v", err)
	}
	r.host.events <- HostEvent{Kind: HostRegistered, Key: key, ConfigKey: [16]byte{1, 2, 3}}
}

func (r *testRig) openLogin(t *testing.T) *fakeUI {
	t.Helper()
	r.host.events <- HostEvent{Kind: HostLoginRequested}
	select {
	case ui := <-r.uiCreated:
		return ui
	case <-time.After(3 * time.Second):
		t.Fatal("login window was not opened")
		return nil
	}
}

func (r *testRig) noLogin(t *testing.T) {
	t.Helper()
	select {
	case identity := <-r.host.logins:
		t.Fatalf("identity was delivered without admission: uid=%q", identity.GameUID)
	case <-time.After(150 * time.Millisecond):
	}
}

func TestFirstLoginRequiresNoticeThenSendsCode(t *testing.T) {
	store := newFakeAccounts()
	rig := newRig(t, store, testPreLoginOK)
	rig.register(t)
	ui := rig.openLogin(t)

	notice := ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "notice" })
	if notice.PrimaryLabel != "我已了解并同意继续" {
		t.Fatalf("primary label = %q", notice.PrimaryLabel)
	}
	if len(notice.Notice) != 8 {
		t.Fatalf("notice sections = %d, want 8", len(notice.Notice))
	}

	ui.events <- loginui.Event{Kind: "acknowledge"}
	ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "phone" })
	if store.accepted != NoticeRevision {
		t.Fatalf("accepted revision = %d", store.accepted)
	}

	ui.events <- loginui.Event{Kind: "send_sms", Phone: testPhone}
	sent := ui.waitFor(t, func(m loginui.Model) bool { return m.Status == "验证码已发送，请查收短信。" })
	if sent.Busy {
		t.Fatal("window still busy after the code was sent")
	}
}

func TestPhoneLoginDeliversIdentityAfterAdmission(t *testing.T) {
	store := newFakeAccounts()
	store.ack = true
	rig := newRig(t, store, testPreLoginOK)
	rig.register(t)
	ui := rig.openLogin(t)
	ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "phone" })

	ui.events <- loginui.Event{Kind: "send_sms", Phone: testPhone}
	ui.waitFor(t, func(m loginui.Model) bool { return m.Status == "验证码已发送，请查收短信。" })

	ui.events <- loginui.Event{Kind: "login_phone", Phone: testPhone, Captcha: "123456"}
	ui.waitClosed(t)

	select {
	case identity := <-rig.host.logins:
		if string(identity.GameUID) != "42" || string(identity.GameToken) != "synthetic-game-token" {
			t.Fatalf("delivered game identity = %q/%q", identity.GameUID, identity.GameToken)
		}
		if string(identity.ChannelUID) != "42" || string(identity.ChannelToken) != "synthetic-token" {
			t.Fatalf("delivered channel identity = %q/%q", identity.ChannelUID, identity.ChannelToken)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("identity was not delivered")
	}
}

func TestCancelDuringLoginPreventsDelivery(t *testing.T) {
	store := newFakeAccounts()
	store.ack = true
	rig := newRig(t, store, testPreLoginOK)
	rig.service.mu.Lock()
	rig.service.gate = make(chan struct{})
	gate := rig.service.gate
	rig.service.mu.Unlock()
	rig.register(t)
	ui := rig.openLogin(t)
	ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "phone" })

	ui.events <- loginui.Event{Kind: "login_phone", Phone: testPhone, Captcha: "123456"}
	deadline := time.Now().Add(3 * time.Second)
	for !rig.service.saw("/sdkapi/v2/user/login") {
		if time.Now().After(deadline) {
			t.Fatal("login request never reached the service")
		}
		time.Sleep(5 * time.Millisecond)
	}

	ui.events <- loginui.Event{Kind: "cancel"}
	select {
	case <-ui.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("login window did not close")
	}
	close(gate)
	rig.noLogin(t)
}

func TestPreLoginQueryRepliesAfterLoginWindowCloses(t *testing.T) {
	store := newFakeAccounts()
	store.ack = true
	rig := newRig(t, store, testPreLoginOK)
	rig.register(t)
	ui := rig.openLogin(t)
	ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "phone" })

	ui.events <- loginui.Event{Kind: "login_phone", Phone: testPhone, Captcha: "123456"}
	ui.waitClosed(t)

	rig.host.events <- HostEvent{Kind: HostQuery, Query: cnproto.Query{Type: cnproto.QueryPreLogin, ContextID: 77}}
	select {
	case reply := <-rig.host.replies:
		if reply.Code != 0 || reply.ContextID != 77 || reply.Message != "ok" {
			t.Fatalf("pre-login reply = %+v", reply)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pre-login query was not answered")
	}
}

func TestPreLoginQueryDuringDeliveryIsAnsweredAfterDelivery(t *testing.T) {
	store := newFakeAccounts()
	store.ack = true
	rig := newRig(t, store, testPreLoginOK)
	rig.register(t)
	ui := rig.openLogin(t)
	ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "phone" })

	// The game may ask for the pre-login result between SendLogin and the
	// delivery completion. The session must remember the query and answer it
	// once the cached result exists.
	rig.host.queryOnSend = &cnproto.Query{Type: cnproto.QueryPreLogin, ContextID: 88}

	ui.events <- loginui.Event{Kind: "login_phone", Phone: testPhone, Captcha: "123456"}
	select {
	case identity := <-rig.host.logins:
		if string(identity.GameUID) != "42" {
			t.Fatalf("delivered game identity = %q", identity.GameUID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("identity was not delivered")
	}

	select {
	case reply := <-rig.host.replies:
		if reply.Code != 0 || reply.ContextID != 88 || reply.Message != "ok" {
			t.Fatalf("pre-login reply = %+v", reply)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("pre-login query that arrived during delivery was not answered")
	}
}

func TestDeniedPreLoginNeverDeliversIdentity(t *testing.T) {
	store := newFakeAccounts()
	store.ack = true
	rig := newRig(t, store, testPreLoginNo)
	rig.register(t)
	ui := rig.openLogin(t)
	ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "phone" })

	ui.events <- loginui.Event{Kind: "send_sms", Phone: testPhone}
	ui.waitFor(t, func(m loginui.Model) bool { return m.Status == "验证码已发送，请查收短信。" })
	ui.events <- loginui.Event{Kind: "login_phone", Phone: testPhone, Captcha: "123456"}

	blocked := ui.waitFor(t, func(m loginui.Model) bool { return strings.Contains(m.Status, "实名") })
	if blocked.Busy {
		t.Fatal("window still busy after a blocked login")
	}
	rig.noLogin(t)
}

func TestRememberSavesOnlyAfterDelivery(t *testing.T) {
	store := newFakeAccounts()
	store.ack = true
	rig := newRig(t, store, testPreLoginOK)
	rig.register(t)
	ui := rig.openLogin(t)
	ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "phone" })

	ui.events <- loginui.Event{Kind: "login_phone", Phone: testPhone, Captcha: "123456", Remember: true}
	ui.waitClosed(t)

	commits, rollbacks, prepared := store.counts()
	if prepared != 1 || commits != 1 || rollbacks != 0 {
		t.Fatalf("prepare=%d commit=%d rollback=%d, want 1/1/0", prepared, commits, rollbacks)
	}
}

func TestRememberRollsBackWhenAdmissionBlocks(t *testing.T) {
	store := newFakeAccounts()
	store.ack = true
	rig := newRig(t, store, testPreLoginNo)
	rig.register(t)
	ui := rig.openLogin(t)
	ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "phone" })

	ui.events <- loginui.Event{Kind: "login_phone", Phone: testPhone, Captcha: "123456", Remember: true}
	ui.waitFor(t, func(m loginui.Model) bool { return strings.Contains(m.Status, "实名") })

	commits, rollbacks, prepared := store.counts()
	if prepared != 1 || commits != 0 || rollbacks != 1 {
		t.Fatalf("prepare=%d commit=%d rollback=%d, want 1/0/1", prepared, commits, rollbacks)
	}
}

func TestSavedAccountLoginValidatesAndDelivers(t *testing.T) {
	store := newFakeAccounts()
	store.ack = true
	store.accounts = []accounts.Account{{ID: "account-1", Label: "138****0000"}}
	store.unlock["account-1"] = accounts.Secret{DeviceID: testDeviceID, ChannelUID: "42", ChannelToken: "synthetic-token"}
	rig := newRig(t, store, testPreLoginOK)
	rig.register(t)
	ui := rig.openLogin(t)

	accountsModel := ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "accounts" })
	if len(accountsModel.Accounts) != 1 || accountsModel.SelectedAccountID != "account-1" {
		t.Fatalf("accounts model = %+v", accountsModel)
	}
	ui.events <- loginui.Event{Kind: "login_saved", AccountID: "account-1"}
	ui.waitClosed(t)

	select {
	case identity := <-rig.host.logins:
		if string(identity.GameUID) != "42" {
			t.Fatalf("delivered uid = %q", identity.GameUID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("identity was not delivered")
	}
	store.mu.Lock()
	last := store.last
	store.mu.Unlock()
	if last != "account-1" {
		t.Fatalf("last account = %q", last)
	}
}

func TestSavedAccountListMarksTajiduoLink(t *testing.T) {
	store := newFakeAccounts()
	store.ack = true
	store.accounts = []accounts.Account{
		{ID: "account-1", Label: "138****0000", TajiduoLinked: true, TajiduoState: "linked"},
		{ID: "account-2", Label: "139****0000"},
		{ID: "account-3", Label: "137****0000", TajiduoState: "needs-relink"},
	}
	rig := newRig(t, store, testPreLoginOK)
	rig.register(t)
	ui := rig.openLogin(t)

	m := ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "accounts" })
	if len(m.Accounts) != 3 {
		t.Fatalf("accounts model = %+v", m)
	}
	if m.Accounts[0].MaskedPhone != "已关联塔吉多" || m.Accounts[1].MaskedPhone != "" || m.Accounts[2].MaskedPhone != "已关联塔吉多（需重新关联）" {
		t.Fatalf("linked accounts not marked: %+v", m.Accounts)
	}
}

func TestSavedAccountLoginToleratesRequestRejectedAPI400(t *testing.T) {
	store := newFakeAccounts()
	store.ack = true
	store.accounts = []accounts.Account{{ID: "account-1", Label: "138****0000"}}
	store.unlock["account-1"] = accounts.Secret{DeviceID: testDeviceID, ChannelUID: "42", ChannelToken: "synthetic-token"}
	rig := newRig(t, store, testPreLoginOK)
	rig.service.validateStatus = http.StatusBadRequest
	rig.register(t)
	ui := rig.openLogin(t)

	ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "accounts" })
	ui.events <- loginui.Event{Kind: "login_saved", AccountID: "account-1"}
	ui.waitClosed(t)

	select {
	case identity := <-rig.host.logins:
		if string(identity.GameUID) != "42" {
			t.Fatalf("delivered uid = %q", identity.GameUID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("identity was not delivered")
	}
}

func TestSavedAccountServiceRejectionFallsBackToPhone(t *testing.T) {
	store := newFakeAccounts()
	store.ack = true
	store.accounts = []accounts.Account{{ID: "account-1", Label: "138****0000"}}
	store.unlock["account-1"] = accounts.Secret{DeviceID: testDeviceID, ChannelUID: "42", ChannelToken: "synthetic-token"}
	rig := newRig(t, store, testPreLoginOK)
	rig.service.validateStatus = http.StatusForbidden
	rig.register(t)
	ui := rig.openLogin(t)

	ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "accounts" })
	ui.events <- loginui.Event{Kind: "login_saved", AccountID: "account-1"}
	phone := ui.waitFor(t, func(m loginui.Model) bool { return m.Screen == "phone" })
	if !strings.Contains(phone.Status, "拒绝") {
		t.Fatalf("phone status = %q", phone.Status)
	}
	select {
	case identity := <-rig.host.logins:
		t.Fatalf("identity delivered after validation rejection: %+v", identity)
	default:
	}
}

func TestLogoutClosesWindowAndReopensOnNextRequest(t *testing.T) {
	store := newFakeAccounts()
	store.ack = true
	rig := newRig(t, store, testPreLoginOK)
	rig.register(t)
	first := rig.openLogin(t)
	first.waitFor(t, func(m loginui.Model) bool { return m.Screen == "phone" })

	rig.host.events <- HostEvent{Kind: HostLogout}
	select {
	case <-first.closed:
	case <-time.After(2 * time.Second):
		t.Fatal("login window was not closed on logout")
	}

	second := rig.openLogin(t)
	second.waitFor(t, func(m loginui.Model) bool { return m.Screen == "phone" })
}
